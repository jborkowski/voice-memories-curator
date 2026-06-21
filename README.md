# Voice Memories Curator (vmc)

A macOS daemon that periodically extracts macOS Voice Memos, transcodes audio to FLAC, and uploads Parquet shards to a private Hugging Face dataset.

## Prerequisites

- macOS Apple Silicon (darwin/arm64)
- CGO enabled (required for DuckDB)
- **Full Disk Access** for your terminal (see [Permissions](#permissions) below)

## Building

```bash
make build
```

Other targets:

```bash
make run      # build + run
make install  # build + install to ~/.local/bin
make clean    # remove binary
```

## Installation

```bash
make install
```

This installs the `vmc` binary to `~/.local/bin/`. Make sure this directory is in your `$PATH`:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

## Permissions

`vmc` reads the macOS Voice Memos SQLite database at:

```
~/Library/Application Support/com.apple.voicememos/Recordings/CloudRecordings.db
```

macOS restricts access to this file. You must grant **Full Disk Access** to the terminal application you use to run `vmc`.

### Granting Full Disk Access

Open the Full Disk Access settings pane directly from the terminal:

```bash
make permissions
```

Then:

1. Click the **+** button (you may need to unlock with your password)
2. Add your terminal app (e.g. Terminal.app, iTerm2, Alacritty, Warp, or the VS Code/Cursor integrated terminal)
3. Restart the terminal after granting access

> **Note:** If you run `vmc` via a launchd agent or cron, the parent process (e.g. `launchd`) must also have Full Disk Access.

If you see the error:
```
cannot open Voice Memos database — is Full Disk Access enabled for this terminal?
```
it means the running process does not yet have the required permission.

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
vmc --help
vmc status
vmc daemon
```
