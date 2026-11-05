#!/bin/sh
# Fills a template from packaging/ for one release and prints it:
#   @VERSION@              the version, without the v;
#   @SHA256_<PLATFORM>@    the SHA-256 of simple-rest-<version>-<platform> from
#                          the checksums file, the platform in capitals with
#                          - and . turned into _ (WINDOWS_AMD64_EXE, LINUX_ARMHF);
#   @SHA256_LICENSE@       the SHA-256 of LICENSE in this checkout.
# A placeholder left without a value is an error, so a renamed release file
# cannot slip into a manifest unnoticed.
# Usage: sh scripts/render.sh <template> <version> [checksums file]
# (the default checksums file is dist/simple-rest-<version>-checksums.txt)
set -eu
cd "$(dirname "$0")/.."
template=${1:?template}
version=$(printf '%s' "${2:?version}" | sed 's/^v//')
sums=${3:-dist/simple-rest-$version-checksums.txt}

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1"; else shasum -a 256 "$1"; fi | cut -d' ' -f1
}

script="s/@VERSION@/$version/g; s/@SHA256_LICENSE@/$(sha256 LICENSE)/g"
while read -r sum name; do
  name=${name#\*}
  platform=${name#simple-rest-$version-}
  [ "$platform" = "$name" ] && continue # the .deb packages
  key=$(printf '%s' "$platform" | tr 'a-z.-' 'A-Z__')
  script="$script; s/@SHA256_${key}@/$sum/g"
done < "$sums"

out=$(sed "$script" "$template")
left=$(printf '%s\n' "$out" | grep -o '@[A-Z][A-Z0-9_]*@' | sort -u | tr '\n' ' ' || true)
if [ -n "$left" ]; then
  echo "render.sh: $template keeps placeholders without a value: $left" >&2
  exit 1
fi
printf '%s\n' "$out"
