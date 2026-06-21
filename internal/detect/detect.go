package detect

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jborkowski/vmc/internal/config"
)

const debugLogPath = "/Users/jonatan/sources/voice-momories-curator/.cursor/debug-a0a063.log"

func debugLog(hypothesisID, location, message string, data map[string]interface{}) {
	entry := map[string]interface{}{
		"sessionId":    "a0a063",
		"hypothesisId": hypothesisID,
		"location":     location,
		"message":      message,
		"data":         data,
		"timestamp":    time.Now().UnixMilli(),
	}
	b, _ := json.Marshal(entry)
	f, err := os.OpenFile(debugLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	f.Write(append(b, '\n'))
	f.Close()
}

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
		return fmt.Errorf("cannot open Voice Memos database — is Full Disk Access enabled for this terminal? Path: %s", appleDBPath)
	}

	shardDir := cfg.ShardDir
	if strings.HasPrefix(shardDir, "~/") {
		shardDir = filepath.Join(homeDir, shardDir[2:])
	}
	if err := os.MkdirAll(shardDir, 0755); err != nil {
		return fmt.Errorf("failed to create shard directory: %w", err)
	}

	attachQuery := fmt.Sprintf("ATTACH '%s' AS apple (TYPE sqlite, READ_ONLY);", strings.ReplaceAll(appleDBPath, "'", "''"))
	// #region agent log
	debugLog("C", "detect.go:attach", "attempting sqlite ATTACH", map[string]interface{}{"path": appleDBPath})
	// #endregion
	if _, err := db.Exec(attachQuery); err != nil {
		// #region agent log
		debugLog("C", "detect.go:attach_fail", "ATTACH failed", map[string]interface{}{"error": err.Error()})
		// #endregion
		return fmt.Errorf("failed to attach Voice Memos database: %w", err)
	}
	// #region agent log
	debugLog("C", "detect.go:attach_ok", "ATTACH succeeded", nil)
	// #endregion
	defer db.Exec("DETACH apple;")
	defer db.Exec("DROP TABLE IF EXISTS uploaded")
	defer db.Exec("DROP TABLE IF EXISTS local_pending")

	dedupMode := "local+hf"
	if cfg.HFToken != "" {
		// #region agent log
		debugLog("A", "detect.go:secret", "creating HF secret", map[string]interface{}{"token_len": len(cfg.HFToken)})
		// #endregion
		if _, err := db.Exec(fmt.Sprintf("CREATE OR REPLACE SECRET hf_secret (TYPE HUGGINGFACE, TOKEN '%s');", cfg.HFToken)); err != nil {
			// #region agent log
			debugLog("A", "detect.go:secret_fail", "HF secret creation failed", map[string]interface{}{"error": err.Error()})
			// #endregion
			slog.Warn("Failed to set HF token", "error", err)
		} else {
			// #region agent log
			debugLog("A", "detect.go:secret_ok", "HF secret created successfully", nil)
			// #endregion
		}
	} else {
		// #region agent log
		debugLog("B", "detect.go:no_token", "HFToken is empty", nil)
		// #endregion
	}

	db.Exec("SET http_max_scan_size = 0")

	// #region agent log
	debugLog("E", "detect.go:hf_query", "about to run hf:// query", map[string]interface{}{"repo": cfg.HFRepo})
	// #endregion
	hfQuery := fmt.Sprintf("SELECT recording_id FROM 'hf://datasets/%s/data/*.parquet'", cfg.HFRepo)
	if _, err := db.Exec(fmt.Sprintf("CREATE TEMP TABLE uploaded AS %s", hfQuery)); err != nil {
		// #region agent log
		debugLog("E", "detect.go:hf_query_fail", "hf:// query failed", map[string]interface{}{"error": err.Error()})
		// #endregion
		slog.Warn("HF query failed, falling back to local-only dedup", "error", err)
		dedupMode = "local-only"
		db.Exec("CREATE TEMP TABLE uploaded (recording_id BIGINT)")
	} else {
		// #region agent log
		debugLog("E", "detect.go:hf_query_ok", "hf:// query succeeded", nil)
		// #endregion
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

	// Count how many new memos we have
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

	// Write shards in batches of shardMaxRows
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
