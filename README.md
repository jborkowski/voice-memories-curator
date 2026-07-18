# Voice Memories Curator (vmc)

A macOS daemon that periodically extracts macOS Voice Memos, transcodes audio to FLAC, and uploads Parquet shards to a private Hugging Face dataset.

Detect/process run hourly via `brew services`. Hub upload is gated by `upload_interval` (default **weekly**) so the Voice Memos database and HF are not hammered every hour.

## Installation

### Homebrew (recommended)

```bash
brew tap jborkowski/vmc https://github.com/jborkowski/voice-memories-curator
brew install --HEAD vmc
git xet install
```

This installs `vmc`, `ffmpeg`, `git-xet`, and the Viewer repair helper script.

To run as a background service:

```bash
brew services start vmc
```

### From source

Prerequisites:
- macOS Apple Silicon (darwin/arm64)
- CGO enabled (required for DuckDB)
- Go 1.22+
- ffmpeg
- git-xet (`brew install git-xet && git xet install`)

```bash
make build
make install   # installs to ~/.local/bin
```

Make sure `~/.local/bin` is in your `$PATH`:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

## Permissions

`vmc` reads the macOS Voice Memos SQLite database at:

```
~/Library/Application Support/com.apple.voicememos/Recordings/CloudRecordings.db
```

macOS restricts access to this file. Grant **Full Disk Access** to the process that runs `vmc`:

| How you run `vmc` | Grant FDA to |
|-------------------|--------------|
| Interactive terminal | Your terminal app (Terminal, iTerm2, Warp, etc.) |
| `brew services` / launchd | The `vmc` binary (e.g. `/opt/homebrew/opt/vmc/bin/vmc`) |
| `make install` binary | `~/.local/bin/vmc` |

Open the Full Disk Access settings pane:

```bash
make permissions
```

Then add the binary or terminal app and restart the process.

If you see:
```
cannot open Voice Memos database — grant Full Disk Access to vmc
```
the running process does not yet have the required permission.

## Configuration

Configuration is read from `~/.config/vmc/config.toml`. Prefer `HF_TOKEN` in the environment over storing the token in the file.

Example:
```toml
# Prefer: export HF_TOKEN=hf_...
# hf_token = "hf_..."
hf_repo = "YOUR_USER/voice-memories"
hf_private = true
sync_interval = 3600          # documented brew detect/process cadence (informational)
upload_interval = 604800      # seconds between Hub publishes (default: 1 week)
log_level = "info"
shard_dir = "~/.local/share/vmc/shards"
keep_uploaded_shards = false
```

| Key | Role |
|-----|------|
| `sync_interval` | Documents the intended detect/process cadence. Homebrew `interval 3600` owns the actual schedule. |
| `upload_interval` | Minimum seconds between Hub uploads (default `604800`). Detect/process still run every service tick. |

## Running

```bash
vmc --help
vmc status
vmc daemon                 # detect → process → upload (if interval elapsed)
vmc daemon --force-upload  # always attempt upload this pass
vmc upload --force         # publish ready shards now
```

Only one daemon/upload instance runs at a time (`~/.local/share/vmc/vmc.lock`).

## Design notes

- Apple’s `CloudRecordings.db` is **snapshotted** (main DB + WAL/SHM) and released before any Hugging Face network I/O, so Voice Memos is not held open during uploads or remote dedup.
- Ready shards are published in **one** git clone/commit/push batch.
- When `uv` or `python3` plus `scripts/fix_hf_parquet.py` are available, shards are rewritten with Hugging Face Audio footer metadata before push (Dataset Viewer).
