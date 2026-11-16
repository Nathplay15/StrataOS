#!/bin/bash
# Installs the "Text recognition (OCR)" component: tesseract with the English
# and French language packs, and poppler-utils to turn the pages of scanned
# PDFs into images. The full-text index then covers pictures and scans.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  tesseract-ocr tesseract-ocr-eng tesseract-ocr-fra poppler-utils
echo ">> Text recognition (OCR) is installed"
ha-notify
