#!/bin/bash
# Installs PacketTrail.app on a Mac from Terminal:
#   curl -fsSL https://mattbrianb.github.io/packettrail/install.sh | bash
#
# Why this exists: PacketTrail is free, so it isn't notarised by Apple (that needs
# a paid developer account). A copy downloaded with a web browser is
# "quarantined", and macOS makes you approve it in System Settings before it
# opens. A copy fetched with curl is not quarantined, so it just opens.
# The download is checked against the release's SHA-256 checksum first.
set -euo pipefail

repo="https://github.com/mattbrianb/packettrail/releases/latest/download"

if [ "$(uname -s)" != "Darwin" ]; then
  echo "This installer is for macOS. For Windows and Linux see https://mattbrianb.github.io/packettrail/#download" >&2
  exit 1
fi
case "$(uname -m)" in
  arm64) asset=packettrail-macos-arm64 ;;
  x86_64) asset=packettrail-macos-x64 ;;
  *) echo "Unsupported Mac processor: $(uname -m)" >&2; exit 1 ;;
esac

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $asset.zip ..."
curl -fL --progress-bar -o "$tmp/$asset.zip" "$repo/$asset.zip"

want="$(curl -fsSL "$repo/SHA256SUMS.txt" | awk -v f="$asset.zip" '$2 == f { print $1 }')"
got="$(shasum -a 256 "$tmp/$asset.zip" | awk '{ print $1 }')"
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  echo "The download does not match its published checksum, so nothing was installed." >&2
  echo "  expected: ${want:-(not listed)}" >&2
  echo "  got:      $got" >&2
  exit 1
fi
echo "Checksum OK."

ditto -x -k "$tmp/$asset.zip" "$tmp/unzipped"
src="$tmp/unzipped/$asset/PacketTrail.app"
[ -d "$src" ] || { echo "PacketTrail.app was not found in the download." >&2; exit 1; }

dest="/Applications"
if [ ! -w "$dest" ]; then
  dest="$HOME/Applications"
  mkdir -p "$dest"
fi

# Close a running copy so it can be replaced.
if pgrep -x packettrail >/dev/null 2>&1; then
  echo "Closing the running PacketTrail ..."
  pkill -x packettrail || true
  sleep 1
fi
rm -rf "$dest/PacketTrail.app"
ditto "$src" "$dest/PacketTrail.app"
xattr -dr com.apple.quarantine "$dest/PacketTrail.app" 2>/dev/null || true

ver="$(/usr/bin/defaults read "$dest/PacketTrail.app/Contents/Info" CFBundleShortVersionString 2>/dev/null || true)"
echo "Installed PacketTrail${ver:+ $ver}: $dest/PacketTrail.app"
if [ "${PACKETTRAIL_NO_OPEN:-}" = "" ]; then
  open "$dest/PacketTrail.app"
fi
