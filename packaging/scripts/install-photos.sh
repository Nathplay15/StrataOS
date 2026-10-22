#!/bin/bash
# Installs the media tools of Photos and Files: libvips with HEIC support,
# ffmpeg (thumbnails, playback of any video or audio format), exiftool
# (previews of camera RAW files) and, on PCs, the VAAPI video drivers.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
pkgs=(libvips-tools libheif-plugin-libde265 ffmpeg libimage-exiftool-perl)
if [ "$(dpkg --print-architecture)" = amd64 ]; then
  pkgs+=(intel-media-va-driver mesa-va-drivers)
fi
apt-get install -y --no-install-recommends "${pkgs[@]}"
echo ">> Photos tools are installed"
