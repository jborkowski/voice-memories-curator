package cmd

import (
	"log/slog"

	"github.com/spf13/cobra"
)

var uploadCmd = &cobra.Command{
	Use:   "upload",
	Short: "Upload processed shards to Hugging Face",
	Run: func(cmd *cobra.Command, args []string) {
		slog.Info("upload subcommand placeholder")
	},
}

func init() {
	rootCmd.AddCommand(uploadCmd)
}
