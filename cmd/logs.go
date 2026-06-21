package cmd

import (
	"log/slog"

	"github.com/spf13/cobra"
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View vmc logs",
	Run: func(cmd *cobra.Command, args []string) {
		slog.Info("logs subcommand placeholder")
	},
}

func init() {
	rootCmd.AddCommand(logsCmd)
}
