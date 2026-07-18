# Implementation Plan

**Status:** Phase F (complete) — all phases done  
**Date:** 2026-06-21  
**Design:** [ADR-00](../adr/00-initial.md), [ADR-01](../adr/01-local-state-db.md)

Pipeline state lives in Parquet shards (`audio IS NULL` = unprocessed; row on HF = synced). No separate state DB.

---

## Phases

### Phase A: Project Scaffold & Build

**Goal:** Go binary with DuckDB embedded and CLI skeleton.

**Deliverable:** `vmc` binary that prints help for all subcommands.

**Acceptance criteria:**
- `CGO_ENABLED=1 go build` produces a working `darwin/arm64` binary
- `vmc --help` shows detect, process, upload, daemon, status, logs, sync-now
- Config loads from `~/.config/vmc/config.toml` (or defaults)
- DuckDB opens and runs a trivial query without error

**Risks:**
- CGO + DuckDB linking on arm64
- go-duckdb version compatibility with sqlite_scanner

---

### Phase B: Detect

**Goal:** Read Apple's Voice Memos DB and append metadata-only Parquet shards.

**Deliverable:** `vmc detect` writes `~/.local/share/vmc/shards/shard_NNNN.parquet` with memo metadata.

**Acceptance criteria:**
- Attaches Apple SQLite via DuckDB sqlite_scanner (read-only)
- Maps Apple epoch → RFC3339
- Writes rows with `audio = NULL`
- Deduplicates against local shards; when online, also against HF remote Parquet
- Offline: skips HF query, local-only dedup (may re-detect; upload phase deduplicates)
- Idempotent: running twice doesn't create duplicate rows

**Risks:**
- Full Disk Access TCC — clear error if denied
- Apple DB schema differences across macOS versions
- `ZPATH` → filesystem path mapping needs validation

---

### Phase C: Process

**Goal:** Fill the audio column via ffmpeg transcode.

**Deliverable:** `vmc process` updates shard rows from `audio = NULL` to FLAC blobs.

**Acceptance criteria:**
- Finds rows with `audio IS NULL` in local shards
- ffmpeg: `.m4a` → FLAC 16kHz mono
- Atomic Parquet write (temp file + rename)
- Missing `.m4a` or ffmpeg failure: log warning, leave `audio` NULL, don't corrupt shard

**Risks:**
- Large files → memory pressure embedding blobs in Parquet
- ffmpeg not installed → clear error (Homebrew formula declares the dependency)

---

### Phase D: Upload

**Goal:** Push complete shards to Hugging Face.

**Deliverable:** `vmc upload` commits a shard to the HF dataset repo and deletes the local copy.

**Acceptance criteria:**
- Connectivity check before upload; offline exits with "N shards ready"
- Uploads shards where all rows have non-NULL audio
- Column-selects for HF schema (drops internal fields like `audio_path`)
- Pushes as `data/shard_NNNN.parquet`
- Deletes local shard on success (unless `keep_uploaded_shards`)
- Partial failure: shard stays local for retry

**Risks:**
- HF API rate limits or token auth
- Large shard upload timeout

---

### Phase E: Homebrew & Operations

**Goal:** Distribute via personal Homebrew tap; wire daemon and observability.

**Deliverable:** `brew install <tap>/vmc && brew services start vmc` runs the pipeline hourly.

**Acceptance criteria:**
- Homebrew formula: `depends_on "ffmpeg"`, `service` block with `StartInterval: 3600`
- `vmc daemon` runs detect → process → upload in sequence
- `vmc status` reports counts from shards (NULL audio = pending, complete shards = ready)
- Structured JSON logs to `~/Library/Logs/vmc/vmc.log` with rotation
- `vmc logs` tails the log file

**Risks:**
- Formula `service` block plist paths on Apple Silicon vs Intel

---

### Phase F: Integration Testing

**Goal:** Validate the pipeline end-to-end without a real Apple DB.

**Deliverable:** Test harness with synthetic SQLite + sample audio.

**Acceptance criteria:**
- Synthetic Apple DB fixture exercises detect → process → upload flow
- Offline detect + online upload dedup scenario covered
- Crash/resume: interrupted process leaves `audio` NULL, next run picks up

**Risks:**
- Simulating Apple DB schema faithfully enough for CI

---

## Open Questions

| Question | Phase | Status |
|----------|-------|--------|
| Exact `ZPATH` → filesystem path mapping | B | Resolved — `ZPATH` is relative to the Recordings dir (parent of `CloudRecordings.db`). Tests use this convention for synthetic fixtures. |
| Apple DB schema differences across macOS versions | B | Resolved — detect code introspects `information_schema.columns` and adapts. Tests use the minimal schema the detect code actually queries. |
| Max shard size before splitting | B/C | Resolved — tests use small shards (3–5 rows). Production can default to unbounded (one shard per detect pass). |
| Memory strategy for large FLAC blobs in Parquet | C | Resolved — not relevant for small test fixtures. Production shards stay small due to per-pass batching. |
| HF upload mechanism: git-based or API-based? | D | Resolved — git-xet batch clone/commit/push; Viewer metadata via `fix_hf_parquet.py` when available. |
