#!/bin/bash
# Build VMC.app on Desktop + ~/Applications. Drag VMC.app into Full Disk Access.
# brew services runs scripts/vmc-service.sh → Resources/vmc inside this app.
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
  echo "vmc not found" >&2
  exit 1
}

build_app() {
  local dest="$1"
  local src="$2"
  rm -rf "${dest}"
  mkdir -p "${dest}/Contents/MacOS" "${dest}/Contents/Resources"
  cp -f "${src}" "${dest}/Contents/Resources/vmc"
  chmod +x "${dest}/Contents/Resources/vmc"
  cat > "${dest}/Contents/MacOS/VMC" <<'WRAP'
#!/bin/bash
exec "$(cd "$(dirname "$0")" && pwd)/../Resources/vmc" "$@"
WRAP
  chmod +x "${dest}/Contents/MacOS/VMC"
  cat > "${dest}/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>VMC</string>
  <key>CFBundleDisplayName</key><string>VMC</string>
  <key>CFBundleIdentifier</key><string>com.jborkowski.vmc</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>CFBundleExecutable</key><string>VMC</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
</dict>
</plist>
PLIST
  codesign --force --deep -s - "${dest}" 2>/dev/null || true
  xattr -cr "${dest}" 2>/dev/null || true
}

SRC="$(resolve_vmc)"
mkdir -p "${HOME}/Applications"
build_app "${HOME}/Desktop/VMC.app" "${SRC}"
build_app "${HOME}/Applications/VMC.app" "${SRC}"

open -R "${HOME}/Desktop/VMC.app"
open "x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_AllFiles"

cat <<EOF

Drag Desktop/VMC.app into Full Disk Access → toggle ON.
(Service uses ~/Applications/VMC.app/Contents/Resources/vmc — same binary.)

Then:
  brew services restart vmc

EOF
