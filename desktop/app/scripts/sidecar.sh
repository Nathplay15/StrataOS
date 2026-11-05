#!/bin/bash
# Builds the sync engine (desktop/, Go) as the sidecar of the app, named
# with the Rust target triple as Tauri expects:
#   scripts/sidecar.sh [TRIPLE]   (default: this machine's, from rustc or uname)
# The version comes from VERSION. CGO is off: the engine is pure Go.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$(cd ../.. && pwd)
V=$(cat "$ROOT/VERSION")
T=${1:-}
if [ -z "$T" ]; then
  if command -v rustc >/dev/null; then
    T=$(rustc -vV | sed -n 's/^host: //p')
  else
    case "$(uname -s)-$(uname -m)" in
      Linux-x86_64) T=x86_64-unknown-linux-gnu ;;
      Linux-aarch64) T=aarch64-unknown-linux-gnu ;;
      Darwin-arm64) T=aarch64-apple-darwin ;;
      Darwin-x86_64) T=x86_64-apple-darwin ;;
      *) echo "unknown machine: pass the target triple" >&2; exit 2 ;;
    esac
  fi
fi
case "$T" in
  x86_64-unknown-linux-gnu) os=linux arch=amd64 ;;
  aarch64-unknown-linux-gnu) os=linux arch=arm64 ;;
  x86_64-pc-windows-msvc) os=windows arch=amd64 ;;
  aarch64-pc-windows-msvc) os=windows arch=arm64 ;;
  x86_64-apple-darwin) os=darwin arch=amd64 ;;
  aarch64-apple-darwin) os=darwin arch=arm64 ;;
  universal-apple-darwin) os=darwin arch=universal ;;
  *) echo "unsupported target $T" >&2; exit 2 ;;
esac
ext=""
[ "$os" = windows ] && ext=.exe
out="$PWD/src-tauri/binaries/strataos-sync-$T$ext"
mkdir -p src-tauri/binaries
build() { (cd .. && CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "-s -w -X main.version=$V" -o "$3" ./cmd/strataos-sync); }
if [ "$arch" = universal ]; then
  build darwin amd64 "$out.amd64"
  build darwin arm64 "$out.arm64"
  lipo -create -output "$out" "$out.amd64" "$out.arm64"
  rm -f "$out.amd64" "$out.arm64"
else
  build "$os" "$arch" "$out"
fi
echo "$out"
