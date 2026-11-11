#!/bin/bash
# Builds PacketTrail.app for Apple silicon and Intel and zips each with its README
# and licences into dist/release. It runs on macOS (the GitHub Actions
# workflow .github/workflows/macos-app.yml) because a proper bundle signature
# needs Apple's codesign. The signature is ad-hoc: free, with no Apple
# Developer account, so macOS still asks the user to confirm the first launch.
#   scripts/macos-app.sh 1.1.0
set -euo pipefail
VERSION="${1:?usage: macos-app.sh VERSION}"
cd "$(dirname "$0")/.."
mkdir -p dist/release

# The icon: PNGs from tools/mkicon, assembled into an .icns by Apple's own
# iconutil so Finder and the Dock are sure to accept it.
rm -rf dist/packettrail.iconset
go run ./tools/mkicon -iconset dist/packettrail.iconset
iconutil -c icns dist/packettrail.iconset -o dist/packettrail.icns

for arch in arm64 amd64; do
  case $arch in arm64) name=packettrail-macos-arm64 ;; amd64) name=packettrail-macos-x64 ;; esac
  stage="dist/$name/$name"
  app="$stage/PacketTrail.app"
  rm -rf "dist/$name"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"

  CGO_ENABLED=0 GOOS=darwin GOARCH=$arch go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "$app/Contents/MacOS/packettrail" ./cmd/packettrail
  sed "s/@VERSION@/$VERSION/g" packaging/macos/Info.plist > "$app/Contents/Info.plist"
  plutil -lint "$app/Contents/Info.plist"
  cp dist/packettrail.icns "$app/Contents/Resources/packettrail.icns"
  printf 'APPL????' > "$app/Contents/PkgInfo"

  codesign --force --deep --sign - --identifier io.github.mattbrianb.packettrail "$app"
  codesign --verify --deep --strict --verbose=2 "$app"

  cp README.md LICENSE "$stage/"
  { cat THIRD-PARTY-NOTICES.md; printf '\n---\n\n'; cat third_party/TwemojiCountryFlags-LICENSE.md; } > "$stage/THIRD-PARTY-NOTICES.md"
  rm -f "dist/release/$name.zip"
  # ditto keeps permissions, symlinks and the signature intact
  (cd "dist/$name" && ditto -c -k --sequesterRsrc --keepParent "$name" "../release/$name.zip")
  echo "packaged dist/release/$name.zip"
done
