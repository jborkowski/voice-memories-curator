package process

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
	var firstErr error

	for _, shardPath := range matches {
		if strings.HasSuffix(shardPath, "_tmp.parquet") {
			continue
		}

		processedCount, skippedCount, err := processShard(db, shardPath)
		if err != nil {
			slog.Error("failed to process shard", "shard", shardPath, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("process %s: %w", shardPath, err)
			}
			continue
		}

		totalProcessed += processedCount
		if skippedCount > 0 {
			anySkipped = true
		}
	}

	if totalProcessed == 0 && !anySkipped && firstErr == nil {
		slog.Info("no unprocessed memos found")
	} else if totalProcessed > 0 {
		slog.Info("process phase complete", "total_processed", totalProcessed)
	}

	return firstErr
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

	type processedRow struct {
		flacPath   string
		origPath   string
		transcript string // empty means NULL
	}

	processed := 0
	skipped := 0
	rowData := make(map[int64]*processedRow)

	for _, p := range pending {
		if _, err := os.Stat(p.path); os.IsNotExist(err) {
			slog.Warn("source file not found, skipping", "audio_path", p.path, "recording_id", p.id)
			skipped++
			continue
		}

		tempFlacPath := filepath.Join(tempDir, fmt.Sprintf("%d.flac", p.id))
		if err := transcodeFLAC(p.path, tempFlacPath); err != nil {
			slog.Warn("ffmpeg transcode failed, skipping", "error", err, "recording_id", p.id)
			skipped++
			continue
		}

		origCopyPath := filepath.Join(tempDir, fmt.Sprintf("%d.m4a", p.id))
		var transcript string

		if isAppleAudio(p.path) {
			// Copy the original .m4a into tempDir so DuckDB can read_blob it
			origData, err := os.ReadFile(p.path)
			if err != nil {
				slog.Warn("failed to read original file, skipping audio_original", "error", err, "recording_id", p.id)
				origCopyPath = ""
			} else if err := os.WriteFile(origCopyPath, origData, 0644); err != nil {
				slog.Warn("failed to write original copy", "error", err, "recording_id", p.id)
				origCopyPath = ""
			}

			transcript, err = extractTranscript(p.path)
			if err != nil {
				slog.Debug("no transcript extracted", "recording_id", p.id, "error", err)
			}
		} else {
			// Consumers (lazy-notes) assume audio_original is .m4a, so a row
			// without it must not be published; leave audio NULL to retry.
			// Use ALAC (Apple Lossless Audio Codec) to preserve 100% bit-exact studio quality.
			if err := transcodeALAC(p.path, origCopyPath); err != nil {
				slog.Warn("ffmpeg ALAC transcode failed, skipping", "error", err, "recording_id", p.id, "audio_path", p.path)
				skipped++
				continue
			}
		}

		rowData[p.id] = &processedRow{
			flacPath:   tempFlacPath,
			origPath:   origCopyPath,
			transcript: transcript,
		}
		processed++
	}

	if len(rowData) == 0 {
		return 0, skipped, nil
	}

	tempShardPath := shardPath + ".tmp"

	var audioExpr strings.Builder
	audioExpr.WriteString("CASE\n")
	for id, rd := range rowData {
		audioExpr.WriteString(fmt.Sprintf("WHEN s.recording_id = %d THEN (SELECT content FROM read_blob('%s'))\n", id, strings.ReplaceAll(rd.flacPath, "'", "''")))
	}
	audioExpr.WriteString("ELSE s.audio END AS audio")

	origCol := "s.audio_original"
	{
		var b strings.Builder
		for id, rd := range rowData {
			if rd.origPath != "" {
				b.WriteString(fmt.Sprintf("WHEN s.recording_id = %d THEN (SELECT content FROM read_blob('%s'))\n", id, strings.ReplaceAll(rd.origPath, "'", "''")))
			}
		}
		if b.Len() > 0 {
			origCol = "CASE\n" + b.String() + "ELSE s.audio_original END"
		}
	}

	txCol := "s.transcription"
	{
		var b strings.Builder
		for id, rd := range rowData {
			if rd.transcript != "" {
				b.WriteString(fmt.Sprintf("WHEN s.recording_id = %d THEN '%s'\n", id, strings.ReplaceAll(rd.transcript, "'", "''")))
			}
		}
		if b.Len() > 0 {
			txCol = "CASE\n" + b.String() + "ELSE s.transcription END"
		}
	}

	copyQuery := fmt.Sprintf(`
		COPY (
			SELECT 
				s.recording_id,
				%s,
				%s AS audio_original,
				s.audio_path, s.title, s.created_at, s.duration_seconds,
				%s AS transcription, s.latitude, s.longitude,
				s.place_name, s.device, s.folder
			FROM '%s' s
		) TO '%s' (FORMAT PARQUET, ROW_GROUP_SIZE 1)
	`, audioExpr.String(), origCol, txCol, strings.ReplaceAll(shardPath, "'", "''"), strings.ReplaceAll(tempShardPath, "'", "''"))

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

// isAppleAudio reports whether path is a Voice Memos container that is
// already .m4a-compatible and may carry a tsrp transcript atom.
func isAppleAudio(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".m4a", ".qta":
		return true
	}
	return false
}

// transcodeFLAC writes the 16 kHz mono FLAC used for the audio column.
func transcodeFLAC(src, dst string) error {
	return runFFmpeg("-i", src, "-ac", "1", "-ar", "16000", "-f", "flac", "-y", dst)
}

// transcodeALAC writes an ALAC .m4a for audio_original from a non-Apple
// source (e.g. DJI WAV), keeping the source channels, sample rate, and bit depth losslessly.
func transcodeALAC(src, dst string) error {
	return runFFmpeg("-i", src, "-vn", "-c:a", "alac", "-movflags", "+faststart", "-f", "ipod", "-y", dst)
}

func runFFmpeg(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ffmpeg", append([]string{"-nostdin", "-loglevel", "error"}, args...)...)
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderrBuf.String()))
	}
	return nil
}

// extractTranscript reads an .m4a/.qta file and extracts the transcript
// from Apple's custom tsrp MPEG-4 atom. The atom contains UTF-8 JSON with
// an attributedString.runs array where string elements alternate with
// integer indices. Returns empty string and error if no tsrp atom is found.
func extractTranscript(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read file: %w", err)
	}

	marker := []byte("tsrp{")
	idx := bytes.Index(data, marker)
	if idx < 0 {
		return "", fmt.Errorf("no tsrp atom found")
	}

	// JSON starts at the '{' after "tsrp"
	jsonStart := idx + 4 // len("tsrp")

	// Find the matching closing brace by counting depth
	depth := 0
	jsonEnd := -1
	for i := jsonStart; i < len(data); i++ {
		switch data[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				jsonEnd = i + 1
				break
			}
		}
		if jsonEnd > 0 {
			break
		}
	}
	if jsonEnd < 0 {
		return "", fmt.Errorf("unterminated tsrp JSON")
	}

	var tsrp struct {
		AttributedString struct {
			Runs []json.RawMessage `json:"runs"`
		} `json:"attributedString"`
	}
	if err := json.Unmarshal(data[jsonStart:jsonEnd], &tsrp); err != nil {
		return "", fmt.Errorf("parse tsrp JSON: %w", err)
	}

	var sb strings.Builder
	for i, raw := range tsrp.AttributedString.Runs {
		if i%2 != 0 {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue
		}
		sb.WriteString(s)
	}

	return sb.String(), nil
}
