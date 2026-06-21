#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = [
#     "pyarrow",
#     "datasets",
#     "huggingface_hub",
# ]
# ///
"""
Fix audio dataset Parquet files for Hugging Face Dataset Viewer.

DuckDB writes struct<bytes, path> columns correctly, but omits the HF
`datasets` library metadata from the Parquet footer.  The Dataset Viewer
uses that metadata to know a column should render an audio player.

This script rewrites each shard through the `datasets` library so the
footer contains the correct feature descriptors.

Usage:
    # Download, fix, and re-upload in one shot
    uv run scripts/fix_hf_parquet.py

    # Fix local shards only (skip download/upload)
    uv run scripts/fix_hf_parquet.py --local ./downloaded_shards -o ./fixed_shards
"""

import argparse
import glob
import os
import subprocess
import sys
import tempfile
from pathlib import Path

import pyarrow as pa
import pyarrow.parquet as pq
from datasets import Audio, Dataset

HF_REPO = "j14i/voice-memories"
AUDIO_COLUMNS = ("audio", "audio_original")


def fix_parquet_shard(path: str, out_dir: str) -> None:
    table = pq.read_table(path)
    schema = table.schema
    print(f"\n--- {Path(path).name} ---")
    print(f"  Columns: {schema.names}")

    new_columns = []
    for i, name in enumerate(schema.names):
        col = table.column(i).combine_chunks()
        field = schema.field(i)

        if name in AUDIO_COLUMNS:
            if pa.types.is_binary(field.type) or pa.types.is_large_binary(field.type):
                path_array = pa.array([""] * len(col), type=pa.string())
                col = pa.StructArray.from_arrays(
                    [col, path_array],
                    fields=[
                        pa.field("bytes", pa.binary()),
                        pa.field("path", pa.string()),
                    ],
                )
                print(f"  {name}: binary -> struct<bytes, path>")
            elif pa.types.is_struct(field.type):
                print(f"  {name}: already struct, adding HF metadata")
            else:
                print(f"  {name}: unexpected type {field.type}, skipping conversion")

        new_columns.append(col)

    new_table = pa.Table.from_arrays(new_columns, names=schema.names)

    ds = Dataset(new_table)
    for col_name in AUDIO_COLUMNS:
        if col_name in ds.column_names:
            ds = ds.cast_column(col_name, Audio())

    out_path = Path(out_dir) / Path(path).name
    out_path.parent.mkdir(parents=True, exist_ok=True)
    ds.to_parquet(str(out_path))
    print(f"  Wrote: {out_path}")


def run(cmd: list[str], **kwargs) -> None:
    print(f"$ {' '.join(cmd)}")
    subprocess.run(cmd, check=True, **kwargs)


def main() -> None:
    parser = argparse.ArgumentParser(
        description="Rewrite Parquet shards with HF datasets audio metadata"
    )
    parser.add_argument(
        "--local",
        help="Local directory or glob pattern instead of downloading from HF",
    )
    parser.add_argument(
        "-o", "--output", help="Output directory for fixed shards (default: temp dir)"
    )
    args = parser.parse_args()

    if args.local:
        input_dir = args.local
        out_dir = args.output or str(Path(input_dir).parent / "fixed")
        if os.path.isdir(input_dir):
            pattern = os.path.join(input_dir, "*.parquet")
        else:
            pattern = input_dir
        files = sorted(glob.glob(pattern))
        if not files:
            print(f"No files matched: {pattern}", file=sys.stderr)
            sys.exit(1)
        print(f"Found {len(files)} shard(s)")
        for f in files:
            fix_parquet_shard(f, out_dir)
        print(f"\nDone! Fixed shards in: {out_dir}")
        print(
            f"Upload with:\n"
            f"  hf upload {HF_REPO} {out_dir} data/ --type dataset"
        )
        return

    with tempfile.TemporaryDirectory(prefix="vmc_fix_") as tmp:
        dl_dir = os.path.join(tmp, "download")
        fix_dir = args.output or os.path.join(tmp, "fixed")

        print(f"Downloading shards from {HF_REPO}...")
        run([
            "hf", "download", HF_REPO,
            "--type", "dataset",
            "--include", "data/*.parquet",
            "--local-dir", dl_dir,
        ])

        data_dir = os.path.join(dl_dir, "data")
        files = sorted(glob.glob(os.path.join(data_dir, "*.parquet")))
        if not files:
            print("No parquet shards found in downloaded repo.", file=sys.stderr)
            sys.exit(1)

        print(f"\nFound {len(files)} shard(s), fixing...")
        for f in files:
            fix_parquet_shard(f, fix_dir)

        print(f"\nUploading fixed shards to {HF_REPO}...")
        run([
            "hf", "upload", HF_REPO,
            fix_dir, "data/",
            "--type", "dataset",
        ])

        print("\nDone! Dataset Viewer should now show audio players.")


if __name__ == "__main__":
    main()
