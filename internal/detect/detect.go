package detect

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jborkowski/vmc/internal/config"
)

func Run(db *sql.DB, cfg *config.Config) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	appleDBPath := cfg.AppleDBPath
	if appleDBPath == "" {
		appleDBPath = filepath.Join(homeDir, "Library", "Application Support", "com.apple.voicememos", "Recordings", "CloudRecordings.db")
	}
	var appleErr error
	if _, err := os.Stat(appleDBPath); os.IsNotExist(err) || os.IsPermission(err) {
		appleErr = fmt.Errorf("cannot open Voice Memos database — grant Full Disk Access to vmc. Path: %s", appleDBPath)
		if !cfg.DJI.Enabled {
			return appleErr
		}
	}

	shardDir := cfg.ShardDir
	if strings.HasPrefix(shardDir, "~/") {
		shardDir = filepath.Join(homeDir, shardDir[2:])
	}
	if err := os.MkdirAll(shardDir, 0755); err != nil {
		return fmt.Errorf("failed to create shard directory: %w", err)
	}

	defer db.Exec("DROP TABLE IF EXISTS known_apple")
	defer db.Exec("DROP TABLE IF EXISTS apple_snapshot")
	defer db.Exec("DROP TABLE IF EXISTS pending")

	// Collect dedup state BEFORE touching Apple's DB so network I/O never
	// overlaps with a live ATTACH on CloudRecordings.db.
	dedupMode := "local+hf"
	// remoteComplete gates DJI: its dedup key and slot allocation are only
	// safe against the full published set, so DJI fails closed.
	remoteComplete := true
	remoteMaxShard := 0
	var remoteRows []knownRow
	if cfg.HFToken != "" && cfg.HFRepo != "" {
		rows, failedFiles, maxShard, err := fetchRemoteRows(cfg)
		if err != nil {
			slog.Warn("HF remote dedup failed, falling back to local-only", "error", err)
			dedupMode = "local-only"
			remoteComplete = false
		} else {
			remoteRows = rows
			remoteMaxShard = maxShard
			if failedFiles > 0 {
				remoteComplete = false
			}
		}
	} else {
		dedupMode = "local-only"
	}

	localShardsPattern := filepath.Join(shardDir, "*.parquet")
	matches, _ := filepath.Glob(localShardsPattern)
	localComplete := true
	var localRows []knownRow
	if len(matches) > 0 {
		rows, err := readLocalRows(db, localShardsPattern)
		if err != nil {
			slog.Warn("failed to read local shards for dedup", "error", err)
			localComplete = false
		}
		localRows = rows
	}

	known := append(remoteRows, localRows...)
	knownIDs := make([]int64, 0, len(known))
	for _, k := range known {
		knownIDs = append(knownIDs, k.ID)
	}
	if err := createKnownAppleTable(db, knownIDs); err != nil {
		return fmt.Errorf("failed to build dedup table: %w", err)
	}

	// Snapshot Apple DB (main + WAL/SHM), attach briefly, copy rows into DuckDB, DETACH.
	// With DJI enabled an Apple failure is reported at the end instead, so DJI still runs.
	if appleErr == nil {
		appleErr = snapshotAndLoadApple(db, appleDBPath, filepath.Dir(appleDBPath))
		if appleErr != nil && !cfg.DJI.Enabled {
			return appleErr
		}
	}
	if appleErr != nil {
		slog.Warn("Voice Memos detect failed, continuing with DJI only", "error", appleErr)
		db.Exec("DROP TABLE IF EXISTS apple_snapshot")
		if _, err := db.Exec(emptyAppleSnapshotSQL); err != nil {
			return errors.Join(appleErr, err)
		}
	}

	// Slot IDs are permanent once published, so stay on them even if DJI is later disabled.
	useSlots := cfg.DJI.Enabled
	for _, id := range knownIDs {
		if !IsLegacyID(id) {
			useSlots = true
			break
		}
	}
	appleIDExpr := "zpk"
	if useSlots {
		appleIDExpr = fmt.Sprintf("CAST(%d AS BIGINT) + zpk * %d", SlotBase, SlotWidth)
	}
	if _, err := db.Exec(fmt.Sprintf(`
		CREATE TEMP TABLE pending AS
		SELECT
			%s AS recording_id,
			audio_path, title, created_at, duration_seconds, transcription,
			latitude, longitude, place_name, device, folder
		FROM apple_snapshot
		WHERE zpk NOT IN (SELECT zpk FROM known_apple)
	`, appleIDExpr)); err != nil {
		return fmt.Errorf("failed to stage new memos: %w", err)
	}

	var djiErr error
	djiNew := 0
	if cfg.DJI.Enabled {
		switch {
		case !remoteComplete:
			slog.Warn("skipping DJI pass: HF remote listing incomplete")
		case !localComplete:
			slog.Warn("skipping DJI pass: local shards unreadable")
		default:
			djiNew, djiErr = stageDJI(db, cfg.DJI, known, knownIDs)
			if djiErr != nil {
				slog.Warn("DJI detect failed", "error", djiErr)
			}
		}
	}

	// Local shards may have been removed after upload. Include the Hub's
	// highest shard number so new local partitions never reuse published names.
	maxShard := remoteMaxShard
	for _, m := range matches {
		name := filepath.Base(m)
		var num int
		if _, err := fmt.Sscanf(name, "shard_%d.parquet", &num); err == nil {
			if num > maxShard {
				maxShard = num
			}
		}
	}

	shardMaxRows := cfg.ShardMaxRows
	if shardMaxRows <= 0 {
		shardMaxRows = 10
	}

	var totalNew int64
	if err := db.QueryRow("SELECT COUNT(*) FROM pending").Scan(&totalNew); err != nil {
		return fmt.Errorf("failed to count new memos: %w", err)
	}

	if totalNew == 0 {
		slog.Info("no new memos detected")
		return errors.Join(appleErr, djiErr)
	}

	var totalWritten int64
	for offset := int64(0); offset < totalNew; offset += int64(shardMaxRows) {
		maxShard++
		tempShardPath := filepath.Join(shardDir, fmt.Sprintf("shard_%04d_tmp.parquet", maxShard))
		finalShardPath := filepath.Join(shardDir, fmt.Sprintf("shard_%04d.parquet", maxShard))

		copyQuery := fmt.Sprintf(`
			COPY (
				SELECT
					recording_id,
					CAST(NULL AS BLOB) AS audio,
					CAST(NULL AS BLOB) AS audio_original,
					audio_path,
					title,
					created_at,
					duration_seconds,
					transcription,
					latitude,
					longitude,
					place_name,
					device,
					folder
				FROM pending
				ORDER BY recording_id
				LIMIT %d OFFSET %d
			) TO '%s' (FORMAT PARQUET, ROW_GROUP_SIZE 1)
		`, shardMaxRows, offset, strings.ReplaceAll(tempShardPath, "'", "''"))

		var rowsWritten int64
		if err := db.QueryRow(copyQuery).Scan(&rowsWritten); err != nil {
			return fmt.Errorf("failed to write shard %d: %w", maxShard, err)
		}

		if rowsWritten == 0 {
			os.Remove(tempShardPath)
			break
		}

		if err := os.Rename(tempShardPath, finalShardPath); err != nil {
			return fmt.Errorf("failed to rename temp shard: %w", err)
		}

		totalWritten += rowsWritten
		slog.Info("wrote shard", "shard", finalShardPath, "rows", rowsWritten)
	}

	slog.Info("detect phase complete",
		slog.Int64("memos_found", totalWritten),
		slog.Int("dji_found", djiNew),
		slog.Int("shard_count", maxShard),
		slog.String("dedup_mode", dedupMode),
	)

	return errors.Join(appleErr, djiErr)
}

