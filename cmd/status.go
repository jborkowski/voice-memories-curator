package cmd

import (
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	_ "github.com/marcboeker/go-duckdb"

	"github.com/jborkowski/vmc/internal/upload"
)

func expandHome(path string) string {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[1:])
		}
	}
	return path
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check vmc status and shard progress",
	RunE: func(cmd *cobra.Command, args []string) error {
		base := strings.TrimRight(cfg.HFBaseURL, "/")
		if base == "" {
			base = "https://huggingface.co"
		}

		online := "Offline"
		client := http.Client{Timeout: 5 * time.Second}
		if resp, err := client.Head(base); err == nil {
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				online = "Online"
			}
		}

		hfURL := fmt.Sprintf("%s/datasets/%s", base, cfg.HFRepo)

		shardDir := expandHome(cfg.ShardDir)
		var shards []string
		if entries, err := os.ReadDir(shardDir); err == nil {
			for _, e := range entries {
				if filepath.Ext(e.Name()) == ".parquet" && !strings.HasSuffix(e.Name(), "_tmp.parquet") {
					shards = append(shards, e.Name())
				}
			}
		}

		if len(shards) == 0 {
			fmt.Println("no shards found")
			fmt.Printf("\nNetwork: %s\n", online)
			fmt.Printf("Dataset: %s\n", hfURL)
			return nil
		}

		// In-memory DuckDB — never contend with the daemon's vmc.db lock.
		mem, err := sql.Open("duckdb", "")
		if err != nil {
			return fmt.Errorf("open in-memory duckdb: %w", err)
		}
		defer mem.Close()

		var pendingRows, processedRows, readyShards int
		var readyPaths []string
		for _, shard := range shards {
			shardPath := filepath.Join(shardDir, shard)
			escaped := strings.ReplaceAll(shardPath, "'", "''")

			var pendingInShard, processedInShard int
			if err := mem.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM read_parquet('%s') WHERE audio IS NULL`, escaped)).Scan(&pendingInShard); err != nil && err != sql.ErrNoRows {
				fmt.Fprintf(os.Stderr, "Failed to query pending rows in %s: %v\n", shard, err)
				continue
			}
			if err := mem.QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM read_parquet('%s') WHERE audio IS NOT NULL`, escaped)).Scan(&processedInShard); err != nil && err != sql.ErrNoRows {
				fmt.Fprintf(os.Stderr, "Failed to query processed rows in %s: %v\n", shard, err)
				continue
			}

			pendingRows += pendingInShard
			processedRows += processedInShard
			if pendingInShard == 0 && processedInShard > 0 {
				readyShards++
				readyPaths = append(readyPaths, shardPath)
			}
		}

		remoteCount := -1
		var missingNames []string
		lastUpload := "(none)"
		if path, err := upload.LastUploadTime(cfg); err == nil && !path.IsZero() {
			lastUpload = path.Format(time.RFC3339)
		}
		if cfg.HFToken != "" && online == "Online" {
			if remote, err := upload.ListRemoteShardNames(cfg); err != nil {
				fmt.Fprintf(os.Stderr, "Hub tree: %v\n", err)
			} else {
				remoteCount = len(remote)
				for _, p := range upload.FilterMissingRemote(readyPaths, remote) {
					missingNames = append(missingNames, filepath.Base(p))
				}
			}
		}

		fmt.Println("VMC Status Report")
		fmt.Println("-----------------")
		fmt.Printf("Total shards:   %d\n", len(shards))
		fmt.Printf("Ready shards:   %d\n", readyShards)
		fmt.Printf("Pending rows:   %d\n", pendingRows)
		fmt.Printf("Processed rows: %d\n", processedRows)
		fmt.Println("-----------------")
		fmt.Printf("Network:        %s\n", online)
		fmt.Printf("Dataset:        %s\n", hfURL)
		if remoteCount >= 0 {
			fmt.Printf("Hub parquet:    %d\n", remoteCount)
			fmt.Printf("Missing on Hub: %d\n", len(missingNames))
			if len(missingNames) > 0 && len(missingNames) <= 12 {
				fmt.Printf("  %s\n", strings.Join(missingNames, ", "))
			} else if len(missingNames) > 12 {
				fmt.Printf("  %s …\n", strings.Join(missingNames[:12], ", "))
			}
		}
		bs := cfg.UploadBatchSize
		if bs <= 0 {
			bs = 2
		}
		fmt.Printf("Last upload:    %s\n", lastUpload)
		fmt.Printf("Batch size:     %d\n", bs)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
