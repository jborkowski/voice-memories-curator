package cmd

import (
	"log/slog"

	"github.com/spf13/cobra"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the vmc daemon",
	Run: func(cmd *cobra.Command, args []string) {
		slog.Info("running detect → process → upload")
	},
}

func init() {
	rootCmd.AddCommand(daemonCmd)
}
