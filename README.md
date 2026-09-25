# Voice Memories Curator (vmc)

A macOS daemon that periodically extracts macOS Voice Memos (and, optionally, DJI Mic recordings), transcodes audio to FLAC, and uploads Parquet shards to a private Hugging Face dataset.

Detect/process/upload run hourly via `brew services`. Upload pushes only shards that are missing from Hugging Face. Ops details: [docs/02-ops-flow.md](docs/02-ops-flow.md).

## Installation

### Homebrew (recommended)

```bash
brew tap jborkowski/vmc https://github.com/jborkowski/voice-memories-curator
brew install --HEAD vmc
git xet install
```

This installs `vmc`, `ffmpeg`, `git-xet`, Viewer repair (`fix_hf_parquet.py`), and Hub upload (`upload_hf_shards.py`) under `share/vmc`. Service logs: `$(brew --prefix)/var/log/vmc.log`.

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
make install   # ~/.local/bin + share/{fix_hf_parquet,upload_hf_shards}.py
make test      # go test ./...
```

Homebrew after pushing `main`:

```bash
make brew-reinstall   # brew install --HEAD
make brew-restart     # FDA refresh + brew services restart
make logs             # tail $(brew --prefix)/var/log/vmc.log
```

`make help` lists all targets. Dataset partitions load with `streaming=True` (see dataset card / ADR-03).

Make sure `~/.local/bin` is in your `$PATH`:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

## Permissions (Full Disk Access)

`brew services` runs the `vmc` binary under launchd. That binary needs **Full Disk Access**.

```bash
vmc-grant-fda
```

Creates **`Desktop/VMC.app`**. Drag it into Full Disk Access → ON → `brew services restart vmc`.

## Configuration

Configuration is read from `~/.config/vmc/config.toml`. Prefer `HF_TOKEN` in the environment over storing the token in the file.

Example:
```toml
# Prefer: export HF_TOKEN=hf_...
# hf_token = "hf_..."
hf_repo = "YOUR_USER/voice-memories"
hf_private = true
sync_interval = 3600          # documented brew detect/process cadence (informational)
upload_interval = 604800      # seconds between empty Hub republish gating (default: 1 week)
upload_batch_size = 2         # parquet partitions per Hub commit
log_level = "info"
shard_dir = "~/.local/share/vmc/shards"
keep_uploaded_shards = false
```

| Key | Role |
|-----|------|
| `sync_interval` | Documents the intended detect/process cadence. Homebrew `interval 3600` owns the actual schedule. |
| `upload_interval` | Cadence gate when nothing is missing on Hub (default `604800`). Missing partitions always upload. |
| `upload_batch_size` | Partitions per Hub API commit (default `2`). |

### DJI Mic recordings (optional)

Optional second source: USB pull into a **local** inbox, then the same detect → process → upload path. Off by default. Paths, device fingerprints, and labels belong in **your** `~/.config/vmc/config.toml` only — they are not baked into the public defaults.

```bash
vmc dji detect                 # fingerprints / is it mounted?
vmc dji pull                   # copy (defaults from [dji])
vmc dji pull --keep --no-eject
vmc dji eject
vmc dji watch enable           # optional: pull on /Volumes change
```

If you previously used a separate `dji-mic` launchd watcher, disable it before `vmc dji watch enable` so the two do not race.

```toml
[dji]
enabled = false
root = "~/path/to/your/dji-inbox"   # set locally; do not commit real paths
dir_glob = "????-??-??-??-??/DJI_Audio_*"
device_label = "DJI Mic"            # optional parquet device tag
min_age_seconds = 120
# media_name / volume_uuid / usb_vendor / usb_product — set locally for USB pull
```

`[dji]` is **where to pull from**. **Where to push** is always top-level `hf_repo` / `hf_token` / `hf_private`. See [adr/02-dji-source.md](adr/02-dji-source.md).

## Running

```bash
vmc --help
vmc status
vmc daemon                 # [dji pull if enabled] → detect → process → upload
vmc daemon --force-upload  # ignore upload_interval cadence
vmc upload                 # Hub API: missing data/*.parquet partitions only
vmc upload --force         # same filter; cadence-only force
vmc dji --help             # USB pull / detect / eject / watch
```

Only one daemon/upload instance runs at a time (`~/.local/share/vmc/vmc.lock`).

## Design notes

- Apple’s `CloudRecordings.db` is **snapshotted** (main DB + WAL/SHM) and released before any Hugging Face network I/O, so Voice Memos is not held open during uploads or remote dedup.
- Hub layout is **partitioned** `data/*.parquet` with HF `Audio` columns — load with `streaming=True` for iterable access. See [adr/03-hub-partitioned-upload.md](adr/03-hub-partitioned-upload.md).
- Ready partitions publish via **Hub API batches** (`scripts/upload_hf_shards.py`), not a full-repo git-LFS push.
- When `uv` or `python3` plus `scripts/fix_hf_parquet.py` are available, shards get Hugging Face Audio footer metadata before push (Dataset Viewer).
- Service logs: `$(brew --prefix)/var/log/vmc.log`.
