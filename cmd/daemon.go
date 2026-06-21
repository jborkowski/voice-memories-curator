package cmd

import (
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/jborkowski/vmc/internal/detect"
	"github.com/jborkowski/vmc/internal/process"
	"github.com/jborkowski/vmc/internal/upload"
)

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the vmc daemon",
	Run: func(cmd *cobra.Command, args []string) {
		slog.Info("starting daemon pass")

		// Detect
		start := time.Now()
		if err := detect.Run(duck.DB(), cfg); err != nil {
			slog.Error("detect phase failed", "phase", "detect", "duration_ms", time.Since(start).Milliseconds(), "error", err)
		} else {
			slog.Info("detect phase completed", "phase", "detect", "duration_ms", time.Since(start).Milliseconds())
		}

		// Process
		start = time.Now()
		if err := process.Run(duck.DB(), cfg); err != nil {
			slog.Error("process phase failed", "phase", "process", "duration_ms", time.Since(start).Milliseconds(), "error", err)
		} else {
			slog.Info("process phase completed", "phase", "process", "duration_ms", time.Since(start).Milliseconds())
		}

		// Upload
		start = time.Now()
		if err := upload.Run(duck.DB(), cfg); err != nil {
			slog.Error("upload phase failed", "phase", "upload", "duration_ms", time.Since(start).Milliseconds(), "error", err)
		} else {
			slog.Info("upload phase completed", "phase", "upload", "duration_ms", time.Since(start).Milliseconds())
		}

		slog.Info("daemon pass completed")
	},
}

func init() {
	rootCmd.AddCommand(daemonCmd)
}
