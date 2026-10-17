#!/bin/bash
# Installs the thumbnail tools of the Photos app (libvips with HEIC support, ffmpeg).
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends libvips-tools libheif-plugin-libde265 ffmpeg
echo ">> Photos tools are installed"
