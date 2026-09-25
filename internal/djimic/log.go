package djimic

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// FileLogger appends timestamped lines to a log file and, unless Quiet,
// echoes them to Stdout.
type FileLogger struct {
	Path   string
	Quiet  bool
	Stdout io.Writer
	Now    func() time.Time
}

// Logf implements Tool.Logf.
func (l *FileLogger) Logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	if l.Path != "" {
		if err := os.MkdirAll(filepath.Dir(l.Path), 0o755); err == nil {
			if f, err := os.OpenFile(l.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprintf(f, "%s %s\n", now().Format("2006-01-02 15:04:05"), msg)
				f.Close()
			}
		}
	}
	if !l.Quiet && l.Stdout != nil {
		fmt.Fprintln(l.Stdout, msg)
	}
}
