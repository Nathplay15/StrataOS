#!/bin/bash
# What apps installed at 0.12.0 or before still look for in the OS release
# (release.yml), now that the Android and desktop apps have their own releases:
#   - the Android app (up to 0.12.0) asks GitHub for releases/latest (or the
#     list, on the beta channel), keeps the v* tags and installs
#     strataos-<tag version>.apk, checked against that release's SHA256SUMS;
#   - the desktop app (up to 0.12.0) reads
#     releases/latest/download/latest.json.
# So every OS release carries a copy of the newest Android APK under the old
# name, and (final releases) the newest final desktop latest.json. Once
# updated, the apps read their own releases (android-v*, and the website's
# desktop/latest.json) and never come back here.
#
#   packaging/legacy-app-assets.sh <FV> <PRERELEASE> <dir>
#
# FV and PRERELEASE as backend/cmd/release-info prints them. Writes
# <dir>/strataos-<FV>.apk (its SHA-256 checked against the Android release's
# SHA256SUMS) and <dir>/latest.json; fails when no Android release newer than
# 0.12.0 exists (installed phones would never update), only warns for
# latest.json (the desktop updater needs the Tauri signing secrets anyway).
# Needs gh (GH_TOKEN, GH_REPO) and runs before the hours of building.
set -euo pipefail
FV=$1 PRE=$2 OUT=$3
# The last app versions with the old update code: what is copied must be newer.
LEGACY=0.12.0
mkdir -p "$OUT"

# versions PREFIX ALLOW_PRE: the versions of a component's releases, drafts
# left out ("~" for pre-releases, as VERSION writes them).
versions() {
  local filter='.[] | select(.isDraft | not)'
  [ "$2" = true ] || filter="$filter | select(.isPrerelease | not)"
  gh release list --limit 200 --json tagName,isDraft,isPrerelease -q "$filter | .tagName" |
    { grep -E "^$1v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.[0-9]+)?\$" || true; } |
    sed "s/^$1v//; s/-/~/"
}

# newest PREFIX ALLOW_PRE: the newest of them (~ sorts first, as for apt).
newest() { versions "$1" "$2" | sort -V | tail -1; }

# newer A B: whether version A sorts after B.
newer() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -1)" = "$1" ]; }

a=$(newest android- "$PRE")
if [ -z "$a" ] || ! newer "$a" "$LEGACY"; then
  echo "::error title=No Android app for installed phones::publish the Android app first (tag android-v<version>, newer than $LEGACY): phones with the app $LEGACY or older only update from the OS release, which must carry its APK (docs/DEVELOPMENT.md, Releases)"
  exit 1
fi
at="android-v${a//\~/-}"
tmp=$(mktemp -d)
gh release download "$at" --pattern "strataos-android-${a//\~/-}.apk" --pattern SHA256SUMS --dir "$tmp"
(cd "$tmp" && grep " [ *]\?strataos-android-${a//\~/-}.apk\$" SHA256SUMS | sha256sum -c -)
mv "$tmp/strataos-android-${a//\~/-}.apk" "$OUT/strataos-$FV.apk"
echo "Android app $a ($at) copied as strataos-$FV.apk"

# Pre-releases are never "latest": the desktop app does not read them. The
# newest final desktop release that has a latest.json (none without the Tauri
# signing secrets).
if [ "$PRE" != true ]; then
  found=
  for d in $(versions desktop- false | sort -rV); do
    newer "$d" "$LEGACY" || break
    if gh release download "desktop-v$d" --pattern latest.json --dir "$tmp" 2>/dev/null; then
      mv "$tmp/latest.json" "$OUT/latest.json"
      echo "desktop latest.json of desktop-v$d copied"
      found=1
      break
    fi
  done
  [ -n "$found" ] || echo "::warning title=No desktop update for installed apps::no final desktop release newer than $LEGACY with a latest.json: desktop apps $LEGACY or older will not find an update in this release"
fi
rm -rf "$tmp"
