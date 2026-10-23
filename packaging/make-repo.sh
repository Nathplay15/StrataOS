#!/bin/bash
# Publishes dist/*.deb into a signed APT repository in dist/apt (amd64 and
# arm64), with two suites: "stable" (final releases) and "testing" (every
# release, pre-releases included: the beta channel). Serve dist/apt over HTTPS
# and build the package with REPO_URL pointing to it: installed systems then
# update from it.
#
# A package whose version holds "~" (0.8.0~beta.1) is a pre-release
# (backend/internal/release): it goes to pool/testing, the others to
# pool/main. "stable" lists pool/main only, "testing" the whole pool; apt
# then installs the newest version of the suites a NAS follows.
#
#   make-repo.sh            build the repository
#   make-repo.sh key        only prepare the key (the package embeds its public part)
#   make-repo.sh sign FILE  write FILE.asc, a detached armored signature (SHA256SUMS)
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
case "${1:-}" in
  key) exit 0 ;;
  sign)
    [ -n "${2:-}" ] || { echo "usage: make-repo.sh sign FILE"; exit 1; }
    gpg --batch --yes --local-user "$KEYID" --armor --detach-sign -o "$2.asc" "$2"
    gpg --batch --verify "$2.asc" "$2"
    exit 0 ;;
esac

mkdir -p "$REPO/pool/main/s/strataos" "$REPO/pool/testing/s/strataos"
for f in dist/strataos_*.deb; do
  [ -f "$f" ] || continue
  case "$(dpkg-deb -f "$f" Version)" in
    *~*) dest=$REPO/pool/testing/s/strataos ;;
    *) dest=$REPO/pool/main/s/strataos ;;
  esac
  cp -n "$f" "$dest/"
done
(
  cd "$REPO"
  for suite in stable testing; do
    src=pool
    [ "$suite" = stable ] && src=pool/main
    for a in $ARCHS; do
      mkdir -p "dists/$suite/main/binary-$a"
      apt-ftparchive --arch "$a" packages "$src" > "dists/$suite/main/binary-$a/Packages"
      gzip -9kf "dists/$suite/main/binary-$a/Packages"
    done
    apt-ftparchive \
      -o APT::FTPArchive::Release::Origin=StrataOS -o APT::FTPArchive::Release::Label=StrataOS \
      -o APT::FTPArchive::Release::Suite=$suite -o APT::FTPArchive::Release::Codename=$suite \
      -o APT::FTPArchive::Release::Components=main -o "APT::FTPArchive::Release::Architectures=$ARCHS" \
      release "dists/$suite" > "dists/$suite/Release"
    gpg --batch --yes --default-key "$KEYID" --clearsign -o "dists/$suite/InRelease" "dists/$suite/Release"
    gpg --batch --yes --default-key "$KEYID" -abs -o "dists/$suite/Release.gpg" "dists/$suite/Release"
  done
)
gpg --batch --yes --export "$KEYID" > "$REPO/strataos-archive.gpg"
echo ">> Repository ready in $REPO: stable $(ls "$REPO/pool/main/s/strataos" | wc -l) package(s), testing $(find "$REPO/pool" -name '*.deb' | wc -l)"
