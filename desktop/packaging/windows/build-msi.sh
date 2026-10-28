#!/bin/bash
# Builds the .msi on Windows (Git Bash), after build.sh windows:
# needs WiX 4+ (dotnet tool install --global wix).
set -euo pipefail
cd "$(dirname "$0")/../../.."
V=$(cat VERSION)
FV=${V//\~/-}
MSIV=${V%%~*} # MSI versions are numbers only: 0.8.0~beta.1 is 0.8.0
SRC=build/desktop/windows
wix build -arch x64 -d "Version=$MSIV" -d "Src=$SRC" desktop/packaging/windows/strataos-sync.wxs -o "dist/strataos-sync-$FV-windows-x64.msi"
ls -l "dist/strataos-sync-$FV-windows-x64.msi"
