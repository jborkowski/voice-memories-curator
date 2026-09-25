//go:build !darwin

package detect

import (
	"os"
	"time"
)

func fileCtime(info os.FileInfo) time.Time {
	return info.ModTime()
}

func isEvicted(os.FileInfo) bool {
	return false
}
