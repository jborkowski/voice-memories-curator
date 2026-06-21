package cmd

import (
	"github.com/jborkowski/vmc/internal/process"

	"github.com/spf13/cobra"
)

var processCmd = &cobra.Command{
	Use:   "process",
	Short: "Process detected voice memos",
	RunE: func(cmd *cobra.Command, args []string) error {
		return process.Run(duck.DB(), cfg)
	},
}

func init() {
	rootCmd.AddCommand(processCmd)
}
