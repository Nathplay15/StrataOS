#!/bin/bash
# Publishes dist/*.deb into a signed APT repository in dist/apt (suite
# "stable", amd64 and arm64). Serve dist/apt over HTTPS and build the package
# with REPO_URL pointing to it: installed systems then update from it.
#
# Signing key: packaging/keys/gnupg (created on first use, never published),
# or, in CI, an armored private key in $APT_SIGNING_KEY. Its public part is
# packaging/keys/strataos-archive.gpg, embedded in the package.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO=dist/apt
ARCHS="amd64 arm64"

if [ -n "${APT_SIGNING_KEY:-}" ]; then
  GNUPGHOME=$(mktemp -d)
  export GNUPGHOME
  trap 'rm -rf "$GNUPGHOME"' EXIT
  printf '%s\n' "$APT_SIGNING_KEY" | gpg --batch --quiet --import
  KEYID=$(gpg --batch --list-secret-keys --with-colons | awk -F: '/^fpr/ {print $10; exit}')
  [ -n "$KEYID" ] || { echo "APT_SIGNING_KEY holds no private key"; exit 1; }
  # The packages trust packaging/keys/strataos-archive.gpg: signing with
  # another key would break updates.
  if [ -s packaging/keys/strataos-archive.gpg ] && ! gpg --show-keys --with-colons packaging/keys/strataos-archive.gpg | grep -q ":$KEYID:"; then
    echo "APT_SIGNING_KEY is not the key of packaging/keys/strataos-archive.gpg"; exit 1
  fi
else
  export GNUPGHOME="$PWD/packaging/keys/gnupg"
  KEYID_FILE=packaging/keys/keyid
  if [ ! -s "$KEYID_FILE" ]; then
    echo ">> Creating the StrataOS archive signing key (packaging/keys, never publish gnupg/)"
    mkdir -p "$GNUPGHOME"; chmod 700 "$GNUPGHOME"
    gpg --batch --quiet --passphrase '' --quick-gen-key "StrataOS Archive <archive@strataos.local>" ed25519 sign never
    gpg --batch --list-keys --with-colons archive@strataos.local | awk -F: '/^fpr/ {print $10; exit}' > "$KEYID_FILE"
  fi
  KEYID=$(cat "$KEYID_FILE")
  gpg --batch --yes --export "$KEYID" > packaging/keys/strataos-archive.gpg
fi
# "make-repo.sh key" only prepares the key (the package embeds its public part).
[ "${1:-}" = "key" ] && exit 0

mkdir -p "$REPO/pool/main/s/strataos"
cp -n dist/strataos_*.deb "$REPO/pool/main/s/strataos/" 2>/dev/null || true
(
  cd "$REPO"
  for a in $ARCHS; do
    mkdir -p "dists/stable/main/binary-$a"
    apt-ftparchive --arch "$a" packages pool > "dists/stable/main/binary-$a/Packages"
    gzip -9kf "dists/stable/main/binary-$a/Packages"
  done
  apt-ftparchive \
    -o APT::FTPArchive::Release::Origin=StrataOS -o APT::FTPArchive::Release::Label=StrataOS \
    -o APT::FTPArchive::Release::Suite=stable -o APT::FTPArchive::Release::Codename=stable \
    -o APT::FTPArchive::Release::Components=main -o "APT::FTPArchive::Release::Architectures=$ARCHS" \
    release dists/stable > dists/stable/Release
  gpg --batch --yes --default-key "$KEYID" --clearsign -o dists/stable/InRelease dists/stable/Release
  gpg --batch --yes --default-key "$KEYID" -abs -o dists/stable/Release.gpg dists/stable/Release
)
gpg --batch --yes --export "$KEYID" > "$REPO/strataos-archive.gpg"
echo ">> Repository ready in $REPO ($(ls "$REPO/pool/main/s/strataos" | wc -l) package(s))"
