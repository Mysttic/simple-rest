#!/bin/sh
# Walks the update path end to end on a real binary, the way the handover
# document asks for: a copy that reports 0.9.0 is started next to a fake hub
# that serves the version from VERSION.md, and the script watches the copy
# check, download, verify, swap its own executable and come back on the same
# port with the new number. Needs go, python3 (or python) and curl.
# Run: sh scripts/e2e-update.sh
set -eu
cd "$(dirname "$0")/.."

project=simple-rest
version=$(grep -m1 -v '^[[:space:]]*$' VERSION.md | sed 's/^[#[:space:]]*//; s/[[:space:]]*$//; s/^v//')
root="$(pwd)/.e2e"
hubport=8791
appport=8792
goos=$(go env GOOS)
goarch=$(go env GOARCH)
ext=""
case "$goos/$goarch" in
  windows/*) suffix="$goos-$goarch.exe"; ext=.exe ;;
  darwin/*)  suffix="macos-$goarch" ;;
  linux/arm) suffix="linux-armv6" ;;
  *)         suffix="$goos-$goarch" ;;
esac
asset="$project-$version-$suffix"
sums="$project-$version-checksums.txt"
exe="$root/app/$project-e2e$ext"

if command -v python3 >/dev/null 2>&1; then py=python3; else py=python; fi

rm -rf "$root"
mkdir -p "$root/app" "$root/hub/dl"

echo "building a copy that reports 0.9.0 and the real $version"
go build -ldflags "-X main.versionOverride=0.9.0" -o "$exe" .
go build -o "$root/hub/dl/$asset" .
(
  cd "$root/hub/dl"
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$asset" > "$sums"; else shasum -a 256 "$asset" > "$sums"; fi
)
size=$(wc -c < "$root/hub/dl/$asset" | tr -d ' ')
csize=$(wc -c < "$root/hub/dl/$sums" | tr -d ' ')
cat > "$root/hub/releases" <<EOF
[{"tag_name":"chatt-v9.9.9","draft":false,"prerelease":false,"published_at":"2030-01-01T00:00:00Z","assets":[]},
 {"tag_name":"$project-v$version","draft":false,"prerelease":false,"published_at":"2026-09-10T12:00:00Z",
  "html_url":"http://127.0.0.1:$hubport/rel","body":"$project $version\n\nhttps://mysttic.github.io/Mysttic/$project/",
  "assets":[{"name":"$asset","size":$size,"browser_download_url":"http://127.0.0.1:$hubport/dl/$asset"},
            {"name":"$sums","size":$csize,"browser_download_url":"http://127.0.0.1:$hubport/dl/$sums"}]}]
EOF

$py -m http.server "$hubport" --bind 127.0.0.1 --directory "$root/hub" > "$root/hub.log" 2>&1 &
hubpid=$!
apppid=""

cleanup() {
  kill "$hubpid" 2>/dev/null || true
  [ -n "$apppid" ] && kill "$apppid" 2>/dev/null || true
  if [ "$goos" = windows ]; then taskkill //F //IM "$project-e2e.exe" >/dev/null 2>&1 || true
  else pkill -f "$project-e2e" 2>/dev/null || true; fi
}
trap cleanup EXIT
fail() { echo "FAIL: $*"; echo "--- app.log"; cat "$root/app.log" 2>/dev/null; echo "--- hub.log"; cat "$root/hub.log"; exit 1; }

tries=60
until curl -s -o /dev/null "http://127.0.0.1:$hubport/releases"; do
  tries=$((tries - 1))
  [ "$tries" -gt 0 ] || fail "the fake hub did not start on port $hubport"
  sleep 0.25
done
echo "fake hub is up"

"$exe" -no-browser -listen "127.0.0.1:$appport" -data "$root/app/data.json" \
  -update-url "http://127.0.0.1:$hubport/releases" > "$root/app.log" 2>&1 &
apppid=$!

api="http://127.0.0.1:$appport/api"
wait_for_version() {
  want=$1; tries=$2
  while [ "$tries" -gt 0 ]; do
    got=$(curl -s "$api/info" 2>/dev/null | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')
    [ "$got" = "$want" ] && return 0
    tries=$((tries - 1)); sleep 0.5
  done
  return 1
}

wait_for_version 0.9.0 30 || fail "the 0.9.0 copy did not start on port $appport"
echo "0.9.0 is up"

status=$(curl -s -X POST "$api/update/check")
case "$status" in
  *'"state":"newer"'*) ;;
  *) fail "check did not find $version: $status" ;;
esac
case "$status" in
  *"\"version\":\"$version\""*) echo "check found $version" ;;
  *) fail "wrong version offered: $status" ;;
esac
case "$status" in
  *'"canInstall":true'*) ;;
  *) fail "the copy says it cannot install: $status" ;;
esac

code=$(curl -s -o "$root/install.json" -w '%{http_code}' -X POST "$api/update/install")
[ "$code" = 202 ] || fail "install answered HTTP $code: $(cat "$root/install.json")"
echo "download started"

wait_for_version "$version" 90 || fail "the new version did not come up within 45 s"
echo "$version answers on port $appport after the restart"

grep -q "updated to $version, restarting" "$root/app.log" || fail "the log lacks the restart line"
tries=30
while [ -e "$exe.old" ] && [ "$tries" -gt 0 ]; do tries=$((tries - 1)); sleep 0.5; done
[ -e "$exe.old" ] && fail "$exe.old was not removed by the new process"
[ -d "$root/app/.$project-update" ] && fail "the update directory was left behind"
"$exe" -version | grep -q " $version " || fail "the file on disk does not report $version"

echo "PASS: 0.9.0 updated itself to $version"