const emptyAppleSnapshotSQL = `CREATE TEMP TABLE apple_snapshot (
	zpk BIGINT, audio_path VARCHAR, title VARCHAR, created_at VARCHAR,
	duration_seconds DOUBLE, transcription VARCHAR, latitude DOUBLE,
	longitude DOUBLE, place_name VARCHAR, device VARCHAR, folder VARCHAR)`

// stageDJI scans the DJI inbox, drops already-known recordings, allocates slot
// IDs after the highest known Apple Z_PK and inserts the rest into pending.
func stageDJI(db *sql.DB, d config.DJI, known []knownRow, knownIDs []int64) (int, error) {
	recs, err := scanDJI(d, time.Now())
	if errors.Is(err, errDJILocked) {
		slog.Info("skipping DJI pass: dji-mic pull lock present")
		return 0, nil
	}
	if err != nil {
		return 0, err
	}

	device := djiDeviceLabel(d)
	recs = newDJIRecordings(recs, known, device)
	if len(recs) == 0 {
		return 0, nil
	}

	var slot int64
	if err := db.QueryRow("SELECT COALESCE(MAX(zpk), 0) FROM apple_snapshot").Scan(&slot); err != nil {
		return 0, fmt.Errorf("failed to find highest Apple Z_PK: %w", err)
	}
	for _, id := range knownIDs {
		slot = max(slot, DecodeAppleZPK(id))
	}
	recs = assignDJIIDs(recs, knownIDs, slot)

	stmt, err := db.Prepare(`INSERT INTO pending
		(recording_id, audio_path, title, created_at, duration_seconds, transcription,
		 latitude, longitude, place_name, device, folder)
		VALUES (?, ?, NULL, ?, ?, NULL, NULL, NULL, NULL, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()
	for _, r := range recs {
		if _, err := stmt.Exec(r.ID, r.AudioPath, r.CreatedAt, r.Duration, device, r.Folder); err != nil {
			return 0, fmt.Errorf("failed to stage dji recording %s: %w", r.AudioPath, err)
		}
	}
	slog.Info("staged DJI recordings", "count", len(recs), "slot", slot)
	return len(recs), nil
}

// snapshotAndLoadApple copies CloudRecordings.db (+ WAL/SHM when present),
// attaches the copy read-only just long enough to materialize apple_snapshot,
// then detaches so the live Voice Memos DB is never held across later work.
func snapshotAndLoadApple(db *sql.DB, appleDBPath, recordingsDir string) error {
	snapPath, cleanup, err := snapshotAppleDB(appleDBPath)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := attachApplePath(db, snapPath); err != nil {
		return err
	}
	defer db.Exec("DETACH apple;")

	rows, err := db.Query("SELECT column_name, data_type FROM information_schema.columns WHERE table_name = 'ZCLOUDRECORDING'")
	if err != nil {
		return fmt.Errorf("failed to inspect ZCLOUDRECORDING schema: %w", err)
	}

	cols := make(map[string]bool)
	colTypes := make(map[string]string)
	for rows.Next() {
		var name, dtype string
		if err := rows.Scan(&name, &dtype); err == nil {
			cols[strings.ToUpper(name)] = true
			colTypes[strings.ToUpper(name)] = strings.ToUpper(dtype)
		}
	}
	rows.Close()

	if !cols["Z_PK"] || !cols["ZDATE"] || !cols["ZPATH"] {
		return fmt.Errorf("apple Voice Memos DB is missing required columns (Z_PK, ZDATE, ZPATH)")
	}

	titleCol := "CAST(NULL AS VARCHAR)"
	if cols["ZCUSTOMLABEL"] && cols["ZENCRYPTEDTITLE"] {
		titleCol = "COALESCE(CAST(ZENCRYPTEDTITLE AS VARCHAR), CAST(ZCUSTOMLABEL AS VARCHAR))"
	} else if cols["ZENCRYPTEDTITLE"] {
		titleCol = "CAST(ZENCRYPTEDTITLE AS VARCHAR)"
	} else if cols["ZCUSTOMLABEL"] {
		titleCol = "CAST(ZCUSTOMLABEL AS VARCHAR)"
	}

	transcriptionCol := "CAST(NULL AS VARCHAR)"
	if cols["ZTRANSCRIPTION"] {
		transcriptionCol = "CAST(ZTRANSCRIPTION AS VARCHAR)"
	}

	latCol := "CAST(NULL AS DOUBLE)"
	if cols["ZLATITUDE"] {
		latCol = "CAST(ZLATITUDE AS DOUBLE)"
	}

	lonCol := "CAST(NULL AS DOUBLE)"
	if cols["ZLONGITUDE"] {
		lonCol = "CAST(ZLONGITUDE AS DOUBLE)"
	}

	placeCol := "CAST(NULL AS VARCHAR)"
	if cols["ZPLACENAME"] {
		placeCol = "CAST(ZPLACENAME AS VARCHAR)"
	}

	deviceCol := "CAST(NULL AS VARCHAR)"
	if cols["ZDEVICE"] {
		deviceCol = "CAST(ZDEVICE AS VARCHAR)"
	}

	folderCol := "CAST(NULL AS VARCHAR)"
	if cols["ZFOLDER"] {
		folderCol = "CAST(ZFOLDER AS VARCHAR)"
	}

	durationCol := "CAST(NULL AS DOUBLE)"
	if cols["ZDURATION"] {
		durationCol = "CAST(ZDURATION AS DOUBLE)"
	}

	var dateExpr string
	if colTypes["ZDATE"] == "TIMESTAMP" {
		dateExpr = "CAST(strftime(ZDATE + INTERVAL '978307200 seconds', '%Y-%m-%dT%H:%M:%SZ') AS VARCHAR)"
	} else {
		dateExpr = "CAST(strftime(CAST(to_timestamp(CAST(ZDATE AS DOUBLE) + 978307200) AS TIMESTAMP), '%Y-%m-%dT%H:%M:%SZ') AS VARCHAR)"
	}

	loadQuery := fmt.Sprintf(`
		CREATE TEMP TABLE apple_snapshot AS
		SELECT
			CAST(Z_PK AS BIGINT) AS zpk,
			CAST('%s/' || ZPATH AS VARCHAR) AS audio_path,
			%s AS title,
			%s AS created_at,
			%s AS duration_seconds,
			%s AS transcription,
			%s AS latitude,
			%s AS longitude,
			%s AS place_name,
			%s AS device,
			%s AS folder
		FROM apple.ZCLOUDRECORDING
	`, strings.ReplaceAll(recordingsDir, "'", "''"), titleCol, dateExpr, durationCol, transcriptionCol, latCol, lonCol, placeCol, deviceCol, folderCol)

	if _, err := db.Exec(loadQuery); err != nil {
		return fmt.Errorf("failed to snapshot Voice Memos rows: %w", err)
	}

	slog.Debug("loaded apple_snapshot from DB copy; releasing Apple attach")
	return nil
}

// snapshotAppleDB copies the SQLite main file plus -wal/-shm sidecars when present.
func snapshotAppleDB(path string) (string, func(), error) {
	src, err := os.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("Voice Memos DB not accessible — grant Full Disk Access to vmc: %w", err)
	}
	defer src.Close()

	tmp, err := os.CreateTemp("", "vmc_voicememos_*.db")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp copy: %w", err)
	}
	tmpPath := tmp.Name()

	cleanup := func() {
		os.Remove(tmpPath)
		os.Remove(tmpPath + "-wal")
		os.Remove(tmpPath + "-shm")
	}

	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		cleanup()
		return "", nil, fmt.Errorf("failed to copy Voice Memos DB: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("failed to close Voice Memos DB copy: %w", err)
	}

	for _, suffix := range []string{"-wal", "-shm"} {
		side := path + suffix
		if _, err := os.Stat(side); err != nil {
			continue
		}
		if err := copyFile(side, tmpPath+suffix); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("failed to copy Voice Memos DB sidecar %s: %w", suffix, err)
		}
	}

	return tmpPath, cleanup, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func attachApplePath(db *sql.DB, path string) error {
	attachQuery := fmt.Sprintf("ATTACH '%s' AS apple (TYPE sqlite, READ_ONLY);", strings.ReplaceAll(path, "'", "''"))

	for i := range 3 {
		if _, err := db.Exec(attachQuery); err == nil {
			if _, err := db.Exec("SELECT 1 FROM apple.ZCLOUDRECORDING LIMIT 1"); err == nil {
				return nil
			}
			db.Exec("DETACH apple;")
		}
		time.Sleep(time.Duration(i+1) * 500 * time.Millisecond)
	}

	return fmt.Errorf("failed to attach Voice Memos DB snapshot at %s", path)
}

// knownRow is the dedup projection of an already detected or published row.
type knownRow struct {
	ID        int64
	Device    sql.NullString
	CreatedAt sql.NullString
	Duration  sql.NullFloat64
}

const knownRowColumns = "CAST(recording_id AS BIGINT), CAST(device AS VARCHAR), CAST(created_at AS VARCHAR), CAST(duration_seconds AS DOUBLE)"

// queryKnownRows projects dedup columns from a parquet source, falling back
// to recording_id alone for files that predate the device/created_at columns.
func queryKnownRows(db *sql.DB, source string) ([]knownRow, error) {
	rows, err := db.Query(fmt.Sprintf("SELECT %s FROM %s", knownRowColumns, source))
	if err != nil {
		idRows, idErr := db.Query(fmt.Sprintf("SELECT CAST(recording_id AS BIGINT) FROM %s", source))
		if idErr != nil {
			return nil, err
		}
		defer idRows.Close()
		var out []knownRow
		for idRows.Next() {
			var k knownRow
			if err := idRows.Scan(&k.ID); err != nil {
				return nil, err
			}
			out = append(out, k)
		}
		return out, idRows.Err()
	}
	defer rows.Close()

	var out []knownRow
	for rows.Next() {
		var k knownRow
		if err := rows.Scan(&k.ID, &k.Device, &k.CreatedAt, &k.Duration); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func readLocalRows(db *sql.DB, pattern string) ([]knownRow, error) {
	return queryKnownRows(db, fmt.Sprintf("read_parquet('%s', union_by_name = true)", strings.ReplaceAll(pattern, "'", "''")))
}

// createKnownAppleTable records the Apple Z_PKs behind ids; DJI IDs are
// excluded because their slot number is an Apple Z_PK they do not own.
func createKnownAppleTable(db *sql.DB, ids []int64) error {
	if _, err := db.Exec("CREATE TEMP TABLE known_apple (zpk BIGINT)"); err != nil {
		return err
	}
	var zpks []int64
	for _, id := range ids {
		if !IsDJIID(id) {
			zpks = append(zpks, DecodeAppleZPK(id))
		}
	}
	for i := 0; i < len(zpks); i += 500 {
		end := min(i+500, len(zpks))
		var values []string
		for _, zpk := range zpks[i:end] {
			values = append(values, fmt.Sprintf("(%d)", zpk))
		}
		if _, err := db.Exec(fmt.Sprintf("INSERT INTO known_apple VALUES %s", strings.Join(values, ","))); err != nil {
			return err
		}
	}
	return nil
}

// fetchRemoteRows lists data/*.parquet on the Hub and returns their dedup
// rows, how many files could not be read, and the highest numbered shard.
func fetchRemoteRows(cfg *config.Config) ([]knownRow, int, int, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	apiURL := fmt.Sprintf("%s/api/datasets/%s/tree/main/data", cfg.HFBaseURL, cfg.HFRepo)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, 0, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.HFToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("HF API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, 0, 0, nil
	}
	if resp.StatusCode != 200 {
		return nil, 0, 0, fmt.Errorf("HF API returned %d", resp.StatusCode)
	}

	// Hub /tree/ returns "path"; /siblings returns "rfilename". Accept both.
	var files []struct {
		Type      string `json:"type"`
		Path      string `json:"path"`
		RfileName string `json:"rfilename"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, 0, 0, fmt.Errorf("failed to parse HF file listing: %w", err)
	}

	var all []knownRow
	failed := 0
	maxShard := 0
	for _, f := range files {
		if f.Type != "" && f.Type != "file" {
			continue
		}
		rel := f.Path
		if rel == "" {
			rel = f.RfileName
		}
		if rel == "" {
			continue
		}
		base := filepath.Base(rel)
		if !strings.HasSuffix(base, ".parquet") {
			continue
		}
		var shardNum int
		if _, err := fmt.Sscanf(base, "shard_%d.parquet", &shardNum); err == nil && shardNum > maxShard {
			maxShard = shardNum
		}
		// Tree paths are usually "data/shard_….parquet"; siblings may be bare names.
		resolvePath := rel
		if !strings.Contains(rel, "/") {
			resolvePath = "data/" + rel
		}
		fileURL := fmt.Sprintf("%s/datasets/%s/resolve/main/%s", cfg.HFBaseURL, cfg.HFRepo, resolvePath)
		rows, err := readRemoteRows(client, cfg.HFToken, fileURL)
		if err != nil {
			slog.Warn("failed to read remote parquet for dedup", "file", resolvePath, "error", err)
			failed++
			continue
		}
		all = append(all, rows...)
	}

	return all, failed, maxShard, nil
}

func readRemoteRows(client *http.Client, token, fileURL string) ([]knownRow, error) {
	req, err := http.NewRequest("GET", fileURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "vmc_dedup_*.parquet")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmp, resp.Body); err != nil {
		tmp.Close()
		return nil, err
	}
	tmp.Close()

	sqlDB, err := sql.Open("duckdb", "")
	if err != nil {
		return nil, err
	}
	defer sqlDB.Close()

	// Project only dedup columns — still downloads the file, but avoids scanning blobs in Go.
	return queryKnownRows(sqlDB, fmt.Sprintf("read_parquet('%s')", strings.ReplaceAll(tmpPath, "'", "''")))
}
