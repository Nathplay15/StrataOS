#!/bin/bash
# Builds the StrataOS Sync packages into dist/ (of the repository):
#   desktop/packaging/build.sh linux     .deb (amd64, arm64) and .tar.gz (amd64), here (no cgo)
#   desktop/packaging/build.sh windows   portable .zip here; the .msi on Windows (WiX, see windows/)
#   desktop/packaging/build.sh macos     universal .app in a .dmg, on macOS only (the menu bar icon needs cgo)
# Version: the VERSION file ("~" becomes "-" in file names, as for releases).
set -euo pipefail
umask 022
cd "$(dirname "$0")/.."
ROOT=$(cd .. && pwd)
V=$(cat "$ROOT/VERSION")
FV=${V//\~/-}
DIST="$ROOT/dist"
B="$ROOT/build/desktop"
mkdir -p "$DIST" "$B"
LDFLAGS="-s -w -X main.version=$V"
go run ./cmd/mkicons "$B/icons"

linux_bin() { CGO_ENABLED=0 GOOS=linux GOARCH=$1 go build -trimpath -ldflags "$LDFLAGS" -o "$B/linux-$1/strataos-sync" ./cmd/strataos-sync; }

deb() { # deb ARCH
  local a=$1 root="$B/deb-$1"
  rm -rf "$root"
  mkdir -p "$root/DEBIAN" "$root/usr/bin" "$root/usr/share/applications" "$root/usr/share/icons/hicolor/256x256/apps" "$root/usr/share/doc/strataos-sync"
  cp "$B/linux-$a/strataos-sync" "$root/usr/bin/"
  cp packaging/linux/strataos-sync.desktop "$root/usr/share/applications/"
  cp "$B/icons/icon.png" "$root/usr/share/icons/hicolor/256x256/apps/strataos-sync.png"
  cp packaging/README.txt "$root/usr/share/doc/strataos-sync/"
  cat > "$root/DEBIAN/control" <<CTRL
Package: strataos-sync
Version: $V
Architecture: $a
Maintainer: StrataOS <noreply@github.com>
Section: net
Priority: optional
Recommends: zenity
Homepage: https://github.com/Nathplay15/StrataOS
Description: two-way folder synchronization with a StrataOS NAS
 StrataOS Sync keeps folders of this computer and of a StrataOS NAS
 identical, in both directions, in the background (tray icon, settings
 page, and a headless mode: strataos-sync --help).
CTRL
  dpkg-deb --root-owner-group --build "$root" "$DIST/strataos-sync-$FV-linux-$a.deb" >/dev/null
}

case "${1:-}" in
linux)
  for a in amd64 arm64; do linux_bin "$a"; deb "$a"; done
  t="$B/tar/strataos-sync-$FV"
  rm -rf "$B/tar" && mkdir -p "$t"
  cp "$B/linux-amd64/strataos-sync" packaging/linux/strataos-sync.desktop packaging/README.txt "$t/"
  cp "$B/icons/icon.png" "$t/strataos-sync.png"
  tar -C "$B/tar" -czf "$DIST/strataos-sync-$FV-linux-amd64.tar.gz" "strataos-sync-$FV"
  ;;
windows)
  w="$B/windows"
  mkdir -p "$w"
  CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$LDFLAGS -H=windowsgui" -o "$w/strataos-sync.exe" ./cmd/strataos-sync
  cp "$B/icons/icon.ico" "$w/strataos-sync.ico"
  cp packaging/README.txt "$w/README.txt"
  rm -f "$DIST/strataos-sync-$FV-windows-x64.zip"
  if command -v zip >/dev/null; then
    (cd "$w" && zip -q -9 "$DIST/strataos-sync-$FV-windows-x64.zip" strataos-sync.exe README.txt)
  else # Git Bash on Windows
    powershell -NoProfile -Command "Compress-Archive -Path '$(cygpath -w "$w")\strataos-sync.exe','$(cygpath -w "$w")\README.txt' -DestinationPath '$(cygpath -w "$DIST")\strataos-sync-$FV-windows-x64.zip'"
  fi
  ;;
macos)
  m="$B/macos"
  rm -rf "$m" && mkdir -p "$m"
  for a in amd64 arm64; do
    CGO_ENABLED=1 GOOS=darwin GOARCH=$a MACOSX_DEPLOYMENT_TARGET=11.0 go build -trimpath -ldflags "$LDFLAGS" -o "$m/strataos-sync-$a" ./cmd/strataos-sync
  done
  app="$m/dmg/StrataOS Sync.app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  lipo -create -output "$app/Contents/MacOS/strataos-sync" "$m/strataos-sync-amd64" "$m/strataos-sync-arm64"
  sed "s/@VERSION@/$V/g" packaging/macos/Info.plist > "$app/Contents/Info.plist"
  cp "$B/icons/icon.icns" "$app/Contents/Resources/strataos-sync.icns"
  codesign --force --deep --sign - "$app" 2>/dev/null || true # ad-hoc: arm64 refuses unsigned code
  ln -s /Applications "$m/dmg/Applications"
  cp packaging/README.txt "$m/dmg/README.txt"
  rm -f "$DIST/strataos-sync-$FV-macos.dmg"
  hdiutil create -quiet -volname "StrataOS Sync" -srcfolder "$m/dmg" -ov -format UDZO "$DIST/strataos-sync-$FV-macos.dmg"
  ;;
*)
  echo "usage: $0 linux|windows|macos" >&2
  exit 2
  ;;
esac
ls -l "$DIST"/strataos-sync-"$FV"-* 2>/dev/null || true
