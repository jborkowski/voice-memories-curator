package upload

import (
	"bytes"
	"database/sql"
	"encoding/json"
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

const defaultUploadBatchSize = 2

// Run uploads ready shards that are missing from Hub (Hub API, batched).
func Run(db *sql.DB, cfg *config.Config) error {
	return RunWithOptions(db, cfg, false)
}

// RunWithOptions publishes ready shards missing from Hub.
// force ignores upload_interval cadence only — it never re-pushes shards
// already present on Hub by filename (partitioned data/*.parquet).
func RunWithOptions(db *sql.DB, cfg *config.Config, force bool) error {
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

	matches, err := filepath.Glob(filepath.Join(shardDir, "*.parquet"))
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

	// Always filter by Hub partition names — force does not skip this.
	remote, err := listRemoteShardNames(cfg)
	if err != nil {
		slog.Warn("remote shard listing failed; uploading all ready shards", "error", err)
	} else {
		readyShards = filterMissingRemote(readyShards, remote)
		if len(readyShards) == 0 {
			slog.Info("all ready shards already on Hub")
			return nil
		}
	}

	// Missing partitions always publish (even when cadence would block empty republish).
	if !force {
		ok, cerr := ShouldUpload(cfg, false)
		if cerr != nil {
			slog.Warn("cadence check failed; continuing with missing shards", "error", cerr)
		} else if !ok {
			slog.Info("upload_interval not elapsed; still publishing missing Hub partitions",
				"missing", len(readyShards))
		}
	}

	slog.Info(fmt.Sprintf("%d shards ready for upload", len(readyShards)))

	batches := batchPaths(readyShards, effectiveBatchSize(cfg))
	slog.Info("upload batches", "batches", len(batches), "batch_size", effectiveBatchSize(cfg))

	var uploaded []string
	for i, batch := range batches {
		slog.Info("upload batch start", "batch", i+1, "of", len(batches), "shards", len(batch))
		if err := uploadShardsBatch(db, cfg, batch); err != nil {
			return err
		}
		uploaded = append(uploaded, batch...)
		slog.Info("upload batch done", "batch", i+1, "of", len(batches))
	}

	for _, shardPath := range uploaded {
		name := filepath.Base(shardPath)
		escaped := strings.ReplaceAll(shardPath, "'", "''")
		escapedName := strings.ReplaceAll(name, "'", "''")
		_, _ = db.Exec(`CREATE TABLE IF NOT EXISTS remote_dedup_cache (
			shard_name VARCHAR,
			recording_id BIGINT,
			device VARCHAR,
			created_at VARCHAR,
			duration_seconds DOUBLE,
			PRIMARY KEY (shard_name, recording_id)
		)`)
		_, _ = db.Exec(fmt.Sprintf(`
			INSERT OR IGNORE INTO remote_dedup_cache
			SELECT '%s', recording_id, device, created_at, duration_seconds
			FROM read_parquet('%s')
		`, escapedName, escaped))
	}

	if !cfg.KeepUploadedShards {
		for _, shardPath := range uploaded {
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

func effectiveBatchSize(cfg *config.Config) int {
	if cfg.UploadBatchSize <= 0 {
		return defaultUploadBatchSize
	}
	return cfg.UploadBatchSize
}

func batchPaths(paths []string, size int) [][]string {
	if size <= 0 {
		size = defaultUploadBatchSize
	}
	if len(paths) == 0 {
		return nil
	}
	var out [][]string
	for i := 0; i < len(paths); i += size {
		end := i + size
		if end > len(paths) {
			end = len(paths)
		}
		out = append(out, paths[i:end])
	}
	return out
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
		name := filepath.Base(shardPath)
		dest := filepath.Join(exportDir, name)
		slog.Info("exporting shard for Hub", "shard", name)
		if err := exportHFParquet(db, shardPath, dest); err != nil {
			return fmt.Errorf("export %s: %w", shardPath, err)
		}
		if st, err := os.Stat(dest); err == nil {
			slog.Info("exported shard", "shard", name, "bytes", st.Size())
		}
		exported = append(exported, dest)
	}

	fixedDir := ""
	if dir, err := enrichParquetForHF(exportDir); err != nil {
		slog.Warn("HF Viewer parquet enrichment skipped", "error", err)
	} else if dir != "" && dir != exportDir {
		fixedDir = dir
		defer os.RemoveAll(fixedDir)
		matches, _ := filepath.Glob(filepath.Join(fixedDir, "*.parquet"))
		exported = matches
		slog.Info("enriched parquet with HF Audio footer metadata", "shards", len(exported))
	}

	readmeFile, err := os.CreateTemp("", "vmc_readme_*.md")
	if err != nil {
		return fmt.Errorf("readme temp: %w", err)
	}
	readmePath := readmeFile.Name()
	defer os.Remove(readmePath)
	if _, err := readmeFile.WriteString(datasetCard()); err != nil {
		readmeFile.Close()
		return err
	}
	if err := readmeFile.Close(); err != nil {
		return err
	}

	names := make([]string, 0, len(exported))
	for _, p := range exported {
		names = append(names, filepath.Base(p))
	}
	msg := fmt.Sprintf("Upload %d shard(s): %s", len(names), strings.Join(names, ", "))

	slog.Info("hub upload start", "shards", len(exported), "repo", cfg.HFRepo)
	if err := hubUploadShards(cfg, exported, readmePath, msg); err != nil {
		return err
	}
	slog.Info("hub upload commit ok", "shards", len(exported), "repo", cfg.HFRepo)

	if err := verifyRemoteHas(cfg, names); err != nil {
		return err
	}
	slog.Info("successfully uploaded shards to HF", "count", len(names), "repo", cfg.HFRepo)
	return nil
}

func verifyRemoteHas(cfg *config.Config, names []string) error {
	remote, err := listRemoteShardNames(cfg)
	if err != nil {
		return fmt.Errorf("post-upload verify: %w", err)
	}
	var missing []string
	for _, n := range names {
		if _, ok := remote[n]; !ok {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("post-upload verify: Hub still missing %v", missing)
	}
	return nil
}

func hubUploadShards(cfg *config.Config, files []string, readmePath, message string) error {
	script, err := findUploadScript()
	if err != nil {
		return err
	}

	args := []string{"--repo", cfg.HFRepo, "--message", message, "--readme", readmePath}
	if cfg.HFPrivate {
		args = append(args, "--private")
	}
	args = append(args, files...)

	var cmd *exec.Cmd
	if _, err := exec.LookPath("uv"); err == nil {
		cmd = exec.Command("uv", append([]string{"run", script}, args...)...)
	} else if _, err := exec.LookPath("python3"); err == nil {
		cmd = exec.Command("python3", append([]string{script}, args...)...)
	} else {
		return fmt.Errorf("neither uv nor python3 found for Hub upload")
	}

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	env := append(os.Environ(), "HF_TOKEN="+cfg.HFToken, "GIT_TERMINAL_PROMPT=0")
	// Prefer config token over a stale shell HF_TOKEN.
	cmd.Env = rewriteEnvToken(env, cfg.HFToken)

	slog.Info("running hub upload script", "script", filepath.Base(script), "files", len(files))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hub upload failed: %w: %s", err, strings.TrimSpace(output.String()))
	}
	if s := strings.TrimSpace(output.String()); s != "" {
		for _, line := range strings.Split(s, "\n") {
			if line != "" {
				slog.Info("hub upload", "line", line)
			}
		}
	}
	return nil
}

func rewriteEnvToken(env []string, token string) []string {
	out := make([]string, 0, len(env)+1)
	seen := false
	for _, e := range env {
		if strings.HasPrefix(e, "HF_TOKEN=") {
			out = append(out, "HF_TOKEN="+token)
			seen = true
			continue
		}
		out = append(out, e)
	}
	if !seen {
		out = append(out, "HF_TOKEN="+token)
	}
	return out
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

Private partitioned audio dataset (Apple Voice Memos + optional DJI Mic).

Each file under ` + "`data/*.parquet`" + ` is one partition/shard. Audio columns use the
Hugging Face ` + "`Audio`" + ` feature (` + "`bytes`" + ` + ` + "`path`" + `) — FLAC 16 kHz mono in
` + "`audio`" + `, AAC/original in ` + "`audio_original`" + `.

## Load (map-style)

` + "```python" + `
from datasets import load_dataset
ds = load_dataset("USER/voice-memories", split="train")
` + "```" + `

## Load (iterable / streaming)

Partitions stream one shard at a time — preferred for large collections:

` + "```python" + `
from datasets import load_dataset
ds = load_dataset("USER/voice-memories", split="train", streaming=True)
for row in ds:
    # row["audio"] is decoded on demand
    break
` + "```" + `
`
}

func isRepoExistsErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "409") || strings.Contains(s, "already created") || strings.Contains(s, "already exist")
}

// ListRemoteShardNames returns parquet basenames under data/ on the Hub.
func ListRemoteShardNames(cfg *config.Config) (map[string]struct{}, error) {
	return listRemoteShardNames(cfg)
}

func listRemoteShardNames(cfg *config.Config) (map[string]struct{}, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	apiURL := fmt.Sprintf("%s/api/datasets/%s/tree/main/data", cfg.HFBaseURL, cfg.HFRepo)
	req, err := http.NewRequest("GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.HFToken)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return map[string]struct{}{}, nil
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HF tree API %d", resp.StatusCode)
	}
	var files []struct {
		Type string `json:"type"`
		Path string `json:"path"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&files); err != nil {
		return nil, err
	}
	out := make(map[string]struct{})
	for _, f := range files {
		if f.Type != "" && f.Type != "file" {
			continue
		}
		base := filepath.Base(f.Path)
		if strings.HasSuffix(base, ".parquet") {
			out[base] = struct{}{}
		}
	}
	return out, nil
}

// FilterMissingRemote returns local paths whose basenames are absent from remote.
func FilterMissingRemote(local []string, remote map[string]struct{}) []string {
	return filterMissingRemote(local, remote)
}

func filterMissingRemote(local []string, remote map[string]struct{}) []string {
	var missing []string
	for _, p := range local {
		if _, ok := remote[filepath.Base(p)]; !ok {
			missing = append(missing, p)
		}
	}
	return missing
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
	return outDir, nil
}

func findShareScript(name string) (string, error) {
	candidates := []string{
		filepath.Join("scripts", name),
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(exeDir, name),
			filepath.Join(exeDir, "..", "share", "vmc", name),
			filepath.Join(exeDir, "..", "..", "share", "vmc", name),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".local", "share", "vmc", name))
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
	return "", fmt.Errorf("%s not found", name)
}

func findFixScript() (string, error) {
	return findShareScript("fix_hf_parquet.py")
}

func findUploadScript() (string, error) {
	return findShareScript("upload_hf_shards.py")
}
