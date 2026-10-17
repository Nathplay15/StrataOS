#!/bin/bash
# Builds dist/strataos_<version>_<arch>.deb from a compiled strata binary.
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION=$(cat VERSION)
ARCH=${ARCH:-amd64}
BIN=${BIN:-build/strata-linux-$ARCH}
[ -x "$BIN" ] || { echo "missing binary $BIN (run make build)"; exit 1; }

ROOT=$(mktemp -d)
trap 'rm -rf "$ROOT"' EXIT
chmod 755 "$ROOT"
install -Dm755 "$BIN" "$ROOT/usr/bin/strata"
install -Dm644 packaging/systemd/strata.service "$ROOT/usr/lib/systemd/system/strata.service"
install -Dm644 packaging/avahi/strata.service "$ROOT/etc/avahi/services/strata.service"
install -Dm755 packaging/scripts/install-zfs.sh "$ROOT/usr/lib/strata/scripts/install-zfs.sh"
install -Dm755 packaging/scripts/install-docker.sh "$ROOT/usr/lib/strata/scripts/install-docker.sh"
install -Dm755 packaging/scripts/install-nut.sh "$ROOT/usr/lib/strata/scripts/install-nut.sh"
install -Dm755 packaging/scripts/install-vms.sh "$ROOT/usr/lib/strata/scripts/install-vms.sh"
install -Dm755 packaging/scripts/install-rclone.sh "$ROOT/usr/lib/strata/scripts/install-rclone.sh"
install -Dm755 packaging/scripts/install-ftp.sh "$ROOT/usr/lib/strata/scripts/install-ftp.sh"
install -Dm755 packaging/scripts/install-directory.sh "$ROOT/usr/lib/strata/scripts/install-directory.sh"
install -Dm755 packaging/scripts/install-antivirus.sh "$ROOT/usr/lib/strata/scripts/install-antivirus.sh"
install -Dm755 packaging/scripts/upssched-cmd "$ROOT/usr/lib/strata/scripts/upssched-cmd"
# APT repository for StrataOS updates (see packaging/make-repo.sh).
if [ -f packaging/keys/strataos-archive.gpg ]; then
  install -Dm644 packaging/keys/strataos-archive.gpg "$ROOT/usr/share/keyrings/strataos-archive.gpg"
fi
if [ -n "${REPO_URL:-}" ]; then
  install -d -m755 "$ROOT/etc/apt" "$ROOT/etc/apt/sources.list.d"
  cat > "$ROOT/etc/apt/sources.list.d/strataos.sources" <<SRC
# StrataOS updates
Types: deb
URIs: $REPO_URL
Suites: stable
Components: main
Signed-By: /usr/share/keyrings/strataos-archive.gpg
SRC
  chmod 644 "$ROOT/etc/apt/sources.list.d/strataos.sources"
fi
mkdir -p "$ROOT/DEBIAN"
SIZE=$(du -sk "$ROOT" | cut -f1)
sed -e "s/@VERSION@/$VERSION/" -e "s/@ARCH@/$ARCH/" -e "s/@SIZE@/$SIZE/" packaging/debian/control.in > "$ROOT/DEBIAN/control"
install -m755 packaging/debian/postinst packaging/debian/prerm packaging/debian/postrm "$ROOT/DEBIAN/"
echo "/etc/avahi/services/strata.service" > "$ROOT/DEBIAN/conffiles"
[ -f "$ROOT/etc/apt/sources.list.d/strataos.sources" ] && echo "/etc/apt/sources.list.d/strataos.sources" >> "$ROOT/DEBIAN/conffiles"
mkdir -p dist
OUT="dist/strataos_${VERSION}_${ARCH}.deb"
dpkg-deb --root-owner-group -Zxz --build "$ROOT" "$OUT"
echo "built $OUT"
