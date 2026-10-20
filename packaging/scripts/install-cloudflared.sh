#!/bin/bash
# Installs cloudflared (Cloudflare Tunnel) from Cloudflare's APT repository
# (amd64 and arm64). The key is fetched over HTTPS from Cloudflare's package
# host, must be a valid OpenPGP key and, when CLOUDFLARE_FPR is set, match it;
# apt then checks every package against it.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
CLOUDFLARE_FPR=${CLOUDFLARE_FPR:-}
KEYRING=/usr/share/keyrings/cloudflare-main.gpg
case "$(dpkg --print-architecture)" in amd64 | arm64) ;; *) echo "unsupported architecture" >&2; exit 1 ;; esac
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl gnupg
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL --proto '=https' --tlsv1.2 https://pkg.cloudflare.com/cloudflare-main.gpg -o "$tmp/key"
# The file may be armored or binary: store it binary for signed-by.
gpg --batch --no-default-keyring --keyring "$tmp/ring.gpg" --import "$tmp/key" 2>/dev/null
got=$(gpg --batch --no-default-keyring --keyring "$tmp/ring.gpg" --with-colons --list-keys 2>/dev/null | awk -F: '$1 == "fpr" { print $10; exit }')
if [ -z "$got" ]; then
  echo "the Cloudflare signing key could not be read" >&2
  exit 1
fi
if [ -n "$CLOUDFLARE_FPR" ] && [ "$got" != "$CLOUDFLARE_FPR" ]; then
  echo "unexpected Cloudflare signing key: $got (expected $CLOUDFLARE_FPR)" >&2
  exit 1
fi
echo "Cloudflare signing key: $got"
gpg --batch --no-default-keyring --keyring "$tmp/ring.gpg" --export > "$tmp/out.gpg"
install -m 644 "$tmp/out.gpg" "$KEYRING"
echo "deb [signed-by=$KEYRING] https://pkg.cloudflare.com/cloudflared any main" > /etc/apt/sources.list.d/cloudflared.list
apt-get update
apt-get install -y cloudflared
cloudflared --version
echo ">> cloudflared is installed"
