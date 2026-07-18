#!/bin/bash
# Put a drag-target `vmc` on the Desktop and open Full Disk Access.
# One gesture: drag Desktop/vmc onto the FDA list (+ or drop).
set -euo pipefail

resolve_vmc() {
  if command -v brew >/dev/null 2>&1; then
    local pref
    pref="$(brew --prefix vmc 2>/dev/null || true)"
    if [[ -n "${pref}" && -x "${pref}/bin/vmc" ]]; then
      python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "${pref}/bin/vmc"
      return
    fi
  fi
  if [[ -x "${HOME}/.local/bin/vmc" ]]; then
    python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "${HOME}/.local/bin/vmc"
    return
  fi
  if command -v vmc >/dev/null 2>&1; then
    python3 -c 'import os,sys; print(os.path.realpath(sys.argv[1]))' "$(command -v vmc)"
    return
  fi
  echo "vmc binary not found (brew install --HEAD vmc or make install)" >&2
  exit 1
}

SRC="$(resolve_vmc)"
DEST="${HOME}/Desktop/vmc"

# Hardlink = same inode as the brew/launchd binary (a copy would get FDA on the
# Desktop path only, and brew services would still be denied).
rm -f "${DEST}"
if ! ln "${SRC}" "${DEST}" 2>/dev/null; then
  # Cross-volume fallback: reveal the real binary instead of copying.
  DEST="${SRC}"
fi

echo "Drag THIS file into Full Disk Access:"
echo "  ${DEST}"
echo "launchd binary:"
echo "  ${SRC}"

open -R "${DEST}"
open "x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_AllFiles"

cat <<EOF

Full Disk Access is open.
1. Unlock (bottom left) if needed
2. Drag the highlighted vmc into the list
3. Toggle ON
4. brew services restart vmc

EOF
