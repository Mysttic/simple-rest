#!/bin/sh
# Packs the Linux files of one release into snaps for amd64, arm64 and armhf
# with "snap pack" (snapd), from packaging/snap/snap.yaml.in. The binaries are
# static, so a snap is only the file and its metadata: no snapcraft build, and
# every architecture packs on one runner.
# Usage: sh scripts/snap.sh <version> <dir with the release files> <output dir>
set -eu
cd "$(dirname "$0")/.."
version=$(printf '%s' "${1:?version}" | sed 's/^v//')
files=${2:?directory with the release files}
out=${3:?output directory}
mkdir -p "$out"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for pair in amd64:linux-amd64 arm64:linux-arm64 armhf:linux-armhf; do
  arch=${pair%%:*}
  platform=${pair#*:}
  root="$work/$arch"
  mkdir -p "$root/meta"
  install -m 0755 "$files/simple-rest-$version-$platform" "$root/simple-rest"
  install -m 0644 LICENSE "$root/LICENSE"
  sed "s/@VERSION@/$version/g; s/@ARCH@/$arch/g" packaging/snap/snap.yaml.in > "$root/meta/snap.yaml"
  snap pack "$root" "$out"
done
ls -la "$out"
