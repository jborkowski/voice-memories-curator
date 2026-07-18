package upload

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jborkowski/vmc/internal/config"
)

// Run uploads all ready shards in a single git clone/commit/push.
// When force is false, respects upload_interval (default weekly).
func Run(db *sql.DB, cfg *config.Config) error {
	return RunWithOptions(db, cfg, false)
}

func RunWithOptions(db *sql.DB, cfg *config.Config, force bool) error {
	if cfg.HFToken == "" {
		return fmt.Errorf("HFToken is required for upload. Set HF_TOKEN environment variable or hf_token in config.toml")
	}

	if _, err := exec.LookPath("git-xet"); err != nil {
		return fmt.Errorf("git-xet not found — install it with: brew install git-xet && git xet install")
	}

	ok, err := ShouldUpload(cfg, force)
	if err != nil {
		return fmt.Errorf("upload cadence check failed: %w", err)
	}
	if !ok {
		slog.Info("skipping upload — upload_interval has not elapsed",
			"upload_interval_s", effectiveUploadInterval(cfg))
		return nil
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
	var checkErrs []error
	for _, shardPath := range matches {
		if strings.HasSuffix(shardPath, "_tmp.parquet") {
			continue
		}
		ready, err := isShardReady(db, shardPath)
		if err != nil {
			slog.Error("failed to check shard readiness", "shard", shardPath, "error", err)
			checkErrs = append(checkErrs, err)
			continue
		}
		if ready {
			readyShards = append(readyShards, shardPath)
		}
	}

	if len(readyShards) == 0 {
		if len(checkErrs) > 0 {
			return fmt.Errorf("no ready shards; readiness checks failed: %v", checkErrs[0])
		}
		slog.Info("0 shards ready")
		return nil
	}

	if err := checkConnectivity(cfg); err != nil {
		slog.Info(fmt.Sprintf("offline, %d shards ready", len(readyShards)))
		return nil
	}

	slog.Info(fmt.Sprintf("%d shards ready for upload", len(readyShards)))

	if err := uploadShardsBatch(db, cfg, readyShards); err != nil {
		return err
	}

	if !cfg.KeepUploadedShards {
		for _, shardPath := range readyShards {
			if err := os.Remove(shardPath); err != nil {
				slog.Error("failed to delete uploaded shard", "shard", shardPath, "error", err)
			} else {
				slog.Info("deleted local shard after successful upload", "shard", shardPath)
			}
		}
	}

	if err := MarkUploaded(cfg); err != nil {
		slog.Warn("failed to record last upload time", "error", err)
	}

	return nil
}

func effectiveUploadInterval(cfg *config.Config) int {
	if cfg.UploadInterval <= 0 {
		return defaultUploadInterval
	}
	return cfg.UploadInterval
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

func uploadShardsBatch(db *sql.DB, cfg *config.Config, shardPaths []string) error {
	exportDir, err := os.MkdirTemp("", "vmc_export_*")
	if err != nil {
		return fmt.Errorf("failed to create export dir: %w", err)
	}
	defer os.RemoveAll(exportDir)

	var exported []string
	for _, shardPath := range shardPaths {
		dest := filepath.Join(exportDir, filepath.Base(shardPath))
		if err := exportHFParquet(db, shardPath, dest); err != nil {
			return fmt.Errorf("export %s: %w", shardPath, err)
		}
		exported = append(exported, dest)
	}

	// Best-effort Viewer metadata rewrite before push.
	if fixedDir, err := enrichParquetForHF(exportDir); err != nil {
		slog.Warn("HF Viewer parquet enrichment skipped", "error", err)
	} else if fixedDir != "" && fixedDir != exportDir {
		exported = nil
		matches, _ := filepath.Glob(filepath.Join(fixedDir, "*.parquet"))
		exported = matches
		defer os.RemoveAll(fixedDir)
	}

	repoDir, err := os.MkdirTemp("", "vmc_repo_*")
	if err != nil {
		return fmt.Errorf("failed to create temp repo dir: %w", err)
	}
	defer os.RemoveAll(repoDir)

	if err := cloneDatasetRepo(cfg, repoDir); err != nil {
		return err
	}
	gitAuth := cfg.HFToken

	dataDir := filepath.Join(repoDir, "data")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}

	readmePath := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(readmePath, []byte(datasetCard()), 0644); err != nil {
		return fmt.Errorf("failed to write dataset card: %w", err)
	}
	_ = gitCmd(repoDir, "", "add", "README.md")

	var names []string
	for _, src := range exported {
		fileName := filepath.Base(src)
		destPath := filepath.Join(dataDir, fileName)
		if err := copyFile(src, destPath); err != nil {
			return fmt.Errorf("failed to write parquet to repo: %w", err)
		}
		if err := gitCmd(repoDir, "", "add", "data/"+fileName); err != nil {
			return fmt.Errorf("git add failed: %w", err)
		}
		names = append(names, fileName)
	}

	_ = gitCmd(repoDir, "", "add", "-A")

	msg := fmt.Sprintf("Upload %d shard(s): %s", len(names), strings.Join(names, ", "))
	if err := gitCmd(repoDir, "", "commit", "-m", msg); err != nil {
		if strings.Contains(err.Error(), "nothing to commit") || strings.Contains(err.Error(), "no changes added") {
			slog.Info("shards already exist on HF with same content", "count", len(names))
			return nil
		}
		return fmt.Errorf("git commit failed: %w", err)
	}

	if err := gitCmd(repoDir, gitAuth, "push"); err != nil {
		if pullErr := gitCmd(repoDir, gitAuth, "pull", "--rebase"); pullErr == nil {
			if retryErr := gitCmd(repoDir, gitAuth, "push"); retryErr == nil {
				slog.Info("successfully uploaded shards to HF", "count", len(names), "repo", cfg.HFRepo)
				return nil
			} else {
				return fmt.Errorf("git push failed after rebase: %w", retryErr)
			}
		}
		return fmt.Errorf("git push failed: %w", err)
	}

	slog.Info("successfully uploaded shards to HF", "count", len(names), "repo", cfg.HFRepo)
	return nil
}

func exportHFParquet(db *sql.DB, shardPath, destPath string) error {
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
	`, strings.ReplaceAll(shardPath, "'", "''"), strings.ReplaceAll(destPath, "'", "''"))

	if _, err := db.Exec(copyQuery); err != nil {
		return fmt.Errorf("failed to extract HF schema: %w", err)
	}
	return nil
}

func datasetCard() string {
	return `---
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
}

func cloneDatasetRepo(cfg *config.Config, repoDir string) error {
	repoURL := fmt.Sprintf("https://huggingface.co/datasets/%s", cfg.HFRepo)
	if err := gitCloneWithToken(repoDir, repoURL, cfg.HFToken); err != nil {
		if createErr := createRepo(cfg); createErr != nil {
			return fmt.Errorf("clone failed and repo creation failed: clone=%w, create=%v", err, createErr)
		}
		if err := gitCloneWithToken(repoDir, repoURL, cfg.HFToken); err != nil {
			return fmt.Errorf("clone failed after repo creation: %w", err)
		}
	}
	return nil
}

// gitCloneWithToken clones via Authorization header instead of embedding the
// token in the remote URL (avoids leaking credentials in process listings).
func gitCloneWithToken(repoDir, repoURL, token string) error {
	return gitCmd(repoDir, token, "clone", "--depth=1", repoURL, ".")
}

func gitCmd(dir, token string, args ...string) error {
	var cmdArgs []string
	if token != "" {
		cmdArgs = append(cmdArgs, "-c", "http.extraHeader=Authorization: Bearer "+token)
	}
	cmdArgs = append(cmdArgs, args...)
	cmd := exec.Command("git", cmdArgs...)
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
	var payload string
	switch len(parts) {
	case 2:
		payload = fmt.Sprintf(`{"type": "dataset", "name": "%s", "organization": "%s", "private": %v}`,
			parts[1], parts[0], cfg.HFPrivate)
	default:
		payload = fmt.Sprintf(`{"type": "dataset", "name": "%s", "private": %v}`, cfg.HFRepo, cfg.HFPrivate)
	}

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
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	slog.Info("created new HF dataset repo", "repo", cfg.HFRepo)
	return nil
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

// enrichParquetForHF runs scripts/fix_hf_parquet.py when uv/python is available.
// Returns a directory of fixed shards, or ("", nil) if enrichment is unavailable.
func enrichParquetForHF(exportDir string) (string, error) {
	script, err := findFixScript()
	if err != nil {
		return "", err
	}
	outDir, err := os.MkdirTemp("", "vmc_hf_fixed_*")
	if err != nil {
		return "", err
	}

	var cmd *exec.Cmd
	if _, err := exec.LookPath("uv"); err == nil {
		cmd = exec.Command("uv", "run", script, "--local", exportDir, "-o", outDir)
	} else if _, err := exec.LookPath("python3"); err == nil {
		cmd = exec.Command("python3", script, "--local", exportDir, "-o", outDir)
	} else {
		os.RemoveAll(outDir)
		return "", fmt.Errorf("neither uv nor python3 found for Viewer enrichment")
	}

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		os.RemoveAll(outDir)
		return "", fmt.Errorf("%w: %s", err, output.String())
	}

	matches, _ := filepath.Glob(filepath.Join(outDir, "*.parquet"))
	if len(matches) == 0 {
		os.RemoveAll(outDir)
		return "", fmt.Errorf("enrichment produced no parquet files")
	}
	slog.Info("enriched parquet with HF Audio footer metadata", "shards", len(matches))
	return outDir, nil
}

func findFixScript() (string, error) {
	candidates := []string{
		"scripts/fix_hf_parquet.py",
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, "fix_hf_parquet.py"),
			filepath.Join(exeDir, "..", "share", "vmc", "fix_hf_parquet.py"),
			filepath.Join(exeDir, "..", "..", "share", "vmc", "fix_hf_parquet.py"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "share", "vmc", "fix_hf_parquet.py"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			abs, err := filepath.Abs(c)
			if err != nil {
				return c, nil
			}
			return abs, nil
		}
	}
	return "", fmt.Errorf("fix_hf_parquet.py not found")
}
