package process

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jborkowski/vmc/internal/config"
)

func Run(db *sql.DB, cfg *config.Config) error {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return fmt.Errorf("ffmpeg not found — install it with: brew install ffmpeg")
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	shardDir := cfg.ShardDir
	if strings.HasPrefix(shardDir, "~/") {
		shardDir = filepath.Join(homeDir, shardDir[2:])
	}

	localShardsPattern := filepath.Join(shardDir, "*.parquet")
	matches, err := filepath.Glob(localShardsPattern)
	if err != nil {
		return fmt.Errorf("failed to glob shards: %w", err)
	}

	if len(matches) == 0 {
		slog.Info("no shards found")
		return nil
	}

	var totalProcessed int
	var anySkipped bool

	for _, shardPath := range matches {
		if strings.HasSuffix(shardPath, "_tmp.parquet") {
			continue
		}

		processedCount, skippedCount, err := processShard(db, shardPath)
		if err != nil {
			slog.Error("failed to process shard", "shard", shardPath, "error", err)
			continue
		}

		totalProcessed += processedCount
		if skippedCount > 0 {
			anySkipped = true
		}
	}

	if totalProcessed == 0 && !anySkipped {
		slog.Info("no unprocessed memos found")
	} else if totalProcessed > 0 {
		slog.Info("process phase complete", "total_processed", totalProcessed)
	}

	return nil
}

func processShard(db *sql.DB, shardPath string) (int, int, error) {
	// Query rows with audio IS NULL AND audio_path IS NOT NULL
	query := fmt.Sprintf(`SELECT recording_id, audio_path FROM '%s' WHERE audio IS NULL AND audio_path IS NOT NULL`, strings.ReplaceAll(shardPath, "'", "''"))
	rows, err := db.Query(query)
	if err != nil {
		return 0, 0, fmt.Errorf("query shard failed: %w", err)
	}
	defer rows.Close()

	type pendingRow struct {
		id   int64
		path string
	}
	var pending []pendingRow
	for rows.Next() {
		var r pendingRow
		if err := rows.Scan(&r.id, &r.path); err != nil {
			return 0, 0, fmt.Errorf("scan failed: %w", err)
		}
		pending = append(pending, r)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("rows error: %w", err)
	}

	if len(pending) == 0 {
		return 0, 0, nil
	}

	tempDir, err := os.MkdirTemp("", "vmc_process_*")
	if err != nil {
		return 0, 0, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	processed := 0
	skipped := 0
	flacPaths := make(map[int64]string)

	for _, p := range pending {
		if _, err := os.Stat(p.path); os.IsNotExist(err) {
			slog.Warn("source file not found, skipping", "audio_path", p.path, "recording_id", p.id)
			skipped++
			continue
		}

		tempFlacPath := filepath.Join(tempDir, fmt.Sprintf("%d.flac", p.id))

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)

		cmd := exec.CommandContext(ctx, "ffmpeg",
			"-i", p.path,
			"-ac", "1",
			"-ar", "16000",
			"-f", "flac",
			"-y",
			tempFlacPath,
		)
		var stderrBuf bytes.Buffer
		cmd.Stderr = &stderrBuf

		if err := cmd.Run(); err != nil {
			slog.Warn("ffmpeg transcode failed, skipping", "error", err, "recording_id", p.id, "stderr", stderrBuf.String())
			skipped++
			cancel()
			continue
		}
		cancel()

		flacPaths[p.id] = tempFlacPath
		processed++
	}

	if len(flacPaths) == 0 {
		return 0, skipped, nil
	}

	// Write new shard
	tempShardPath := shardPath + ".tmp"

	var audioExpr strings.Builder
	audioExpr.WriteString("CASE\n")
	for id, flacPath := range flacPaths {
		audioExpr.WriteString(fmt.Sprintf("WHEN s.recording_id = %d THEN read_blob('%s')\n", id, strings.ReplaceAll(flacPath, "'", "''")))
	}
	audioExpr.WriteString("ELSE s.audio END AS audio")

	copyQuery := fmt.Sprintf(`
		COPY (
			SELECT 
				s.recording_id,
				%s,
				s.audio_path, s.title, s.created_at, s.duration_seconds,
				s.transcription, s.latitude, s.longitude,
				s.place_name, s.device, s.folder
			FROM '%s' s
		) TO '%s' (FORMAT PARQUET)
	`, audioExpr.String(), strings.ReplaceAll(shardPath, "'", "''"), strings.ReplaceAll(tempShardPath, "'", "''"))

	if _, err := db.Exec(copyQuery); err != nil {
		os.Remove(tempShardPath)
		return 0, 0, fmt.Errorf("failed to rewrite shard: %w", err)
	}

	if err := os.Rename(tempShardPath, shardPath); err != nil {
		return 0, 0, fmt.Errorf("failed to replace shard: %w", err)
	}

	slog.Info("processed shard", "shard", shardPath, "processed", processed, "skipped", skipped)

	return processed, skipped, nil
}
