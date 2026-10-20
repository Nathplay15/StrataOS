#!/bin/bash
# Installs Tailscale from its official APT repository (Debian 13 trixie,
# amd64 and arm64). The signing key is checked against its known fingerprint
# before apt trusts it.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
FPR=2596A99EAAB33821893C0A79458CA832957F5868 # Tailscale Inc. (Package repository signing key)
KEYRING=/usr/share/keyrings/tailscale-archive-keyring.gpg
case "$(dpkg --print-architecture)" in amd64 | arm64) ;; *) echo "unsupported architecture" >&2; exit 1 ;; esac
apt-get update
apt-get install -y --no-install-recommends ca-certificates curl gnupg
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL --proto '=https' --tlsv1.2 https://pkgs.tailscale.com/stable/debian/trixie.noarmor.gpg -o "$tmp/key.gpg"
got=$(gpg --show-keys --with-colons "$tmp/key.gpg" 2>/dev/null | awk -F: '$1 == "fpr" { print $10; exit }')
if [ "$got" != "$FPR" ]; then
  echo "unexpected Tailscale signing key: '$got' (expected $FPR)" >&2
  exit 1
fi
install -m 644 "$tmp/key.gpg" "$KEYRING"
echo "deb [signed-by=$KEYRING] https://pkgs.tailscale.com/stable/debian trixie main" > /etc/apt/sources.list.d/tailscale.list
apt-get update
apt-get install -y tailscale
systemctl enable --now tailscaled.service
tailscale version | head -1
echo ">> Tailscale is installed"
