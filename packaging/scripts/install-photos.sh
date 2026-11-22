#!/bin/bash
# Installs the media tools of Photos and Files: libvips with HEIC support,
# ffmpeg (thumbnails, playback of any video or audio format), exiftool
# (previews of camera RAW files) and, on PCs, the VAAPI video drivers.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
pkgs=(libvips-tools libheif-plugin-libde265 ffmpeg libimage-exiftool-perl)
# vainfo shows what the graphics processor decodes and encodes (Hardware page).
has() { [ -n "$(apt-cache policy "$1" 2>/dev/null | awk '/Candidate:/ && $2 != "(none)" { print $2 }')" ]; }
pkgs+=(vainfo)
if [ "$(dpkg --print-architecture)" = amd64 ]; then
  # Intel: the non-free iHD driver encodes on every generation, the free
  # one decodes only on many; non-free is used when the APT sources have
  # the non-free component (the StrataOS installer does not add it).
  # i965 serves processors older than Broadwell (2015).
  if has intel-media-va-driver-non-free; then
    pkgs+=(intel-media-va-driver-non-free)
  else
    pkgs+=(intel-media-va-driver)
  fi
  pkgs+=(i965-va-driver mesa-va-drivers)
fi
apt-get install -y --no-install-recommends "${pkgs[@]}"
echo ">> Photos tools are installed"
