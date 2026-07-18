package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/jborkowski/vmc/internal/detect"
	"github.com/jborkowski/vmc/internal/lock"
	"github.com/jborkowski/vmc/internal/process"
	"github.com/jborkowski/vmc/internal/upload"
)

var forceUpload bool

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run one detect → process → upload pass",
	RunE: func(cmd *cobra.Command, args []string) error {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		lockFile, err := lock.Acquire(filepath.Join(home, ".local", "share", "vmc", "vmc.lock"))
		if err != nil {
			return err
		}
		defer lockFile.Close()

		slog.Info("starting daemon pass")

		var firstErr error
		noteErr := func(phase string, err error) {
			if err == nil {
				return
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", phase, err)
			}
		}

		start := time.Now()
		if err := detect.Run(duck.DB(), cfg); err != nil {
			slog.Error("detect phase failed", "phase", "detect", "duration_ms", time.Since(start).Milliseconds(), "error", err)
			noteErr("detect", err)
		} else {
			slog.Info("detect phase completed", "phase", "detect", "duration_ms", time.Since(start).Milliseconds())
		}

		start = time.Now()
		if err := process.Run(duck.DB(), cfg); err != nil {
			slog.Error("process phase failed", "phase", "process", "duration_ms", time.Since(start).Milliseconds(), "error", err)
			noteErr("process", err)
		} else {
			slog.Info("process phase completed", "phase", "process", "duration_ms", time.Since(start).Milliseconds())
		}

		start = time.Now()
		if err := upload.RunWithOptions(duck.DB(), cfg, forceUpload); err != nil {
			slog.Error("upload phase failed", "phase", "upload", "duration_ms", time.Since(start).Milliseconds(), "error", err)
			noteErr("upload", err)
		} else {
			slog.Info("upload phase completed", "phase", "upload", "duration_ms", time.Since(start).Milliseconds())
		}

		if firstErr != nil {
			slog.Error("daemon pass completed with errors", "error", firstErr)
			return firstErr
		}
		slog.Info("daemon pass completed")
		return nil
	},
}

func init() {
	daemonCmd.Flags().BoolVar(&forceUpload, "force-upload", false, "upload ready shards even if upload_interval has not elapsed")
	rootCmd.AddCommand(daemonCmd)
}
