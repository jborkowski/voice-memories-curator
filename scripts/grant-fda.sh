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

cp -f "${SRC}" "${DEST}"
chmod +x "${DEST}"
xattr -cr "${DEST}" 2>/dev/null || true

echo "Drag target: ${DEST}"
echo "Source:      ${SRC}"

open -R "${DEST}"
open "x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_AllFiles"

cat <<EOF

Full Disk Access is open.
1. Unlock the lock (bottom left) if needed
2. Drag Desktop/vmc into the list (or + → Desktop → vmc)
3. Ensure the toggle is ON
4. brew services restart vmc

EOF
