#!/bin/sh
# Cross-compiles simple-rest into dist/ as one static file per system, named
# <project>-<version>-<platform>[.exe]: Windows x64, Linux x64, and the
# Raspberry Pi with a 64-bit (arm64) or 32-bit (armhf) system. Where dpkg-deb
# is available (Debian, Ubuntu, the CI runners) it also packs the Linux files
# into .deb packages (scripts/deb.sh). Last comes a sha256 checksums file over
# everything in dist/.
# The names are a contract: the updater inside the app, the hub page and the
# templates in packaging/ read them, so they change only together with a
# release that reads the new names.
# Requires only the Go toolchain (https://go.dev/dl/). Run: sh build.sh
# REQUIRE_DEB=1 turns a missing dpkg-deb into an error (CI and releases).
set -eu
cd "$(dirname "$0")"
project=simple-rest
version=$(grep -m1 -v '^[[:space:]]*$' VERSION.md | sed 's/^[#[:space:]]*//; s/[[:space:]]*$//; s/^v//')
rm -rf dist
mkdir -p dist

build() {
  out="dist/$project-$version-$3"
  echo "  $1/$2 -> $out"
  CGO_ENABLED=0 GOOS=$1 GOARCH=$2 GOARM=${4:-} go build -trimpath -ldflags "-s -w" -o "$out" .
}

echo "building $project $version:"
build windows amd64 windows-amd64.exe
build linux   amd64 linux-amd64
build linux   arm64 linux-arm64            # Raspberry Pi 3/4/5 with a 64-bit OS
build linux   arm   linux-armhf  6         # every Raspberry Pi with a 32-bit OS

if command -v dpkg-deb >/dev/null 2>&1; then
  sh scripts/deb.sh "$version"
elif [ "${REQUIRE_DEB:-}" = 1 ]; then
  echo "dpkg-deb not found, and REQUIRE_DEB=1 needs the .deb packages" >&2
  exit 1
else
  echo "  dpkg-deb not found: no .deb packages (they are built on Linux)"
fi

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
