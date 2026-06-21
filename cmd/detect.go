package cmd

import (
	"log/slog"

	"github.com/spf13/cobra"
)

var detectCmd = &cobra.Command{
	Use:   "detect",
	Short: "Detect new voice memos",
	Run: func(cmd *cobra.Command, args []string) {
		slog.Info("detect subcommand placeholder")
	},
}

func init() {
	rootCmd.AddCommand(detectCmd)
}
