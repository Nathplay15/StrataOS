#!/bin/bash
# Publishes dist/*.deb into a signed APT repository in dist/apt (suite "stable").
# Serve dist/apt over HTTP(S) and build the package with REPO_URL pointing to it.
set -euo pipefail
cd "$(dirname "$0")/.."
export GNUPGHOME="$PWD/packaging/keys/gnupg"
KEYID_FILE=packaging/keys/keyid
REPO=dist/apt

if [ ! -s "$KEYID_FILE" ]; then
  echo ">> Creating the StrataOS archive signing key (packaging/keys, never publish gnupg/)"
  mkdir -p "$GNUPGHOME"; chmod 700 "$GNUPGHOME"
  gpg --batch --quiet --passphrase '' --quick-gen-key "StrataOS Archive <archive@strataos.local>" ed25519 sign never
  gpg --batch --list-keys --with-colons archive@strataos.local | awk -F: '/^fpr/ {print $10; exit}' > "$KEYID_FILE"
fi
KEYID=$(cat "$KEYID_FILE")
gpg --batch --yes --export "$KEYID" > packaging/keys/strataos-archive.gpg
# "make-repo.sh key" only prepares the key (the package embeds its public part).
[ "${1:-}" = "key" ] && exit 0

mkdir -p "$REPO/pool/main/s/strataos" "$REPO/dists/stable/main/binary-amd64"
cp -n dist/strataos_*.deb "$REPO/pool/main/s/strataos/" 2>/dev/null || true
(
  cd "$REPO"
  apt-ftparchive packages pool > dists/stable/main/binary-amd64/Packages
  gzip -9kf dists/stable/main/binary-amd64/Packages
  apt-ftparchive \
    -o APT::FTPArchive::Release::Origin=StrataOS -o APT::FTPArchive::Release::Label=StrataOS \
    -o APT::FTPArchive::Release::Suite=stable -o APT::FTPArchive::Release::Codename=stable \
    -o APT::FTPArchive::Release::Components=main -o APT::FTPArchive::Release::Architectures=amd64 \
    release dists/stable > dists/stable/Release
  gpg --batch --yes --default-key "$KEYID" --clearsign -o dists/stable/InRelease dists/stable/Release
  gpg --batch --yes --default-key "$KEYID" -abs -o dists/stable/Release.gpg dists/stable/Release
)
cp packaging/keys/strataos-archive.gpg "$REPO/strataos-archive.gpg"
echo ">> Repository ready in $REPO ($(ls "$REPO/pool/main/s/strataos" | wc -l) package(s))"
