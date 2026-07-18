#!/bin/bash
set -euo pipefail

PREFIX="$(brew --prefix vmc 2>/dev/null || true)"
CANDIDATES=(
  "$(cd "$(dirname "$0")" && pwd)/make-fda-app.sh"
  "${PREFIX}/share/vmc/make-fda-app.sh"
  "/opt/homebrew/share/vmc/make-fda-app.sh"
)

for s in "${CANDIDATES[@]}"; do
  if [[ -n "${s}" && -f "${s}" ]]; then
    exec bash "${s}"
  fi
done

echo "make-fda-app.sh not found — reinstall: brew install --HEAD vmc" >&2
exit 1
