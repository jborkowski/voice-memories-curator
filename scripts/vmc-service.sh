#!/bin/bash
# launchd entrypoint: run vmc daemon directly
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
if [[ -x "${HERE}/vmc" ]]; then
  exec "${HERE}/vmc" "$@"
fi

if command -v brew >/dev/null 2>&1; then
  PREF="$(brew --prefix vmc 2>/dev/null || true)"
  if [[ -n "${PREF}" && -x "${PREF}/bin/vmc" ]]; then
    exec "${PREF}/bin/vmc" "$@"
  fi
fi

if command -v vmc >/dev/null 2>&1; then
  exec "$(command -v vmc)" "$@"
fi

echo "vmc binary not found" >&2
exit 1
