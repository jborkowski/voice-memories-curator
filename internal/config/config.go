package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config top-level HF fields are the Hub push destination.
// [dji] is optional USB pull + filesystem ingest; set paths and device
// fingerprints only in the operator's local ~/.config/vmc/config.toml —
// never commit personal paths or device IDs into this repository.
type Config struct {
	HFToken            string `toml:"hf_token"`
	HFRepo             string `toml:"hf_repo"`
	HFPrivate          bool   `toml:"hf_private"`
	SyncInterval       int    `toml:"sync_interval"`
	UploadInterval     int    `toml:"upload_interval"`
	LogLevel           string `toml:"log_level"`
	ShardDir           string `toml:"shard_dir"`
	ShardMaxRows       int    `toml:"shard_max_rows"`
	KeepUploadedShards bool   `toml:"keep_uploaded_shards"`
	HFBaseURL          string `toml:"hf_base_url"`
	AppleDBPath        string `toml:"apple_db_path"`
	DJI                DJI    `toml:"dji"`
}

// DJI holds optional DJI Mic pull + ingest settings.
// Defaults are empty / safe: the feature is off until the operator fills
// local config (root, media_name, volume_uuid, usb ids, etc.).
type DJI struct {
	Enabled bool `toml:"enabled"`

	Root    string `toml:"root"`     // local inbox for pulled audio; set in config.toml
	DirGlob string `toml:"dir_glob"` // relative to Root
	DateFmt string `toml:"date_fmt"` // Go time layout for stamp folders

	DeviceLabel string `toml:"device_label"` // parquet device column when set

	MediaName  string `toml:"media_name"`
	VolumeUUID string `toml:"volume_uuid"`
	USBVendor  int    `toml:"usb_vendor"`
	USBProduct int    `toml:"usb_product"`

	AudioDirGlob  string `toml:"audio_dir_glob"`
	MountWaitSecs int    `toml:"mount_wait_secs"`
	MinAgeSeconds int    `toml:"min_age_seconds"`

	DeleteAfter bool `toml:"delete_after"`
	EjectAfter  bool `toml:"eject_after"`
	Notify      bool `toml:"notify"`

	PullLock string `toml:"pull_lock"`
	LogFile  string `toml:"log_file"` // vmc dji pull/watch log
}

// RootPath returns Root with a leading "~/" expanded.
func (d DJI) RootPath() (string, error) {
	if strings.TrimSpace(d.Root) == "" {
		return "", errors.New("dji.root is unset; set it in ~/.config/vmc/config.toml")
	}
	return ExpandHome(d.Root)
}

// Pattern returns the absolute glob for DJI recording directories under Root.
func (d DJI) Pattern() (string, error) {
	root, err := d.RootPath()
	if err != nil {
		return "", err
	}
	glob := d.DirGlob
	if glob == "" {
		glob = "????-??-??-??-??/DJI_Audio_*"
	}
	return filepath.Join(root, glob), nil
}

// PullLockPath returns the absolute pull lock directory path.
func (d DJI) PullLockPath() (string, error) {
	p := d.PullLock
	if p == "" {
		p = "~/.local/state/vmc/dji-pull.lock"
	}
	return ExpandHome(p)
}

// LogFilePath returns the absolute DJI pull log file path.
func (d DJI) LogFilePath() (string, error) {
	p := d.LogFile
	if p == "" {
		p = "~/Library/Logs/vmc-dji.log"
	}
	return ExpandHome(p)
}

// ExpandHome expands a leading "~/" (or a bare "~") to the user's home directory.
// Tildes elsewhere (e.g. Apple container path segments) are left untouched.
func ExpandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(homeDir, strings.TrimPrefix(path, "~")), nil
}

func DefaultConfig() *Config {
	return &Config{
		HFToken:            "",
		HFRepo:             "voice-memories",
		HFPrivate:          true,
		SyncInterval:       3600,
		UploadInterval:     604800,
		LogLevel:           "info",
		ShardDir:           "~/.local/share/vmc/shards",
		ShardMaxRows:       10,
		KeepUploadedShards: false,
		HFBaseURL:          "https://huggingface.co",
		AppleDBPath:        "",
		DJI: DJI{
			Enabled:       false,
			Root:          "", // operator-local only
			DirGlob:       "????-??-??-??-??/DJI_Audio_*",
			DateFmt:       "2006-01-02-15-04",
			DeviceLabel:   "",
			MediaName:     "",
			VolumeUUID:    "",
			USBVendor:     0,
			USBProduct:    0,
			AudioDirGlob:  "DJI_Audio_*",
			MountWaitSecs: 20,
			MinAgeSeconds: 120,
			DeleteAfter:   true,
			EjectAfter:    true,
			Notify:        true,
			PullLock:      "~/.local/state/vmc/dji-pull.lock",
			LogFile:       "~/Library/Logs/vmc-dji.log",
		},
	}
}

func LoadConfig() (*Config, error) {
	cfg := DefaultConfig()

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(homeDir, ".config", "vmc", "config.toml")
	if _, err := os.Stat(configPath); err == nil {
		if _, err := toml.DecodeFile(configPath, cfg); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	if hfToken := os.Getenv("HF_TOKEN"); hfToken != "" {
		cfg.HFToken = hfToken
	}

	return cfg, nil
}
