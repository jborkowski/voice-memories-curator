# VMC ops flow (Solmigo / brew services)

## Pipeline

```
brew services (hourly)
  → vmc daemon
      → detect   (read Voice Memos DB → local parquet shards)
      → process  (ffmpeg → FLAC in shards)
      → upload   (push only shards missing from Hugging Face)
```

- **Detect/process:** every hour (`Formula` `interval 3600`).
- **Upload:** automatic when local ready shards are not on Hub. No `--force` for normal use.
- **State:** `~/.local/share/vmc/shards/`, `vmc.db`, `vmc.lock`.
- **Config:** `~/.config/vmc/config.toml` (`hf_token`, `hf_repo`, `apple_db_path`, …).

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

This:

1. Resolves the real Cellar binary
2. Copies it to `~/Desktop/vmc` (visible drag target)
3. Reveals it in Finder
4. Opens **System Settings → Privacy & Security → Full Disk Access**

Then: **drag `Desktop/vmc` into the FDA list** → toggle ON →:

```bash
brew services restart vmc
```

After `brew reinstall` / new HEAD, run `vmc-grant-fda` again if FDA breaks (binary path under Cellar changes).

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
