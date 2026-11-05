#!/bin/bash
# Builds the Linux app (.deb and .AppImage) in a container with Rust and
# Tauri's libraries (docker/Dockerfile), for a machine without them:
#   scripts/docker-build.sh [tauri build arguments…]
# Node and Go come from the host (PATH); caches go to $CACHE (default
# ../../build/desktop-cache). The files land in src-tauri/target/release/bundle.
set -euo pipefail
cd "$(dirname "$0")/.."
APP=$PWD
ROOT=$(cd ../.. && pwd)
CACHE=${CACHE:-$ROOT/build/desktop-cache}
IMAGE=${IMAGE:-strataos-desktop-build}
mkdir -p "$CACHE/cargo" "$CACHE/home"
docker image inspect "$IMAGE" >/dev/null 2>&1 || docker build -t "$IMAGE" docker
scripts/sidecar.sh x86_64-unknown-linux-gnu >/dev/null
NODE_DIR=$(dirname "$(dirname "$(command -v node)")")
docker run --rm -u "$(id -u):$(id -g)" \
  -v "$ROOT:$ROOT" -v "$NODE_DIR:/opt/node:ro" -v "$CACHE:/cache" \
  -e HOME=/cache/home -e CARGO_HOME=/cache/cargo -e PATH=/opt/node/bin:/usr/local/cargo/bin:/usr/local/bin:/usr/bin:/bin \
  -e APPIMAGE_EXTRACT_AND_RUN=1 -e NO_STRIP=1 \
  -e TAURI_SIGNING_PRIVATE_KEY -e TAURI_SIGNING_PRIVATE_KEY_PASSWORD \
  -w "$APP" "$IMAGE" sh -c "npm ci --no-audit --no-fund >/dev/null && npx tauri build $*"
