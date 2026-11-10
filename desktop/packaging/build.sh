#!/bin/bash
# Builds the desktop app (desktop/app: Tauri shell + the Go sync engine as
# its sidecar) and the headless engine into dist/ (of the repository):
#   desktop/packaging/build.sh app      the app for this system (Linux: .deb and
#                                       .AppImage; Windows: setup .exe and .msi;
#                                       macOS: universal .dmg and the updater .app.tar.gz)
#   desktop/packaging/build.sh cli      the headless engine alone: Linux .tar.gz
#                                       (amd64, arm64) and Windows .zip, cross-compiled here
#   desktop/packaging/build.sh latest-json   latest.json of the Tauri updater, from the
#                                       .sig files in dist/ (release job)
# Files: strataos-sync-<version>-<system>-<arch>.<ext> ("~" becomes "-", as for
# releases). The updater signatures (.sig) are made when TAURI_SIGNING_PRIVATE_KEY
# (and its _PASSWORD) are set; without them the app is built without them.
set -euo pipefail
umask 022
cd "$(dirname "$0")/.."
ROOT=$(cd .. && pwd)
V=$(cat "$ROOT/VERSION")
FV=${V//\~/-}
MSIV=${V%%~*} # MSI versions are numbers only: 0.8.0~beta.1 is 0.8.0
DIST="$ROOT/dist"
B="$ROOT/build/desktop"
mkdir -p "$DIST" "$B"
LDFLAGS="-s -w -X main.version=$V"

copy() { # copy SRC NAME: the file and its updater signature
  cp "$1" "$DIST/$2"
  if [ -f "$1.sig" ]; then cp "$1.sig" "$DIST/$2.sig"; fi
}

app() {
  local os triple bundles conf sign=false
  case "$(uname -s)" in
    Linux) os=linux; triple=$(rustc -vV | sed -n 's/^host: //p'); bundles=deb,appimage ;;
    Darwin) os=macos; triple=universal-apple-darwin; bundles=app,dmg ;;
    MINGW* | MSYS* | CYGWIN*) os=windows; triple=x86_64-pc-windows-msvc; bundles=nsis,msi ;;
    *) echo "unknown system" >&2; exit 2 ;;
  esac
  if [ -n "${TAURI_SIGNING_PRIVATE_KEY:-}" ]; then sign=true; fi
  conf=$(printf '{"version":"%s","bundle":{"createUpdaterArtifacts":%s,"windows":{"wix":{"version":"%s"}}}}' "$FV" "$sign" "$MSIV")
  if [ "$triple" = universal-apple-darwin ]; then
    # Tauri looks for the sidecar of each architecture it builds, and the universal one.
    app/scripts/sidecar.sh aarch64-apple-darwin >/dev/null
    app/scripts/sidecar.sh x86_64-apple-darwin >/dev/null
  fi
  app/scripts/sidecar.sh "$triple" >/dev/null
  (cd app && npm ci --no-audit --no-fund && npx tauri build --ci --bundles "$bundles" --target "$triple" --config "$conf")
  local out="app/src-tauri/target/$triple/release/bundle"
  case $os in
    linux)
      local a=amd64
      if [ "${triple%%-*}" = aarch64 ]; then a=arm64; fi
      copy "$(ls "$out"/deb/*.deb)" "strataos-sync-$FV-linux-$a.deb"
      copy "$(ls "$out"/appimage/*.AppImage)" "strataos-sync-$FV-linux-$a.AppImage"
      ;;
    windows)
      copy "$(ls "$out"/nsis/*-setup.exe)" "strataos-sync-$FV-windows-x64-setup.exe"
      copy "$(ls "$out"/msi/*.msi)" "strataos-sync-$FV-windows-x64.msi"
      ;;
    macos)
      copy "$(ls "$out"/dmg/*.dmg)" "strataos-sync-$FV-macos.dmg"
      if ls "$out"/macos/*.app.tar.gz >/dev/null 2>&1; then copy "$(ls "$out"/macos/*.app.tar.gz)" "strataos-sync-$FV-macos.app.tar.gz"; fi
      ;;
  esac
}

cli() {
  local t="$B/cli/strataos-sync-$FV" a
  for a in amd64 arm64; do
    rm -rf "$B/cli" && mkdir -p "$t"
    CGO_ENABLED=0 GOOS=linux GOARCH=$a go build -trimpath -ldflags "$LDFLAGS" -o "$t/strataos-sync" ./cmd/strataos-sync
    cp packaging/README.txt "$t/"
    tar -C "$B/cli" -czf "$DIST/strataos-sync-$FV-linux-$a.tar.gz" "strataos-sync-$FV"
  done
  local w="$B/cli-windows"
  rm -rf "$w" && mkdir -p "$w"
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS" -o "$w/strataos-sync.exe" ./cmd/strataos-sync
  cp packaging/README.txt "$w/README.txt"
  rm -f "$DIST/strataos-sync-$FV-windows-x64.zip"
  if command -v zip >/dev/null; then
    (cd "$w" && zip -q -9 "$DIST/strataos-sync-$FV-windows-x64.zip" strataos-sync.exe README.txt)
  else # Git Bash on Windows
    powershell -NoProfile -Command "Compress-Archive -Path '$(cygpath -w "$w")\strataos-sync.exe','$(cygpath -w "$w")\README.txt' -DestinationPath '$(cygpath -w "$DIST")\strataos-sync-$FV-windows-x64.zip'"
  fi
}

# latest.json: for each system, the package the updater installs and its
# signature ("linux-x86_64" the AppImage, "linux-x86_64-deb" the .deb,
# "windows-x86_64" the setup, "windows-x86_64-msi", "darwin-*" the universal
# .app.tar.gz). The updater looks for <os>-<arch>-<installer> first.
latest_json() {
  local url="https://github.com/Nathplay15/StrataOS/releases/download/v$FV" entries=()
  add() { # add KEY FILE
    if [ -f "$DIST/$2.sig" ]; then
      entries+=("$(printf '"%s":{"signature":"%s","url":"%s/%s"}' "$1" "$(tr -d '\n' < "$DIST/$2.sig")" "$url" "$2")")
    fi
  }
  add linux-x86_64 "strataos-sync-$FV-linux-amd64.AppImage"
  add linux-x86_64-appimage "strataos-sync-$FV-linux-amd64.AppImage"
  add linux-x86_64-deb "strataos-sync-$FV-linux-amd64.deb"
  add linux-aarch64 "strataos-sync-$FV-linux-arm64.AppImage"
  add linux-aarch64-appimage "strataos-sync-$FV-linux-arm64.AppImage"
  add linux-aarch64-deb "strataos-sync-$FV-linux-arm64.deb"
  add windows-x86_64 "strataos-sync-$FV-windows-x64-setup.exe"
  add windows-x86_64-nsis "strataos-sync-$FV-windows-x64-setup.exe"
  add windows-x86_64-msi "strataos-sync-$FV-windows-x64.msi"
  add darwin-aarch64 "strataos-sync-$FV-macos.app.tar.gz"
  add darwin-x86_64 "strataos-sync-$FV-macos.app.tar.gz"
  if [ ${#entries[@]} -eq 0 ]; then
    echo "no updater signatures in dist/: latest.json not written" >&2
    return 0
  fi
  local IFS=,
  printf '{"version":"%s","notes":"https://github.com/Nathplay15/StrataOS/releases/tag/v%s","pub_date":"%s","platforms":{%s}}\n' \
    "$FV" "$FV" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${entries[*]}" > "$DIST/latest.json"
  cat "$DIST/latest.json"
}

case "${1:-}" in
app) app ;;
cli) cli ;;
latest-json) latest_json ;;
*)
  echo "usage: $0 app|cli|latest-json" >&2
  exit 2
  ;;
esac
ls -l "$DIST"/strataos-sync-"$FV"-* 2>/dev/null || true
