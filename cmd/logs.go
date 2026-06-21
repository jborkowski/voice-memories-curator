package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"
)

var (
	logLines  int
	logFollow bool
)

var logsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View vmc logs",
	Run: func(cmd *cobra.Command, args []string) {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error getting home directory: %v\n", err)
			os.Exit(1)
		}

		logFile := filepath.Join(homeDir, "Library", "Logs", "vmc", "vmc.log")
		if _, err := os.Stat(logFile); os.IsNotExist(err) {
			fmt.Printf("No log file found at %s\n", logFile)
			return
		}

		tailArgs := []string{"-n", strconv.Itoa(logLines)}
		if logFollow {
			tailArgs = append(tailArgs, "-f")
		}
		tailArgs = append(tailArgs, logFile)

		tailCmd := exec.Command("tail", tailArgs...)
		tailCmd.Stdout = os.Stdout
		tailCmd.Stderr = os.Stderr
		
		if err := tailCmd.Run(); err != nil {
			// tail -f might be killed, which is normal.
			// Only print if it's not an ExitError or exit code is not 0/130
			if exitErr, ok := err.(*exec.ExitError); ok {
				if exitErr.ExitCode() != 0 && exitErr.ExitCode() != 130 {
					fmt.Fprintf(os.Stderr, "Error running tail: %v\n", err)
				}
			} else {
				fmt.Fprintf(os.Stderr, "Error running tail: %v\n", err)
			}
		}
	},
}

func init() {
	logsCmd.Flags().IntVarP(&logLines, "lines", "n", 50, "Number of lines to show")
	logsCmd.Flags().BoolVarP(&logFollow, "follow", "f", false, "Follow log output")
	rootCmd.AddCommand(logsCmd)
}
