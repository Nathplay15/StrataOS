#!/bin/bash
# Installs the "Document previews" component: LibreOffice without its desktop
# interface (about 400 MB), used to show office documents as PDF in Files,
# with metric-compatible fonts so documents keep their layout.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y --no-install-recommends \
  libreoffice-core-nogui libreoffice-writer-nogui libreoffice-calc-nogui libreoffice-impress-nogui libreoffice-draw-nogui \
  fonts-liberation2 fonts-crosextra-carlito fonts-crosextra-caladea fonts-dejavu-core
# Conversions run as this unprivileged account.
id strata-preview >/dev/null 2>&1 || useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin --user-group strata-preview
echo ">> Document previews are installed"
