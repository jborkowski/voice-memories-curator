//go:build darwin && arm64

package main

import (
	"log/slog"
	"os"

	"github.com/jborkowski/vmc/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		if !cmd.IsReported(err) {
			slog.Error("execution failed", "error", err)
		}
		os.Exit(1)
	}
}
