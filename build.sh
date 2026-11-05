#!/bin/sh
# Cross-compiles simple-rest into dist/ as one static file per system, named
# <project>-<version>-<platform>[.exe], plus a sha256 checksums file. One build
# per system: Windows x64, Linux x64 and 64-bit ARM for the Raspberry Pi.
# The names are a contract: the updater inside the app and the hub page read
# them, so they change only together with a release that reads the new names.
# Requires only the Go toolchain (https://go.dev/dl/). Run: sh build.sh
set -eu
cd "$(dirname "$0")"
project=simple-rest
version=$(grep -m1 -v '^[[:space:]]*$' VERSION.md | sed 's/^[#[:space:]]*//; s/[[:space:]]*$//; s/^v//')
rm -rf dist
mkdir -p dist

build() {
  out="dist/$project-$version-$3"
  echo "  $1/$2 -> $out"
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 go build -trimpath -ldflags "-s -w" -o "$out" .
}

echo "building $project $version:"
build windows amd64 windows-amd64.exe
build linux   amd64 linux-amd64
build linux   arm64 linux-arm64            # Raspberry Pi 3/4/5 with a 64-bit OS

sums="$project-$version-checksums.txt"
(
  cd dist
  files=$(ls -1 | grep -v -- '-checksums.txt')
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum $files > "$sums"
  else
    shasum -a 256 $files > "$sums"
  fi
)
echo "  checksums -> dist/$sums"
ls -la dist
