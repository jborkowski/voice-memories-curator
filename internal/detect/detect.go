package detect

import (
	"database/sql"
	"encoding/json"
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
	if _, err := os.Stat(appleDBPath); os.IsNotExist(err) || os.IsPermission(err) {
		return fmt.Errorf("cannot open Voice Memos database — grant Full Disk Access to vmc. Path: %s", appleDBPath)
	}

	shardDir := cfg.ShardDir
	if strings.HasPrefix(shardDir, "~/") {
		shardDir = filepath.Join(homeDir, shardDir[2:])
	}
	if err := os.MkdirAll(shardDir, 0755); err != nil {
		return fmt.Errorf("failed to create shard directory: %w", err)
	}

	defer db.Exec("DROP TABLE IF EXISTS uploaded")
	defer db.Exec("DROP TABLE IF EXISTS local_pending")
	defer db.Exec("DROP TABLE IF EXISTS apple_snapshot")

	// Build dedup tables BEFORE touching Apple's DB so network I/O never
	// overlaps with a live ATTACH on CloudRecordings.db.
	dedupMode := "local+hf"
	if cfg.HFToken != "" && cfg.HFRepo != "" {
		ids, err := fetchRemoteRecordingIDs(cfg)
		if err != nil {
			slog.Warn("HF remote dedup failed, falling back to local-only", "error", err)
			dedupMode = "local-only"
			db.Exec("CREATE TEMP TABLE uploaded (recording_id BIGINT)")
		} else {
			if err := createUploadedTable(db, ids); err != nil {
				slog.Warn("failed to populate uploaded table", "error", err)
				db.Exec("CREATE TEMP TABLE uploaded (recording_id BIGINT)")
				dedupMode = "local-only"
			}
		}
	} else {
		dedupMode = "local-only"
		db.Exec("CREATE TEMP TABLE uploaded (recording_id BIGINT)")
	}

	localShardsPattern := filepath.Join(shardDir, "*.parquet")
	matches, _ := filepath.Glob(localShardsPattern)
	if len(matches) > 0 {
		if _, err := db.Exec(fmt.Sprintf("CREATE TEMP TABLE local_pending AS SELECT recording_id FROM '%s'", localShardsPattern)); err != nil {
			slog.Warn("failed to read local shards for dedup", "error", err)
			db.Exec("CREATE TEMP TABLE local_pending (recording_id BIGINT)")
		}
	} else {
		db.Exec("CREATE TEMP TABLE local_pending (recording_id BIGINT)")
	}

	recordingsDir := filepath.Dir(appleDBPath)

	// Snapshot Apple DB (main + WAL/SHM), attach briefly, copy rows into DuckDB, DETACH.
	if err := snapshotAndLoadApple(db, appleDBPath, recordingsDir); err != nil {
		return err
	}

	maxShard := 0
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
	countQuery := `SELECT COUNT(*) FROM apple_snapshot
		WHERE recording_id NOT IN (SELECT recording_id FROM uploaded)
		  AND recording_id NOT IN (SELECT recording_id FROM local_pending)`
	if err := db.QueryRow(countQuery).Scan(&totalNew); err != nil {
		return fmt.Errorf("failed to count new memos: %w", err)
	}

	if totalNew == 0 {
		slog.Info("no new memos detected")
		return nil
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
				FROM apple_snapshot
				WHERE recording_id NOT IN (SELECT recording_id FROM uploaded)
				  AND recording_id NOT IN (SELECT recording_id FROM local_pending)
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
		slog.Int("shard_count", maxShard),
		slog.String("dedup_mode", dedupMode),
	)

	return nil
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
			CAST(Z_PK AS BIGINT) AS recording_id,
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

func fetchRemoteRecordingIDs(cfg *config.Config) ([]int64, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	apiURL := fmt.Sprintf("%s/api/datasets/%s/tree/main/data", cfg.HFBaseURL, cfg.HFRepo)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.HFToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HF API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return nil, nil
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HF API returned %d", resp.StatusCode)
	}

	// Hub /tree/ returns "path"; /siblings returns "rfilename". Accept both.
	var files []struct {
		Type      string `json:"type"`
		Path      string `json:"path"`
		RfileName string `json:"rfilename"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("failed to parse HF file listing: %w", err)
	}

	var allIDs []int64
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
		// Tree paths are usually "data/shard_….parquet"; siblings may be bare names.
		resolvePath := rel
		if !strings.Contains(rel, "/") {
			resolvePath = "data/" + rel
		}
		fileURL := fmt.Sprintf("%s/datasets/%s/resolve/main/%s", cfg.HFBaseURL, cfg.HFRepo, resolvePath)
		ids, err := readRecordingIDs(client, cfg.HFToken, fileURL)
		if err != nil {
			slog.Warn("failed to read remote parquet for dedup", "file", resolvePath, "error", err)
			continue
		}
		allIDs = append(allIDs, ids...)
	}

	return allIDs, nil
}

func readRecordingIDs(client *http.Client, token, fileURL string) ([]int64, error) {
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

	// Project only recording_id — still downloads the file, but avoids scanning blobs in Go.
	rows, err := sqlDB.Query(fmt.Sprintf("SELECT recording_id FROM read_parquet('%s')", strings.ReplaceAll(tmpPath, "'", "''")))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func createUploadedTable(db *sql.DB, ids []int64) error {
	if _, err := db.Exec("CREATE TEMP TABLE uploaded (recording_id BIGINT)"); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	for i := 0; i < len(ids); i += 500 {
		end := min(i+500, len(ids))
		var values []string
		for _, id := range ids[i:end] {
			values = append(values, fmt.Sprintf("(%d)", id))
		}
		if _, err := db.Exec(fmt.Sprintf("INSERT INTO uploaded VALUES %s", strings.Join(values, ","))); err != nil {
			return err
		}
	}
	return nil
}
