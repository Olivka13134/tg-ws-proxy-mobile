#!/bin/bash
# Builds the Go core for iOS and generates the Xcode project, then opens it.
# Needs: a Mac with Xcode, Homebrew (https://brew.sh).
set -euo pipefail
cd "$(dirname "$0")"

command -v brew >/dev/null || { echo "Install Homebrew first: https://brew.sh"; exit 1; }
command -v go >/dev/null || brew install go
command -v xcodegen >/dev/null || brew install xcodegen

export PATH="$(go env GOPATH)/bin:$PATH"
go install golang.org/x/mobile/cmd/gomobile@latest golang.org/x/mobile/cmd/gobind@latest

echo "==> Building the Go proxy core (Tgws.xcframework)…"
rm -rf Frameworks/Tgws.xcframework
(cd .. && gomobile bind -target=ios,iossimulator -iosversion=15.0 \
    -ldflags="-s -w" -trimpath -o ios/Frameworks/Tgws.xcframework ./mobile/tgws)

echo "==> Generating TgWsProxy.xcodeproj…"
xcodegen generate

echo "==> Done. Opening Xcode."
open TgWsProxy.xcodeproj
