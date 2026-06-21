package cmd

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/jborkowski/vmc/internal/config"
	"github.com/jborkowski/vmc/internal/db"
	"github.com/jborkowski/vmc/internal/logging"
)

var (
	cfg  *config.Config
	duck *db.DuckDB
)

var rootCmd = &cobra.Command{
	Use:   "vmc",
	Short: "Voice Memories Curator",
	Long:  `vmc is a macOS daemon that extracts Voice Memos, transcodes them, and uploads them to Hugging Face.`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Do not initialize config and DB for help commands
		if cmd.Name() == "help" {
			return nil
		}

		initConfig()
		initLogger(cmd.Name())
		return initDB()
	},
	PersistentPostRunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Name() == "help" {
			return nil
		}
		cleanup()
		return nil
	},
}

func Execute() error {
	return rootCmd.Execute()
}

func initConfig() {
	var err error
	cfg, err = config.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
}

func initLogger(cmdName string) {
	level := slog.LevelInfo
	switch strings.ToLower(cfg.LogLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if cmdName == "daemon" {
		rw, err := logging.NewRotatingWriter()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create rotating logger, falling back to stdout: %v\n", err)
			handler = slog.NewJSONHandler(os.Stdout, opts)
		} else {
			handler = slog.NewJSONHandler(rw, opts)
		}
	} else {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	slog.SetDefault(slog.New(handler))
}

func initDB() error {
	var err error
	duck, err = db.InitDB()
	if err != nil {
		return fmt.Errorf("failed to initialize DuckDB: %w", err)
	}
	return nil
}

func cleanup() {
	if duck != nil {
		if err := duck.Close(); err != nil {
			slog.Error("failed to close DuckDB", "error", err)
		}
	}
}
