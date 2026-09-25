# ADR-02: DJI Mic as a Second Source (Single Daemon, Feature-Flagged)

**Status:** Accepted  
**Date:** 2026-09-25  
**Context:** DJI Mic recordings that land in a local inbox directory should reach the same Hugging Face dataset as Voice Memos, without a second ingest daemon. Operator-specific paths and device fingerprints must stay in local config, not in public defaults.

---

## Decision

### 1. One daemon

The inbox → Hub path lives inside vmc's existing `brew services` job. On each hourly tick, when `[dji] enabled` and `root` is set, the daemon may `pull --if-present` first, then `detect` scans the configured inbox alongside Apple's `CloudRecordings.db`.

```
vmc dji pull → local inbox/*.WAV → detect → process → upload → hf_repo data/*.parquet
```

An optional launchd `WatchPaths` agent (`vmc dji watch`) pulls on mount; it is not required for hourly ingest. Disable any prior bash `dji-mic` watcher before enabling it.

### 2. Feature flag + empty public defaults

`[dji] enabled = false` by default. `root`, `media_name`, `volume_uuid`, `usb_vendor`, `usb_product`, and similar fingerprints default to **empty**. Operators set them only in `~/.config/vmc/config.toml` (not committed). Safe structural defaults (e.g. `dir_glob`, `min_age_seconds`) may ship in code.

### 3. Pull vs push config

| Concern | Where |
|---------|--------|
| Where to **pull** from | Local `[dji]` (`root`, device ids, …) |
| Where to **push** to | Top-level `hf_repo` / `hf_token` / `hf_private` |

```toml
hf_repo = "YOUR_USER/voice-memories"
hf_private = true

[dji]
enabled = false
root = "~/path/to/your/dji-inbox"
dir_glob = "????-??-??-??-??/DJI_Audio_*"
device_label = "DJI Mic"
min_age_seconds = 120
```

### 4. Same Hub layout and schema

DJI rows go into `data/*.parquet` (unchanged schema). `device` uses `device_label` when set. Title / transcription / location stay NULL. Dedup on `(device, created_at, duration_seconds)`; fail closed if remote listing fails.

### 5. Process

Non-`.m4a`/`.qta` sources: `audio` = 16 kHz mono FLAC; `audio_original` = AAC `.m4a`. Skip transcript extraction.

### 6. Recording IDs

Slot IDs so new Apple + DJI rows stay above lazy-notes' watermark (see implementation in `internal/detect/ids.go`).

---

## Consequences

- Public repo does not embed personal iCloud paths or device UUIDs.
- Enabling DJI without setting `root` fails clearly until local config is filled.
- Turning DJI off after slot IDs exist needs care (see QA notes on persisting “slots in use”).
