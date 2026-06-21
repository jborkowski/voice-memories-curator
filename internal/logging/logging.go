package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	maxFileSize = 10 * 1024 * 1024 // 10 MB
	maxFiles    = 5
)

type RotatingWriter struct {
	mu       sync.Mutex
	dir      string
	filePath string
	file     *os.File
	size     int64
}

func NewRotatingWriter() (*RotatingWriter, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(homeDir, "Library", "Logs", "vmc")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	filePath := filepath.Join(dir, "vmc.log")
	
	rw := &RotatingWriter{
		dir:      dir,
		filePath: filePath,
	}

	if err := rw.openFile(); err != nil {
		return nil, err
	}

	return rw, nil
}

func (rw *RotatingWriter) openFile() error {
	info, err := os.Stat(rw.filePath)
	var size int64
	if err == nil {
		size = info.Size()
	} else if !os.IsNotExist(err) {
		return err
	}

	file, err := os.OpenFile(rw.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	rw.file = file
	rw.size = size
	return nil
}

func (rw *RotatingWriter) Write(p []byte) (n int, err error) {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	writeLen := int64(len(p))
	if rw.size+writeLen > maxFileSize {
		if err := rw.rotate(); err != nil {
			return 0, err
		}
	}

	n, err = rw.file.Write(p)
	if err == nil {
		rw.size += int64(n)
	}
	return n, err
}

func (rw *RotatingWriter) rotate() error {
	if rw.file != nil {
		if err := rw.file.Close(); err != nil {
			return err
		}
		rw.file = nil
	}

	// Rotate files: vmc.log.4 -> vmc.log.5 (deleted), vmc.log.3 -> vmc.log.4, ..., vmc.log -> vmc.log.1
	for i := maxFiles - 1; i > 0; i-- {
		oldPath := fmt.Sprintf("%s.%d", rw.filePath, i)
		newPath := fmt.Sprintf("%s.%d", rw.filePath, i+1)

		if i == maxFiles-1 {
			// Delete the oldest file if it exists
			_ = os.Remove(oldPath)
			continue
		}

		if _, err := os.Stat(oldPath); err == nil {
			_ = os.Rename(oldPath, newPath)
		}
	}

	if _, err := os.Stat(rw.filePath); err == nil {
		_ = os.Rename(rw.filePath, fmt.Sprintf("%s.1", rw.filePath))
	}

	return rw.openFile()
}

func (rw *RotatingWriter) Close() error {
	rw.mu.Lock()
	defer rw.mu.Unlock()

	if rw.file != nil {
		err := rw.file.Close()
		rw.file = nil
		return err
	}
	return nil
}
