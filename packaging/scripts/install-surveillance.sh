#!/bin/bash
# Installs ffmpeg for the built-in recorder of the Surveillance app (already
# there when the Photos tools are installed).
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
if ! command -v ffmpeg >/dev/null; then
  apt-get update
  apt-get install -y --no-install-recommends ffmpeg
fi
ffmpeg -hide_banner -version | head -n 1
echo ">> Surveillance recorder is ready"
