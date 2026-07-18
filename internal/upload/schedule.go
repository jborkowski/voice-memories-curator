package upload

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jborkowski/vmc/internal/config"
)

const defaultUploadInterval = 604800 // 7 days

func stateDir(cfg *config.Config) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	shardDir := cfg.ShardDir
	if strings.HasPrefix(shardDir, "~/") {
		shardDir = filepath.Join(homeDir, shardDir[2:])
	}
	// Store cadence state next to shards' parent (…/vmc/).
	return filepath.Dir(shardDir), nil
}

func lastUploadPath(cfg *config.Config) (string, error) {
	dir, err := stateDir(cfg)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "last_upload"), nil
}

// ShouldUpload reports whether upload_interval has elapsed since the last
// successful upload. Force always returns true. Missing state means upload.
func ShouldUpload(cfg *config.Config, force bool) (bool, error) {
	if force {
		return true, nil
	}
	interval := cfg.UploadInterval
	if interval <= 0 {
		interval = defaultUploadInterval
	}
	path, err := lastUploadPath(cfg)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return true, nil
	}
	last := time.Unix(sec, 0)
	return time.Since(last) >= time.Duration(interval)*time.Second, nil
}

// MarkUploaded records a successful upload timestamp for cadence gating.
func MarkUploaded(cfg *config.Config) error {
	path, err := lastUploadPath(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n", time.Now().Unix())), 0600)
}
