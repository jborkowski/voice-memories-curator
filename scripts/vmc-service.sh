#!/bin/bash
# launchd entrypoint: prefer FDA-granted app bundle binary, else Cellar vmc.
set -euo pipefail

APP_BIN="${HOME}/Applications/VMC.app/Contents/Resources/vmc"
if [[ -x "${APP_BIN}" ]]; then
  exec "${APP_BIN}" "$@"
fi

DESK_BIN="${HOME}/Desktop/VMC.app/Contents/Resources/vmc"
if [[ -x "${DESK_BIN}" ]]; then
  exec "${DESK_BIN}" "$@"
fi

HERE="$(cd "$(dirname "$0")" && pwd)"
if [[ -x "${HERE}/vmc" ]]; then
  exec "${HERE}/vmc" "$@"
fi

echo "vmc binary not found" >&2
exit 1
