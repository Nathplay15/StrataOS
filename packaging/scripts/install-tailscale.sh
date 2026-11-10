#!/bin/bash
# Installs Tailscale from its official APT repository (the Debian release
# running, or trixie when Tailscale does not publish it yet; amd64 and arm64). The signing key is checked against its known fingerprint
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
BASE=https://pkgs.tailscale.com/stable/debian
SUITE=$(. /etc/os-release && echo "${VERSION_CODENAME:-trixie}")
case "$SUITE" in *[!a-z]* | "") SUITE=trixie ;; esac
if [ "$SUITE" != trixie ] && ! curl -fsSI --proto '=https' --tlsv1.2 "$BASE/dists/$SUITE/InRelease" >/dev/null 2>&1; then
  echo ">> Tailscale has no repository for $SUITE yet: using the trixie one"
  SUITE=trixie
fi
curl -fsSL --proto '=https' --tlsv1.2 "$BASE/$SUITE.noarmor.gpg" -o "$tmp/key.gpg"
got=$(gpg --show-keys --with-colons "$tmp/key.gpg" 2>/dev/null | awk -F: '$1 == "fpr" { print $10; exit }')
if [ "$got" != "$FPR" ]; then
  echo "unexpected Tailscale signing key: '$got' (expected $FPR)" >&2
  exit 1
fi
install -m 644 "$tmp/key.gpg" "$KEYRING"
echo "deb [signed-by=$KEYRING] $BASE $SUITE main" > /etc/apt/sources.list.d/tailscale.list
apt-get update
apt-get install -y tailscale
systemctl enable --now tailscaled.service
tailscale version | head -1
echo ">> Tailscale is installed"
