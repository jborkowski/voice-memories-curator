#!/bin/bash
set -euo pipefail

resolve_vmc() {
  if command -v brew >/dev/null 2>&1; then
    local pref
    pref="$(brew --prefix vmc 2>/dev/null || true)"
    if [[ -n "${pref}" && -x "${pref}/bin/vmc" ]]; then
      echo "${pref}/bin/vmc"
      return
    fi
  fi
  if [[ -x "${HOME}/.local/bin/vmc" ]]; then
    echo "${HOME}/.local/bin/vmc"
    return
  fi
  if command -v vmc >/dev/null 2>&1; then
    command -v vmc
    return
  fi
  echo "vmc binary not found" >&2
  exit 1
}

SRC="$(resolve_vmc)"
echo "vmc binary: ${SRC}"

open -R "${SRC}"
open "x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_AllFiles" 2>/dev/null || \
open "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles" 2>/dev/null || true

cat <<EOF

Full Disk Access settings opened.
Add '${SRC}' to Full Disk Access (or drag it from Finder) and toggle ON.

Then restart the service:
  brew services restart vmc
EOF
