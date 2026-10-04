#!/bin/bash
# Builds the Go core (gomobile) and the signed APK.
# Needs: Go, JDK 17+, Android SDK (ANDROID_HOME) with NDK (ANDROID_NDK_HOME).
set -euo pipefail
cd "$(dirname "$0")"
: "${ANDROID_HOME:?set ANDROID_HOME to your Android SDK}"
export PATH="$(go env GOPATH)/bin:$PATH"
command -v gomobile >/dev/null || go install golang.org/x/mobile/cmd/gomobile@latest golang.org/x/mobile/cmd/gobind@latest

mkdir -p app/libs
(cd .. && gomobile bind -target=android/arm64,android/arm,android/amd64 -androidapi 24 \
    -javapkg=io.github.tgwsproxy -ldflags="-s -w" -trimpath \
    -o android/app/libs/tgws.aar ./mobile/tgws)
rm -f app/libs/tgws-sources.jar
./gradlew --no-daemon assembleRelease
ls -la app/build/outputs/apk/release/
