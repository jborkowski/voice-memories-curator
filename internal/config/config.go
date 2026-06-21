package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	HFToken            string `toml:"hf_token"`
	HFRepo             string `toml:"hf_repo"`
	HFPrivate          bool   `toml:"hf_private"`
	SyncInterval       int    `toml:"sync_interval"`
	LogLevel           string `toml:"log_level"`
	ShardDir           string `toml:"shard_dir"`
	KeepUploadedShards bool   `toml:"keep_uploaded_shards"`
}

func DefaultConfig() *Config {
	return &Config{
		HFToken:            "",
		HFRepo:             "voice-memories",
		HFPrivate:          true,
		SyncInterval:       3600,
		LogLevel:           "info",
		ShardDir:           "~/.local/share/vmc/shards",
		KeepUploadedShards: false,
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
