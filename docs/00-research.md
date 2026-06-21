# Research: macOS Voice Memos → Hugging Face Dataset (Parquet)

**Date:** 2026-06-21
**Project:** voice-momories-curator
**Objective:** Verify whether anyone has already built a tool/process to extract macOS Voice Memos and publish them as a Hugging Face dataset in Parquet format.

---

## 1. Summary of Finding

**No existing end-to-end tool exists.** There are pieces — voice memo exporters, Parquet converters, HF upload scripts — but no single pipeline chains macOS Voice Memos extraction → structured audio Dataset → Hugging Face Parquet publishing. This project would fill that gap.

---

## 2. What Already Exists

### 2.1 macOS Voice Memos Exporters

These tools read the Voice Memos SQLite database and copy audio files to disk. None produce a Hugging Face–compatible dataset.

| Tool | Type | Source |
|------|------|--------|
| **macOSVoiceMemosExporter** (robbyHuelsi) | CLI Python | https://github.com/robbyHuelsi/macOSVoiceMemosExporter |
| **mac-os-voice-memos-exporter** (tessellated-studio) | CLI Python (fork) | https://github.com/tessellated-studio/mac-os-voice-memos-exporter |
| **voice-memos-exporter** (rudrakabir) | GUI Tkinter app | https://github.com/rudrakabir/voice-memos-exporter |
| **py-voice-memo-export** (bulletinmybeard) | Python tool | https://github.com/bulletinmybeard/py-voice-memo-export |

**Common mechanism:** Read `ZCLOUDRECORDING` table from `~/Library/Application Support/com.apple.voicememos/Recordings/CloudRecordings.db`.

**Key DB fields:**
- `ZDATE` — Apple epoch (seconds since 2001-01-01)
- `ZDURATION` — length in seconds
- `ZCUSTOMLABEL` — user-assigned title
- `ZPATH` — relative path to `.m4a` file
- `ZENCRYPTEDTITLE` — alternate/modified title

### 2.2 Audio → Parquet → Hugging Face Publishers

These tools take already-organized audio files + CSV metadata and produce Parquet datasets on HF. None read macOS databases.

| Tool | Purpose | Source |
|------|---------|--------|
| **ParquetToHuggingFace** (pr0mila) | Audio + CSV → Parquet → HF upload | https://github.com/pr0mila/ParquetToHuggingFace |
| **extract-audio** (egorsmkv) | Reverse: extract audio from HF Parquet | https://github.com/egorsmkv/extract-audio |

**Reference pattern (from ParquetToHuggingFace):**
```python
from datasets import Dataset, Audio, Features, Value
dataset = Dataset.from_list(samples, features=features)
dataset.to_parquet("output.parquet")
dataset.push_to_hub("username/dataset-name")
```

### 2.3 Existing HF Datasets of Personal Voice Notes

Very few. Only one relevant public dataset found.

- **danielrosehill/Voice-Note-Audio** — 101 MP3 voice notes with annotations and uncorrected transcripts. Not from macOS Voice Memos (manually collected). Not in Parquet format.
  - https://huggingface.co/datasets/danielrosehill/Voice-Note-Audio

No dataset on HF was found that was created from macOS Voice Memos exports.

### 2.4 Related HF Audio Datasets (for schema reference)

| Dataset | Notes |
|---------|-------|
| openslr/librispeech_asr | Standard ASR benchmark, 16kHz, transcribed |
| SilencioNetwork/complete-voiceai-speech-dataset | Multi-language, multi-emotion speech |
| freococo/kachin_asr_audio | Low-resource ASR example |

These are useful as **schema templates** (Audio feature, transcription columns) but are not personal/voice-memo datasets.

---

## 3. The Gap

```
macOS Voice Memos DB  ──?──▶  HF Dataset (Parquet)
       │                         │
       ▼                         ▼
  4 exporters exist       HF publish tools exist
  (file-copy only)        (assume CSV ready)
       │                         │
       └──── NO BRIDGE ──────────┘
```

No tool:
- Reads the macOS Voice Memos SQLite DB
- Extracts audio + metadata into a Dataset-compatible structure
- Accepts optional Whisper transcription
- Publishes as Parquet to Hugging Face Hub

That is exactly what the `voice-momories-curator` project needs to build.

---

## 4. Ingredients for the Solution

### 4.1 Database Access

```
~/Library/Application Support/com.apple.voicememos/Recordings/
├── CloudRecordings.db   (iCloud-synced, preferred)
└── Recordings.db        (local-only fallback)
```

Requires **Full Disk Access** permission (macOS privacy). The GUI exporter (rudrakabir) demonstrates the permission-granting UX pattern.

### 4.2 Schema for HF Dataset

```python
features = Features({
    "audio": Audio(sampling_rate=16000),
    "title": Value("string"),
    "created_at": Value("timestamp[ns]"),
    "duration_seconds": Value("float32"),
    "path": Value("string"),
    "transcription": Value("string"),   # optional
})
```

### 4.3 Technical Steps

1. **Connect** to `CloudRecordings.db` (SQLite)
2. **Query** `ZCLOUDRECORDING` for all records
3. **Convert** Apple epoch → standard datetime
4. **Copy** `.m4a` files from recording directory
5. **Decode** audio, resample to 16kHz (mono)
6. **Build** `Dataset` from list of samples
7. **(opt)** Run Whisper for local transcription
8. **Export** to Parquet shards via `dataset.to_parquet()`
9. **Push** to HF Hub via `dataset.push_to_hub()`

---

## 5. Open Questions

| Question | Status |
|----------|--------|
| Does the user want private or public dataset? | Configurable |
| Transcription needed (Whisper)? | Optional |
| What audio format in Parquet (embedded vs path)? | `Audio()` feature embeds decoded array |
| HF dataset naming convention? | Per user |
| macOS version differences in DB schema? | Needs testing |

---

## 6. Sources

- macOSVoiceMemosExporter: https://github.com/robbyHuelsi/macOSVoiceMemosExporter
- voice-memos-exporter (GUI): https://github.com/rudrakabir/voice-memos-exporter
- py-voice-memo-export: https://github.com/bulletinmybeard/py-voice-memo-export
- ParquetToHuggingFace: https://github.com/pr0mila/ParquetToHuggingFace
- extract-audio: https://github.com/egorsmkv/extract-audio
- Voice-Note-Audio (HF): https://huggingface.co/datasets/danielrosehill/Voice-Note-Audio
- HF Audio Datasets docs: https://huggingface.co/docs/datasets/audio_dataset
- HF Parquet datasets: https://huggingface.co/docs/hub/datasets-parquet
