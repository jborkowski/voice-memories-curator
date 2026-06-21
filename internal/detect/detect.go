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

	if err := attachAppleDB(db, appleDBPath); err != nil {
		return err
	}
	defer db.Exec("DETACH apple;")
	defer db.Exec("DROP TABLE IF EXISTS uploaded")
	defer db.Exec("DROP TABLE IF EXISTS local_pending")

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

	recordingsDir := filepath.Dir(appleDBPath)

	var dateExpr string
	if colTypes["ZDATE"] == "TIMESTAMP" {
		dateExpr = "CAST(strftime(ZDATE + INTERVAL '978307200 seconds', '%Y-%m-%dT%H:%M:%SZ') AS VARCHAR)"
	} else {
		dateExpr = "CAST(strftime(CAST(to_timestamp(CAST(ZDATE AS DOUBLE) + 978307200) AS TIMESTAMP), '%Y-%m-%dT%H:%M:%SZ') AS VARCHAR)"
	}

	shardMaxRows := cfg.ShardMaxRows
	if shardMaxRows <= 0 {
		shardMaxRows = 10
	}

	var totalNew int64
	countQuery := `SELECT COUNT(*) FROM apple.ZCLOUDRECORDING
		WHERE Z_PK NOT IN (SELECT recording_id FROM uploaded)
		  AND Z_PK NOT IN (SELECT recording_id FROM local_pending)`
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
					CAST(Z_PK AS BIGINT) AS recording_id,
					CAST(NULL AS BLOB) AS audio,
					CAST(NULL AS BLOB) AS audio_original,
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
				WHERE Z_PK NOT IN (SELECT recording_id FROM uploaded)
				  AND Z_PK NOT IN (SELECT recording_id FROM local_pending)
				ORDER BY Z_PK
				LIMIT %d OFFSET %d
			) TO '%s' (FORMAT PARQUET, ROW_GROUP_SIZE 1)
		`, strings.ReplaceAll(recordingsDir, "'", "''"), titleCol, dateExpr, durationCol, transcriptionCol, latCol, lonCol, placeCol, deviceCol, folderCol, shardMaxRows, offset, tempShardPath)

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

// attachAppleDB tries direct ATTACH first, falls back to copying the DB file
// (handles iCloud/VoiceMemos WAL locks and macOS TCC restrictions).
func attachAppleDB(db *sql.DB, path string) error {
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

	// Direct access failed — copy DB to temp and attach copy
	src, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("Voice Memos DB not accessible — grant Full Disk Access to vmc: %w", err)
	}
	defer src.Close()

	tmp, err := os.CreateTemp("", "vmc_voicememos_*.db")
	if err != nil {
		return fmt.Errorf("failed to create temp copy: %w", err)
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("failed to copy Voice Memos DB: %w", err)
	}
	tmp.Close()

	attachCopy := fmt.Sprintf("ATTACH '%s' AS apple (TYPE sqlite, READ_ONLY);", strings.ReplaceAll(tmp.Name(), "'", "''"))
	if _, err := db.Exec(attachCopy); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("failed to attach copied Voice Memos DB: %w", err)
	}
	// tmp file cleaned up by caller via defer os.Remove after DETACH
	return nil
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

	var files []struct {
		RfileName string `json:"rfilename"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, fmt.Errorf("failed to parse HF file listing: %w", err)
	}

	var allIDs []int64
	for _, f := range files {
		if !strings.HasSuffix(f.RfileName, ".parquet") {
			continue
		}
		fileURL := fmt.Sprintf("%s/datasets/%s/resolve/main/data/%s", cfg.HFBaseURL, cfg.HFRepo, f.RfileName)
		ids, err := readRecordingIDs(client, cfg.HFToken, fileURL)
		if err != nil {
			slog.Warn("failed to read remote parquet for dedup", "file", f.RfileName, "error", err)
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

	rows, err := sqlDB.Query(fmt.Sprintf("SELECT recording_id FROM '%s'", strings.ReplaceAll(tmpPath, "'", "''")))
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
