#!/usr/bin/env python3
# /// script
# requires-python = ">=3.10"
# dependencies = [
#     "huggingface_hub>=0.23.0",
# ]
# ///
"""Upload partitioned parquet shards to a HF dataset via the Hub API.

Avoids git clone + git-lfs pre-push of the full repo. Auth via HF_TOKEN env
(never print the token). Large files go through Hub LFS/Xet automatically.
"""

from __future__ import annotations

import argparse
import os
import sys
from pathlib import Path

from huggingface_hub import CommitOperationAdd, HfApi, create_repo


def main() -> int:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--repo", required=True, help="dataset id USER/NAME")
    p.add_argument("--private", action="store_true", help="create private if missing")
    p.add_argument("--message", default="", help="commit message")
    p.add_argument("--readme", default="", help="optional local README.md to include")
    p.add_argument("files", nargs="+", help="local .parquet paths")
    args = p.parse_args()

    token = os.environ.get("HF_TOKEN", "").strip()
    if not token:
        print("error: HF_TOKEN env is required", file=sys.stderr)
        return 2

    api = HfApi(token=token)
    try:
        create_repo(
            args.repo,
            repo_type="dataset",
            private=args.private,
            exist_ok=True,
            token=token,
        )
    except Exception as exc:  # noqa: BLE001 — surface Hub errors to vmc
        print(f"create_repo warn: {exc}", file=sys.stderr, flush=True)

    ops: list[CommitOperationAdd] = []
    names: list[str] = []
    for raw in args.files:
        path = Path(raw)
        if not path.is_file():
            print(f"error: missing file {path}", file=sys.stderr)
            return 2
        size = path.stat().st_size
        print(f"stage {path.name} ({size} bytes) → data/{path.name}", flush=True)
        ops.append(
            CommitOperationAdd(
                path_in_repo=f"data/{path.name}",
                path_or_fileobj=str(path.resolve()),
            )
        )
        names.append(path.name)

    if args.readme:
        readme = Path(args.readme)
        if not readme.is_file():
            print(f"error: missing README {readme}", file=sys.stderr)
            return 2
        ops.append(
            CommitOperationAdd(
                path_in_repo="README.md",
                path_or_fileobj=str(readme.resolve()),
            )
        )

    msg = args.message.strip() or f"Upload {len(names)} shard(s): {', '.join(names)}"
    print(f"create_commit: {msg}", flush=True)
    api.create_commit(
        repo_id=args.repo,
        repo_type="dataset",
        operations=ops,
        commit_message=msg,
    )
    print("ok", flush=True)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
