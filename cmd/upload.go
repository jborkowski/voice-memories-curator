package cmd

import (
	"log/slog"
	"os"

	"github.com/jborkowski/vmc/internal/config"
	"github.com/jborkowski/vmc/internal/db"
	"github.com/jborkowski/vmc/internal/upload"
	"github.com/spf13/cobra"
)

var uploadCmd = &cobra.Command{
	Use:   "upload",
	Short: "Upload processed shards to Hugging Face",
	Run: func(cmd *cobra.Command, args []string) {
		cfg, err := config.LoadConfig()
		if err != nil {
			slog.Error("failed to load config", "error", err)
			os.Exit(1)
		}

		database, err := db.InitDB()
		if err != nil {
			slog.Error("failed to open duckdb", "error", err)
			os.Exit(1)
		}
		defer database.Close()

		if err := upload.Run(database.DB(), cfg); err != nil {
			slog.Error("upload phase failed", "error", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(uploadCmd)
}
