# ADR-01: Parquet-as-State Pipeline Tracking

**Status:** Accepted  
**Date:** 2026-06-21  
**Context:** The `vmc` daemon runs three independent phases (detect, process, upload). Rather than maintaining a separate state database, the Parquet shards themselves encode pipeline progress — a row with `audio IS NULL` hasn't been processed yet, and a shard that still exists locally hasn't been uploaded yet.

---

## Decision

Eliminate the separate `sync_state` DuckDB table. Use local Parquet shards and the HF remote dataset as the sole sources of truth for pipeline state.

Each phase derives its work list from the data that already exists:

| Question | Answer |
|----------|--------|
| "Has this memo been detected?" | It has a row in a local shard or in HF |
| "Has this memo been processed?" | Its `audio` column is non-NULL |
| "Has this memo been uploaded?" | It exists in `hf://datasets/{user}/{repo}/data/*.parquet` |

No status column. No state file. No schema migrations.

## Phase 1: Detect — "What's new?"

Query Apple's DB, subtract everything already known (on HF and in local shards).

```sql
ATTACH '~/Library/.../CloudRecordings.db' AS apple (TYPE sqlite, READ_ONLY);

-- What's already on HF?
CREATE TEMP TABLE uploaded AS
  SELECT recording_id FROM 'hf://datasets/{user}/{repo}/data/*.parquet';

-- What's already in local shards?
CREATE TEMP TABLE local_pending AS
  SELECT recording_id FROM '~/.local/share/vmc/shards/*.parquet';

-- New memos = in Apple DB but not in either place
SELECT * FROM apple.ZCLOUDRECORDING
WHERE Z_PK NOT IN (SELECT recording_id FROM uploaded)
  AND Z_PK NOT IN (SELECT recording_id FROM local_pending);
```

Write metadata-only rows (with `audio = NULL`) to the current local shard.

## Phase 2: Process — "Which rows need audio?"

Query local shards for rows missing audio and fill them via ffmpeg.

```sql
SELECT recording_id, audio_path
FROM '~/.local/share/vmc/shards/*.parquet'
WHERE audio IS NULL;
```

For each: transcode `.m4a` → FLAC 16kHz mono via ffmpeg, write the FLAC blob into the shard's `audio` column. No status update needed — the presence of audio data *is* the state.

## Phase 3: Upload — "Which shards are complete?"

A shard is ready when none of its rows have NULL audio.

```sql
SELECT filename
FROM glob('~/.local/share/vmc/shards/*.parquet')
WHERE filename NOT IN (
  SELECT filename FROM '~/.local/share/vmc/shards/*.parquet' WHERE audio IS NULL
);
```

Upload with column selection (only HF-destined columns, excluding internal fields like `audio_path`):

```sql
COPY (
  SELECT recording_id, audio, title, created_at, duration_seconds,
         transcription, latitude, longitude, place_name, device, folder
  FROM 'shard_NNNN.parquet'
) TO 'upload_shard_NNNN.parquet' (FORMAT PARQUET);
```

Push to HF, then delete the local shard.

## Offline Handling

| Phase | Offline? | Behavior |
|-------|----------|----------|
| Detect | partial | Skips the HF query, checks only against local shards. May re-detect items already uploaded; phase 3 deduplicates before uploading. |
| Process | works | Fully local — reads `.m4a` files and writes to shards. |
| Upload | skips | Needs connectivity. Logs "N shards ready", exits. |

## Crash Safety

Parquet writes are atomic (write to temp file, rename on success). Each phase is safe to interrupt:

- **Phase 1 crashes mid-write:** The temp shard is never renamed, so nothing changes. Next run re-detects.
- **Phase 2 / ffmpeg crashes:** `audio` stays NULL for that row. Next run picks it up.
- **Phase 3 / upload fails:** Shard stays local. Next run retries.

Same crash-safety guarantees as a dedicated state DB, with zero extra machinery.

## What Is NOT Stored Locally

- No separate state database or tracking table
- No status column or content hashes
- Audio bytes exist in local shards only transiently (between phase 2 and phase 3), then permanently on HF

## Consequences

**Pros:**
- Fewer moving parts — no state schema to define, migrate, or recover
- Pipeline state is implicit in the data, not duplicated alongside it
- Each phase still queries exactly what it needs (same DuckDB queries, just against shards/HF instead of a state table)
- Phases remain decoupled — run independently, in any order, at any frequency
- Crash-safe via atomic Parquet writes
- `DROP` + re-detect is trivially just "delete local shards" (anything already on HF is fine)

**Cons:**
- Phase 1 requires an HF network query to know what's already uploaded (offline mode falls back to local-only detection with possible re-detection)
- No local record of HF commit SHAs (acceptable: HF's own git history serves this purpose)
