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
	Short: "Check vmc status and verify DuckDB connection",
	Run: func(cmd *cobra.Command, args []string) {
		// Online Status
		online := "Offline"
		client := http.Client{Timeout: 5 * time.Second}
		resp, err := client.Head("https://huggingface.co")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				online = "Online"
			}
		}

		base := strings.TrimRight(cfg.HFBaseURL, "/")
		if base == "" {
			base = "https://huggingface.co"
		}
		hfURL := fmt.Sprintf("%s/datasets/%s", base, cfg.HFRepo)

		// Shards directory
		shardDir := expandHome(cfg.ShardDir)
		var shards []string
		if entries, err := os.ReadDir(shardDir); err == nil {
			for _, e := range entries {
				if filepath.Ext(e.Name()) == ".parquet" {
					shards = append(shards, e.Name())
				}
			}
		}

		if len(shards) == 0 {
			fmt.Println("no shards found")
			fmt.Printf("\nNetwork: %s\n", online)
			fmt.Printf("Dataset: %s\n", hfURL)
			return
		}

		// DuckDB queries
		if err := duck.Ping(); err != nil {
			fmt.Fprintf(os.Stderr, "DuckDB connection failed: %v\n", err)
			os.Exit(1)
		}

		// Calculate stats
		var pendingRows, processedRows, readyShards int
		
		for _, shard := range shards {
			shardPath := filepath.Join(shardDir, shard)
			
			var pendingInShard, processedInShard int
			
			err = duck.DB().QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM read_parquet('%s') WHERE audio IS NULL`, shardPath)).Scan(&pendingInShard)
			if err != nil && err != sql.ErrNoRows {
				fmt.Fprintf(os.Stderr, "Failed to query pending rows in %s: %v\n", shard, err)
				continue
			}

			err = duck.DB().QueryRow(fmt.Sprintf(`SELECT COUNT(*) FROM read_parquet('%s') WHERE audio IS NOT NULL`, shardPath)).Scan(&processedInShard)
			if err != nil && err != sql.ErrNoRows {
				fmt.Fprintf(os.Stderr, "Failed to query processed rows in %s: %v\n", shard, err)
				continue
			}

			pendingRows += pendingInShard
			processedRows += processedInShard

			if pendingInShard == 0 && processedInShard > 0 {
				readyShards++
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
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
