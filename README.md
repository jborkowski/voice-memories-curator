# Voice Memories Curator (vmc)

A macOS daemon that periodically extracts macOS Voice Memos, transcodes audio to FLAC, and uploads Parquet shards to a private Hugging Face dataset.

## Building

Requires macOS Apple Silicon (darwin/arm64) and CGO enabled.

```bash
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build -o vmc
```

## Configuration

Configuration is read from `~/.config/vmc/config.toml`. 

Example:
```toml
hf_token = "YOUR_TOKEN"
hf_repo = "voice-memories"
hf_private = true
sync_interval = 3600
log_level = "info"
shard_dir = "~/.local/share/vmc/shards"
keep_uploaded_shards = false
```

## Running

```bash
./vmc --help
./vmc status
./vmc daemon
```
