package detect

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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
	if _, err := db.Exec(attachQuery); err != nil {
		return fmt.Errorf("failed to attach Voice Memos database: %w", err)
	}
	defer db.Exec("DETACH apple;")
	defer db.Exec("DROP TABLE IF EXISTS uploaded")
	defer db.Exec("DROP TABLE IF EXISTS local_pending")

	dedupMode := "local+hf"
	if cfg.HFToken != "" {
		if _, err := db.Exec(fmt.Sprintf("CREATE SECRET hf_secret (TYPE HUGGINGFACE, TOKEN '%s');", cfg.HFToken)); err != nil {
			slog.Warn("Failed to set HF token", "error", err)
		}
	}

	hfQuery := fmt.Sprintf("SELECT recording_id FROM 'hf://datasets/%s/data/*.parquet'", cfg.HFRepo)
	if _, err := db.Exec(fmt.Sprintf("CREATE TEMP TABLE uploaded AS %s", hfQuery)); err != nil {
		slog.Warn("HF query failed, falling back to local-only dedup", "error", err)
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
	nextShard := maxShard + 1
	tempShardPath := filepath.Join(shardDir, fmt.Sprintf("shard_%04d_tmp.parquet", nextShard))
	finalShardPath := filepath.Join(shardDir, fmt.Sprintf("shard_%04d.parquet", nextShard))

	recordingsDir := filepath.Dir(appleDBPath)

	// Build timestamp expression based on ZDATE column type
	var dateExpr string
	if colTypes["ZDATE"] == "TIMESTAMP" {
		dateExpr = "CAST(strftime(ZDATE + INTERVAL '978307200 seconds', '%Y-%m-%dT%H:%M:%SZ') AS VARCHAR)"
	} else {
		dateExpr = "CAST(strftime(CAST(to_timestamp(CAST(ZDATE AS DOUBLE) + 978307200) AS TIMESTAMP), '%Y-%m-%dT%H:%M:%SZ') AS VARCHAR)"
	}

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
		) TO '%s' (FORMAT PARQUET)
	`, strings.ReplaceAll(recordingsDir, "'", "''"), titleCol, dateExpr, durationCol, transcriptionCol, latCol, lonCol, placeCol, deviceCol, folderCol, tempShardPath)

	var rowsWritten int64
	if err := db.QueryRow(copyQuery).Scan(&rowsWritten); err != nil {
		return fmt.Errorf("failed to write shard: %w", err)
	}

	if rowsWritten == 0 {
		slog.Info("no new memos detected")
		os.Remove(tempShardPath)
		return nil
	}

	if err := os.Rename(tempShardPath, finalShardPath); err != nil {
		return fmt.Errorf("failed to rename temp shard: %w", err)
	}

	slog.Info("detect phase complete",
		slog.Int64("memos_found", rowsWritten),
		slog.String("shard", finalShardPath),
		slog.String("dedup_mode", dedupMode),
	)

	return nil
}
