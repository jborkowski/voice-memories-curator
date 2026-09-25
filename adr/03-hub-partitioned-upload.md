# ADR-03: Partitioned Hub upload via Hugging Face API

**Status:** Accepted  
**Date:** 2026-09-25  
**Context:** Monolithic `git clone` + `git lfs pre-push` of many large `data/*.parquet` partitions hung for hours and re-uploaded shards already on Hub when `--force` skipped remote filtering. Consumers (lazy-notes, `datasets`) expect **partitioned Parquet** with embedded `Audio` structs — not AudioFolder.

---

## Decision

### 1. Keep partitioned Parquet + Audio feature

Hub layout remains `data/shard_*.parquet`. Columns `audio` / `audio_original` stay HF `Audio` (`bytes` + `path`). This preserves lazy-notes (`audio.bytes`) and enables:

```python
load_dataset("USER/repo", split="train", streaming=True)  # IterableDataset over partitions
```

### 2. Upload only missing partitions

Remote tree listing always filters by basename. `--force` / `--force-upload` ignore **upload_interval cadence only** — they never re-push partitions already on Hub.

### 3. Hub file API, small batches

Publish via `scripts/upload_hf_shards.py` (`huggingface_hub` `create_commit` / LFS-Xet). Default `upload_batch_size = 2`. No full-repo git clone for routine sync. Token via `HF_TOKEN` env to the script (not in git remotes).

### 4. Progress + verify

Log export / enrich / upload lines per batch; verify tree API sees new names before `MarkUploaded`.

---

## Consequences

- Faster, resumable publishes; orphaned multi-GB `vmc_repo_*` temps avoided.
- Empty republish of local `shard_0003`… when Hub already has them is impossible through normal flags.
- `git-xet` remains a formula dependency for ecosystem/Xet tooling but is not required on the upload hot path.
