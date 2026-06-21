package cmd

import (
	"log/slog"

	"github.com/spf13/cobra"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Check vmc status and verify DuckDB connection",
	Run: func(cmd *cobra.Command, args []string) {
		err := duck.Ping()
		if err != nil {
			slog.Error("DuckDB connection failed", "error", err)
		} else {
			slog.Info("DuckDB connection successful")
		}
		slog.Info("status subcommand placeholder")
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
