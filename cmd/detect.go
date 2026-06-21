package cmd

import (
	"github.com/jborkowski/vmc/internal/detect"
	"github.com/spf13/cobra"
)

var detectCmd = &cobra.Command{
	Use:   "detect",
	Short: "Detect new voice memos",
	RunE: func(cmd *cobra.Command, args []string) error {
		return detect.Run(duck.DB(), cfg)
	},
}

func init() {
	rootCmd.AddCommand(detectCmd)
}
