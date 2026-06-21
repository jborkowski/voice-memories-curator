package upload

import (
	"bytes"
	"database/sql"
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
	if cfg.HFToken == "" {
		return fmt.Errorf("HFToken is required for upload. Set HF_TOKEN environment variable or hf_token in config.toml")
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

	var readyShards []string

	// 1. Find ready shards (all rows have audio IS NOT NULL)
	for _, shardPath := range matches {
		if strings.HasSuffix(shardPath, "_tmp.parquet") {
			continue
		}

		ready, err := isShardReady(db, shardPath)
		if err != nil {
			slog.Error("failed to check shard readiness", "shard", shardPath, "error", err)
			continue
		}

		if ready {
			readyShards = append(readyShards, shardPath)
		}
	}

	if len(readyShards) == 0 {
		slog.Info("0 shards ready")
		return nil
	}

	// 2. Connectivity check
	if err := checkConnectivity(cfg); err != nil {
		slog.Info(fmt.Sprintf("offline, %d shards ready", len(readyShards)))
		return nil
	}

	slog.Info(fmt.Sprintf("%d shards ready for upload", len(readyShards)))

	// 3. Upload ready shards
	for _, shardPath := range readyShards {
		if err := uploadShard(db, cfg, shardPath); err != nil {
			slog.Error("failed to upload shard", "shard", shardPath, "error", err)
			// Partial failure: shard stays local for retry
			continue
		}

		// 5. Delete local shard on success (unless keep_uploaded_shards)
		if !cfg.KeepUploadedShards {
			if err := os.Remove(shardPath); err != nil {
				slog.Error("failed to delete uploaded shard", "shard", shardPath, "error", err)
			} else {
				slog.Info("deleted local shard after successful upload", "shard", shardPath)
			}
		}
	}

	return nil
}

func checkConnectivity(cfg *config.Config) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Head(cfg.HFBaseURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	return nil
}

func isShardReady(db *sql.DB, shardPath string) (bool, error) {
	query := fmt.Sprintf(`SELECT COUNT(*) FROM '%s' WHERE audio IS NULL`, strings.ReplaceAll(shardPath, "'", "''"))
	var nullCount int
	if err := db.QueryRow(query).Scan(&nullCount); err != nil {
		return false, err
	}
	return nullCount == 0, nil
}

func uploadShard(db *sql.DB, cfg *config.Config, shardPath string) error {
	// 3. Column-select for HF schema — drop internal fields like audio_path
	tempFile, err := os.CreateTemp("", "vmc_upload_*.parquet")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tempPath := tempFile.Name()
	tempFile.Close()
	defer os.Remove(tempPath)

	copyQuery := fmt.Sprintf(`
		COPY (
			SELECT 
				recording_id,
				audio,
				title,
				created_at,
				duration_seconds,
				transcription,
				latitude,
				longitude,
				place_name,
				device,
				folder
			FROM '%s'
		) TO '%s' (FORMAT PARQUET)
	`, strings.ReplaceAll(shardPath, "'", "''"), strings.ReplaceAll(tempPath, "'", "''"))

	if _, err := db.Exec(copyQuery); err != nil {
		return fmt.Errorf("failed to extract HF schema: %w", err)
	}

	// 4. Push as data/shard_NNNN.parquet to the HF dataset repo
	fileName := filepath.Base(shardPath)
	pathInRepo := fmt.Sprintf("data/%s", fileName)

	fileData, err := os.ReadFile(tempPath)
	if err != nil {
		return fmt.Errorf("failed to read temp parquet file: %w", err)
	}

	url := fmt.Sprintf("%s/api/datasets/%s/upload/main/%s", cfg.HFBaseURL, cfg.HFRepo, pathInRepo)
	
	req, err := http.NewRequest("POST", url, bytes.NewReader(fileData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+cfg.HFToken)
	req.Header.Set("Content-Type", "application/octet-stream")

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// Try creating the repo first
		if err := createRepo(cfg); err != nil {
			return fmt.Errorf("repo not found and failed to create: %w", err)
		}
		// Retry upload
		req, _ = http.NewRequest("POST", url, bytes.NewReader(fileData))
		req.Header.Set("Authorization", "Bearer "+cfg.HFToken)
		req.Header.Set("Content-Type", "application/octet-stream")
		resp2, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("HTTP request failed on retry: %w", err)
		}
		defer resp2.Body.Close()
		if resp2.StatusCode < 200 || resp2.StatusCode >= 300 {
			body, _ := io.ReadAll(resp2.Body)
			return fmt.Errorf("upload failed on retry with status %d: %s", resp2.StatusCode, string(body))
		}
	} else if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	slog.Info("successfully uploaded shard to HF", "shard", shardPath, "repo", cfg.HFRepo, "path", pathInRepo)
	return nil
}

func createRepo(cfg *config.Config) error {
	url := fmt.Sprintf("%s/api/repos/create", cfg.HFBaseURL)
	
	// The HFRepo should be "username/repo_name". We need the repo_name part.
	parts := strings.Split(cfg.HFRepo, "/")
	repoName := cfg.HFRepo
	if len(parts) == 2 {
		repoName = parts[1]
	}

	payload := fmt.Sprintf(`{"type": "dataset", "name": "%s", "private": %v}`, repoName, cfg.HFPrivate)
	
	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	if err != nil {
		return err
	}
	
	req.Header.Set("Authorization", "Bearer "+cfg.HFToken)
	req.Header.Set("Content-Type", "application/json")
	
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	
	slog.Info("created new HF dataset repo", "repo", cfg.HFRepo)
	return nil
}
