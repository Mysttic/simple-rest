#!/bin/sh
# Packs the Linux files from dist/ into Debian packages for amd64, arm64 and
# armhf (Raspberry Pi OS, Ubuntu, Debian), installed as /usr/bin/simple-rest.
# build.sh calls it after the binaries are built. Needs dpkg-deb.
# Usage: sh scripts/deb.sh 1.2.3
# A copy installed this way keeps its data in ~/.config/simple-rest and leaves
# updates to the package (install.go).
set -eu
cd "$(dirname "$0")/.."
version=${1:?version}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for pair in amd64:linux-amd64 arm64:linux-arm64 armhf:linux-armhf; do
  arch=${pair%%:*}
  platform=${pair#*:}
  root="$work/$arch"
  mkdir -p "$root/DEBIAN" "$root/usr/bin" "$root/usr/share/doc/simple-rest"
  install -m 0755 "dist/simple-rest-$version-$platform" "$root/usr/bin/simple-rest"
  install -m 0644 LICENSE "$root/usr/share/doc/simple-rest/copyright"
  size=$(du -sk "$root/usr" | cut -f1)
  cat > "$root/DEBIAN/control" <<EOF
Package: simple-rest
Version: $version
Architecture: $arch
Maintainer: Mysttic <19372942+Mysttic@users.noreply.github.com>
Installed-Size: $size
Section: web
Priority: optional
Homepage: https://github.com/Mysttic/simple-rest
Description: REST and TCP client in one file, with its UI in the browser
 simple-rest serves its interface on http://127.0.0.1:8787/ and sends the
 requests itself, so CORS never gets in the way: collections, environments
 with {{variables}}, history, schedules that keep a token fresh and a TCP
 console for devices on the network. Data lives in ~/.config/simple-rest.
EOF
  out="dist/simple-rest_${version}_$arch.deb"
  dpkg-deb --build --root-owner-group "$root" "$out" >/dev/null
  echo "  $arch -> $out"
done
