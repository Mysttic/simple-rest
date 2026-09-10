#!/bin/sh
# Cross-compiles simple-rest into dist/ as one static file per platform.
# Requires only the Go toolchain (https://go.dev/dl/). Run: sh build.sh
set -e
cd "$(dirname "$0")"
mkdir -p dist
build() {
  echo "  $1/$2 -> dist/$3"
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 GOARM=$4 go build -trimpath -ldflags "-s -w" -o "dist/$3" .
}
echo "building:"
build windows amd64 simple-rest-windows-amd64.exe
build windows arm64 simple-rest-windows-arm64.exe
build linux   amd64 simple-rest-linux-amd64
build linux   arm64 simple-rest-linux-arm64          # Raspberry Pi 3/4/5 with 64-bit OS
build linux   arm   simple-rest-linux-armv6 6        # Raspberry Pi Zero/1/2 and 32-bit OS
build darwin  arm64 simple-rest-macos-arm64
build darwin  amd64 simple-rest-macos-amd64
build android arm64 simple-rest-android-arm64        # run inside Termux
echo "done"
