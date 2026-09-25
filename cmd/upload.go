package cmd

import (
	"github.com/spf13/cobra"

	"github.com/jborkowski/vmc/internal/upload"
)

var uploadForce bool

var uploadCmd = &cobra.Command{
	Use:   "upload",
	Short: "Upload processed shards to Hugging Face",
	RunE: func(cmd *cobra.Command, args []string) error {
		return upload.RunWithOptions(duck.DB(), cfg, uploadForce)
	},
}

func init() {
	uploadCmd.Flags().BoolVar(&uploadForce, "force", false, "ignore upload_interval cadence (still skips shards already on Hub)")
	rootCmd.AddCommand(uploadCmd)
}
