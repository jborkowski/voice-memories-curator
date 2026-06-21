package upload

import (
	"bytes"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jborkowski/vmc/internal/config"
)

func Run(db *sql.DB, cfg *config.Config) error {
	if cfg.HFToken == "" {
		return fmt.Errorf("HFToken is required for upload. Set HF_TOKEN environment variable or hf_token in config.toml")
	}

	if _, err := exec.LookPath("git-xet"); err != nil {
		return fmt.Errorf("git-xet not found — install it with: brew install git-xet && git xet install")
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

	if err := checkConnectivity(cfg); err != nil {
		slog.Info(fmt.Sprintf("offline, %d shards ready", len(readyShards)))
		return nil
	}

	slog.Info(fmt.Sprintf("%d shards ready for upload", len(readyShards)))

	for _, shardPath := range readyShards {
		if err := uploadShard(db, cfg, shardPath); err != nil {
			slog.Error("failed to upload shard", "shard", shardPath, "error", err)
			continue
		}

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
	// Export shard with HF-destined columns only (drop audio_path)
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
				{'bytes': audio, 'path': 'recording_' || CAST(recording_id AS VARCHAR) || '.flac'} AS audio,
				{'bytes': audio_original, 'path': 'recording_' || CAST(recording_id AS VARCHAR) || '.m4a'} AS audio_original,
				title, created_at, duration_seconds,
				transcription, latitude, longitude, place_name, device, folder
			FROM '%s'
		) TO '%s' (FORMAT PARQUET, ROW_GROUP_SIZE 1)
	`, strings.ReplaceAll(shardPath, "'", "''"), strings.ReplaceAll(tempPath, "'", "''"))

	if _, err := db.Exec(copyQuery); err != nil {
		return fmt.Errorf("failed to extract HF schema: %w", err)
	}

	// Clone the dataset repo into a temp dir (shallow)
	repoDir, err := os.MkdirTemp("", "vmc_repo_*")
	if err != nil {
		return fmt.Errorf("failed to create temp repo dir: %w", err)
	}
	defer os.RemoveAll(repoDir)

	repoURL := fmt.Sprintf("https://x-access-token:%s@huggingface.co/datasets/%s", cfg.HFToken, cfg.HFRepo)

	if err := gitCmd(repoDir, "clone", "--depth=1", repoURL, "."); err != nil {
		// Repo might not exist yet — try creating it
		if createErr := createRepo(cfg); createErr != nil {
			return fmt.Errorf("clone failed and repo creation failed: clone=%w, create=%v", err, createErr)
		}
		if err := gitCmd(repoDir, "clone", "--depth=1", repoURL, "."); err != nil {
			return fmt.Errorf("clone failed after repo creation: %w", err)
		}
	}

	// Ensure data/ directory exists
	dataDir := filepath.Join(repoDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}

	// Ensure dataset card exists with audio feature metadata
	readmePath := filepath.Join(repoDir, "README.md")
	if _, err := os.Stat(readmePath); os.IsNotExist(err) {
		card := `---
configs:
  - config_name: default
    data_files:
      - split: train
        path: "data/*.parquet"
dataset_info:
  features:
    - name: recording_id
      dtype: int64
    - name: audio
      dtype: audio
    - name: audio_original
      dtype: audio
    - name: title
      dtype: string
    - name: created_at
      dtype: string
    - name: duration_seconds
      dtype: float64
    - name: transcription
      dtype: string
    - name: latitude
      dtype: float64
    - name: longitude
      dtype: float64
    - name: place_name
      dtype: string
    - name: device
      dtype: string
    - name: folder
      dtype: string
license: other
---
# Voice Memories

Private dataset of Apple Voice Memos, transcoded to FLAC 16kHz mono.
`
		os.WriteFile(readmePath, []byte(card), 0644)
		gitCmd(repoDir, "add", "README.md")
	}

	// Copy the exported parquet into data/
	fileName := filepath.Base(shardPath)
	destPath := filepath.Join(dataDir, fileName)
	data, err := os.ReadFile(tempPath)
	if err != nil {
		return fmt.Errorf("failed to read exported parquet: %w", err)
	}
	if err := os.WriteFile(destPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write parquet to repo: %w", err)
	}

	// git add + commit + push
	if err := gitCmd(repoDir, "add", "data/"+fileName); err != nil {
		return fmt.Errorf("git add failed: %w", err)
	}

	if err := gitCmd(repoDir, "add", "-A"); err != nil {
		// non-fatal, README might not have been created
	}

	if err := gitCmd(repoDir, "commit", "-m", fmt.Sprintf("Upload %s", fileName)); err != nil {
		// "nothing to commit" means file already exists with same content — success
		if strings.Contains(err.Error(), "nothing to commit") || strings.Contains(err.Error(), "no changes added") {
			slog.Info("shard already exists on HF with same content", "shard", shardPath)
			return nil
		}
		return fmt.Errorf("git commit failed: %w", err)
	}

	if err := gitCmd(repoDir, "push"); err != nil {
		// Retry with pull --rebase in case remote has new commits
		if pullErr := gitCmd(repoDir, "pull", "--rebase"); pullErr == nil {
			if retryErr := gitCmd(repoDir, "push"); retryErr == nil {
				slog.Info("successfully uploaded shard to HF", "shard", shardPath, "repo", cfg.HFRepo, "path", "data/"+fileName)
				return nil
			} else {
				return fmt.Errorf("git push failed after rebase: %w", retryErr)
			}
		}
		return fmt.Errorf("git push failed: %w", err)
	}

	slog.Info("successfully uploaded shard to HF", "shard", shardPath, "repo", cfg.HFRepo, "path", "data/"+fileName)
	return nil
}

func gitCmd(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %s", err, output.String())
	}
	return nil
}

func createRepo(cfg *config.Config) error {
	url := fmt.Sprintf("%s/api/repos/create", cfg.HFBaseURL)

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
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	slog.Info("created new HF dataset repo", "repo", cfg.HFRepo)
	return nil
}
