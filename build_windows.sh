#!/usr/bin/env bash
# build_windows.sh — Cross-compile for Windows and produce the installer .exe
set -e

echo "=== Building remote_viewer.exe for Windows ==="
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -ldflags="-s -w" -o remote_viewer.exe .

echo "=== Building installer with NSIS ==="
makensis installer.nsi

echo ""
echo "Done! Installer: AntigravityRemoteViewer-Setup.exe"
