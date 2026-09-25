# VMC ops flow (Solmigo / brew services)

## Pipeline

```
brew services (hourly)
  → vmc daemon
      → detect   (read Voice Memos DB [+ DJI inbox WAVs if [dji] enabled] → local parquet shards)
      → process  (ffmpeg → FLAC in shards; DJI WAV also → AAC .m4a original)
      → upload   (push only shards missing from Hugging Face)
```

- **Detect/process:** every hour (`Formula` `interval 3600`).
- **Upload:** automatic when local ready shards are not on Hub. No `--force` for normal use.
- **State:** `~/.local/share/vmc/shards/`, `vmc.db`, `vmc.lock`.
- **Config:** `~/.config/vmc/config.toml` (`hf_token`, `hf_repo`, `apple_db_path`, `[dji]`, …). Personal inbox paths and device IDs live only in that local file.

## DJI Mic source (optional, off by default)

Hourly ingest stays on the single `brew services` job. When `[dji] enabled = true` and `root` is set, each tick runs `pull --if-present` (if the transmitter is mounted) then detect → process → upload.

```
vmc dji pull → local inbox/*.WAV → detect → process → upload → hf_repo
```

- **Where to pull from:** `[dji]` in local config (`root`, fingerprints, …). Public defaults are empty — fill paths and device IDs only in `~/.config/vmc/config.toml`.
- **Where to push to:** top-level `hf_repo` / `hf_token` / `hf_private` (same dataset as Voice Memos).
- **Enable:** set `[dji] enabled = true` and a real `root`, then `brew services restart vmc`.
- **Rows:** `device` from `device_label` when set; title / transcription / location stay NULL. `audio` is 16 kHz mono FLAC; `audio_original` is AAC `.m4a`.
- **Process failures:** bad WAV / AAC encode → row skipped and retried next tick.

### USB CLI (`vmc dji`)

| Command | Role |
|---------|------|
| `vmc dji pull` | Copy audio into `<root>/<stamp>/` (flags: `--keep`/`--delete`, `--eject`/`--no-eject`, `--dry-run`, `--if-present`, `--source`, `--dest`) |
| `vmc dji detect` | Print matched volume + fingerprints (exit 1 if absent) |
| `vmc dji eject` | Safely eject the transmitter |
| `vmc dji watch enable\|disable\|status` | Optional launchd `WatchPaths` on `/Volumes` → `pull --if-present --quiet` on plug |

The watcher plist prefers `~/Applications/VMC.app/.../vmc` (or `vmc-service`) so Full Disk Access matches brew services. Run `vmc-grant-fda` first.

**Migrating from the bash `dji-mic` tool:** disable the old watcher before enabling vmc’s (`dji-mic watch disable`, then `vmc dji watch enable`). The tools use different lock paths and will race if both are armed.

See [adr/02-dji-source.md](../adr/02-dji-source.md).

## One-time: Full Disk Access (required)

launchd runs `/opt/homebrew/opt/vmc/bin/vmc`. That binary must have **Full Disk Access** or detect fails with `operation not permitted` on:

```
~/Library/Group Containers/group.com.apple.VoiceMemos.shared/Recordings/CloudRecordings.db
```

(or the classic `Application Support/com.apple.voicememos/...` path).

### Drag shortcut (recommended)

```bash
vmc-grant-fda
# or: make permissions
```

This builds **`~/Desktop/VMC.app`** (+ `~/Applications/VMC.app`), reveals it, opens **Full Disk Access**.

Then:

1. Drag **`VMC.app`** into the FDA list → toggle ON  
2. `brew services restart vmc`

`brew services` runs `vmc-service` → `~/Applications/VMC.app/Contents/Resources/vmc` (inside the app you granted). After upgrades, run `vmc-grant-fda` again to refresh the app binary.

## Install / upgrade

```bash
brew tap jborkowski/vmc https://github.com/jborkowski/voice-memories-curator
brew uninstall --ignore-dependencies vmc 2>/dev/null || true
brew install --HEAD --formula jborkowski/vmc/vmc
git xet install
vmc-grant-fda   # drag Desktop/vmc into FDA
brew services restart vmc
```

## Verify

```bash
vmc status
# Hub (private): must be logged in as dataset owner, or use API with token
tail -f ~/Library/Logs/vmc/vmc.log
```

Healthy detect: `detect phase completed` / `no new memos` / `wrote shard`.  
Broken FDA: `grant Full Disk Access` / `operation not permitted`.

## Manual upload (optional)

```bash
vmc upload          # missing shards only
vmc upload --force  # all ready shards
```
