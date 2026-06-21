package cmd

import (
	"log/slog"

	"github.com/spf13/cobra"
)

var processCmd = &cobra.Command{
	Use:   "process",
	Short: "Process detected voice memos",
	Run: func(cmd *cobra.Command, args []string) {
		slog.Info("process subcommand placeholder")
	},
}

func init() {
	rootCmd.AddCommand(processCmd)
}
