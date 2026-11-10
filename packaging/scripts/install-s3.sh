#!/bin/bash
# Installs the built-in S3 service: versitygw (Versity S3 Gateway, Apache
# 2.0), a single static binary from its GitHub release, pinned by version
# and SHA-256 (amd64 and arm64). StrataOS writes its configuration and unit.
set -euo pipefail
VERSION=1.9.0
case "$(dpkg --print-architecture)" in
  amd64) ARCH=x86_64 SUM=0c28ccda3452451d7c71a0427f6aa7d0db5cac5e2b98a8e4dd2970967e1aadc1 ;;
  arm64) ARCH=arm64 SUM=1ab234dc11d540d861898d391bfda5914eaff78f20d162c3ce7c84337b3d9204 ;;
  *) echo "unsupported architecture" >&2; exit 1 ;;
esac
command -v curl >/dev/null || { apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends curl ca-certificates; }
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
NAME=versitygw_v${VERSION}_Linux_${ARCH}
curl -fsSL --proto '=https' --tlsv1.2 -o "$tmp/gw.tar.gz" "https://github.com/versity/versitygw/releases/download/v${VERSION}/${NAME}.tar.gz"
echo "$SUM  $tmp/gw.tar.gz" | sha256sum -c --quiet || { echo "checksum mismatch: download refused" >&2; exit 1; }
tar -xzf "$tmp/gw.tar.gz" -C "$tmp" "$NAME/versitygw" "$NAME/LICENSE"
install -Dm755 "$tmp/$NAME/versitygw" /usr/local/lib/strata/versitygw
install -Dm644 "$tmp/$NAME/LICENSE" /usr/local/share/doc/strata-versitygw/LICENSE
echo ">> versitygw $VERSION is installed (enable S3 in File services)"
