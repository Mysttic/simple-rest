#!/bin/sh
# Walks the update path end to end on a real binary, the way the handover
# document asks for: a copy that reports 0.9.0 is started next to a fake hub
# that serves the version from VERSION.md, and the script watches the copy
# check, download, verify, swap its own executable and come back on the same
# port with the new number. Before the update a schedule of the data file is
# turned on and fetches a token from the fake hub, and the TCP console
# connects to a fake device that answers MLLP frames with an ACK; after it
# the schedule must be off, the overlay empty, no TCP connection open, the
# device must have seen the FIN and the data file must be untouched. Needs
# go, python3 (or python), curl and sha256sum (or shasum).
# Run: sh scripts/e2e-update.sh
set -eu
cd "$(dirname "$0")/.."

# need stops right away, naming the tool, when none of its alternatives is on
# the PATH; without it a missing python or curl shows up much later as a fake
# hub that never started.
need() {
  for tool in "$@"; do command -v "$tool" >/dev/null 2>&1 && return 0; done
  echo "FAIL: this test needs $(echo "$*" | sed 's/ / or /g') on the PATH" >&2
  exit 1
}
need go
need curl
need python3 python
need sha256sum shasum

project=simple-rest
version=$(grep -m1 -v '^[[:space:]]*$' VERSION.md | sed 's/^[#[:space:]]*//; s/[[:space:]]*$//; s/^v//')
root="$(pwd)/.e2e"
hubport=8791
appport=8792
devport=8793
goos=$(go env GOOS)
goarch=$(go env GOARCH)
ext=""
case "$goos/$goarch" in
  windows/amd64) suffix="windows-amd64.exe"; ext=.exe ;;
  linux/amd64)   suffix="linux-amd64" ;;
  linux/arm64)   suffix="linux-arm64" ;;
  *) echo "no release build for $goos/$goarch, nothing to walk here"; exit 0 ;;
esac
asset="$project-$version-$suffix"
sums="$project-$version-checksums.txt"
exe="$root/app/$project-e2e$ext"

if command -v python3 >/dev/null 2>&1; then py=python3; else py=python; fi

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

rm -rf "$root"
mkdir -p "$root/app" "$root/hub/dl"

# The token server is the fake hub itself: it serves token.json like any file.
# The data file holds a request for it, an environment and a schedule that
# sets {{token}} from access_token; nothing in the file turns it on. It also
# holds a TCP connection to the fake device, whose port comes from the
# environment; nothing in the file connects it.
printf '%s\n' '{"access_token":"e2e","expires_in":300}' > "$root/hub/token.json"
cat > "$root/app/data.json" <<EOF
{"version":1,"collections":[{"id":"c1","name":"E2E","open":true,"requests":[
  {"id":"r1","name":"Token","method":"GET","url":"http://127.0.0.1:$hubport/token.json"}]}],
 "envs":[{"id":"e1","name":"local","vars":[{"enabled":true,"name":"dev_port","value":"$devport"}]}],"activeEnv":"e1",
 "schedules":[{"id":"s1","name":"Token refresh","requestId":"r1","envId":"e1","everySec":240,"leadSec":60,
  "expiresPath":"expires_in","extract":[{"enabled":true,"optional":false,"var":"token","from":"json","path":"access_token"}]}],
 "tcp":[{"id":"t1","name":"E2E device","host":"127.0.0.1","port":"{{dev_port}}","framing":{"preset":"mllp"}}]}
EOF

# The fake device answers every MLLP frame with an ACK and notes in device.log
# when it listens, when a connection comes and when one ends with a FIN.
cat > "$root/device.py" <<'EOF'
import socket, sys

port, out = int(sys.argv[1]), sys.argv[2]

def note(line):
    with open(out, "ab") as f:
        f.write(line.encode() + b"\n")

srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.bind(("127.0.0.1", port))
srv.listen(4)
note("listening")
while True:
    conn, _ = srv.accept()
    note("accepted")
    pending = b""
    try:
        while True:
            data = conn.recv(65536)
            if not data:
                note("closed")
                break
            pending += data
            while b"\x1c\r" in pending:
                frame, pending = pending.split(b"\x1c\r", 1)
                note("frame %d" % len(frame))
                conn.sendall(b"\x0bMSH|^~\\&|DEVICE|E2E|||||ACK^O01|1|P|2.5\rMSA|AA|1\r\x1c\r")
    except OSError as e:
        note("error %s" % e)
    conn.close()
EOF
datasum=$(sha256 "$root/app/data.json")

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
$py "$root/device.py" "$devport" "$root/device.log" > "$root/device.out" 2>&1 &
devpid=$!
apppid=""

cleanup() {
  kill "$hubpid" 2>/dev/null || true
  kill "$devpid" 2>/dev/null || true
  [ -n "$apppid" ] && kill "$apppid" 2>/dev/null || true
  if [ "$goos" = windows ]; then taskkill //F //IM "$project-e2e.exe" >/dev/null 2>&1 || true
  else pkill -f "$project-e2e" 2>/dev/null || true; fi
}
trap cleanup EXIT
fail() {
  echo "FAIL: $*"
  echo "--- app.log"; cat "$root/app.log" 2>/dev/null || true
  echo "--- hub.log"; cat "$root/hub.log"
  echo "--- device.log"; cat "$root/device.log" "$root/device.out" 2>/dev/null || true
  exit 1
}

tries=60
until curl -s -o /dev/null "http://127.0.0.1:$hubport/releases"; do
  tries=$((tries - 1))
  [ "$tries" -gt 0 ] || fail "the fake hub did not start on port $hubport"
  sleep 0.25
done
echo "fake hub is up"
tries=60
until grep -q '^listening$' "$root/device.log" 2>/dev/null; do
  tries=$((tries - 1))
  [ "$tries" -gt 0 ] || fail "the fake device did not start on port $devport"
  sleep 0.25
done
echo "fake TCP device is up"

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

boot_of() { curl -s "$api/info" | sed -n 's/.*"boot":"\([0-9a-f]*\)".*/\1/p'; }
token_gets() { grep -c 'GET /token.json' "$root/hub.log" || true; }
oldboot=$(boot_of)
[ -n "$oldboot" ] || fail "/api/info has no boot"
case "$(curl -s "$api/schedules")" in
  *'"enabled":true'*) fail "a schedule is on right after the start" ;;
esac
code=$(curl -s -o "$root/enable.json" -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' "$api/schedules/s1/enable")
[ "$code" = 200 ] || fail "enable answered HTTP $code: $(cat "$root/enable.json")"
tries=30
until curl -s "$api/schedules" | grep -q '"value":"e2e"'; do
  tries=$((tries - 1))
  [ "$tries" -gt 0 ] || fail "the schedule did not set the token: $(curl -s "$api/schedules")"
  sleep 0.5
done
[ "$(token_gets)" -ge 1 ] || fail "the hub log shows no GET /token.json"
echo "the schedule is on and set {{token}} from the fake hub"

# The TCP console: no connection after the start; Connect takes the port from
# the environment, and a message with Wait for reply gets the device's ACK.
case "$(curl -s "$api/tcp")" in
  *'"sessions":[]'*) ;;
  *) fail "a TCP connection is open right after the start: $(curl -s "$api/tcp")" ;;
esac
code=$(curl -s -o "$root/connect.json" -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d '{"envId":"e1","host":"127.0.0.1","port":"{{dev_port}}","framing":{"preset":"mllp"}}' "$api/tcp/t1/connect")
[ "$code" = 202 ] || fail "connect answered HTTP $code: $(cat "$root/connect.json")"
tries=30
until curl -s "$api/tcp" | grep -q '"state":"connected"'; do
  tries=$((tries - 1))
  [ "$tries" -gt 0 ] || fail "the TCP connection did not open: $(curl -s "$api/tcp")"
  sleep 0.5
done
# MSH|^~\&|E2E|LAB|||||ORM^O01|1|P|2.5 and a CR, in base64.
msg='TVNIfF5+XCZ8RTJFfExBQnx8fHx8T1JNXk8wMXwxfFB8Mi41DQ=='
code=$(curl -s -o "$root/send.json" -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  -d "{\"parts\":[{\"b64\":\"$msg\"}],\"waitReply\":true}" "$api/tcp/t1/send")
[ "$code" = 200 ] || fail "send answered HTTP $code: $(cat "$root/send.json")"
grep -q '"wrapped":true' "$root/send.json" || fail "the message was not framed: $(cat "$root/send.json")"
# The wait ends with the first entry that has replyTo: the reply, an RX entry,
# or a note that none came. An entry holds no object, and dir comes before
# replyTo in it.
tries=30
until curl -s "$api/tcp/t1/log" | grep -q '"replyTo":'; do
  tries=$((tries - 1))
  [ "$tries" -gt 0 ] || fail "no reply in the TCP log: $(curl -s "$api/tcp/t1/log")"
  sleep 0.5
done
curl -s "$api/tcp/t1/log" > "$root/log.json"
grep -Eq '"dir":"rx"[^}]*"replyTo":' "$root/log.json" || fail "the wait ended without the device's reply: $(cat "$root/log.json")"
grep -q '^frame ' "$root/device.log" || fail "the device got no frame"
echo "the TCP connection is open and the device's ACK came as the reply"

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

# Every start, an update included, begins with all schedules off and an empty
# overlay, and the program never writes the data file.
newboot=$(boot_of)
[ -n "$newboot" ] && [ "$newboot" != "$oldboot" ] || fail "boot after the restart: '$newboot', before: '$oldboot'"
snap=$(curl -s "$api/schedules")
case "$snap" in
  *"\"boot\":\"$newboot\""*'"s1":{'*) ;;
  *) fail "no snapshot of s1 after the restart: $snap" ;;
esac
case "$snap" in
  *'"enabled":true'*) fail "a schedule is on after the restart: $snap" ;;
esac
case "$snap" in
  *'"value":"e2e"'*) fail "the overlay survived the restart: $snap" ;;
esac
[ "$(sha256 "$root/app/data.json")" = "$datasum" ] || fail "the data file changed"
gets=$(token_gets)
sleep 3
[ "$(token_gets)" = "$gets" ] || fail "GET /token.json went on after the restart: $gets, then $(token_gets)"
starts=$(grep -c 'schedules: 1 defined, all off until turned on' "$root/app.log" || true)
[ "$starts" -ge 2 ] || fail "the log has the schedules line $starts times, want one per start"
echo "after the restart: all schedules off, the overlay empty, the data file unchanged"

# The old process closed its TCP connection with a FIN, and the new one opens
# none by itself. A stream that comes back with the old boot gets a snapshot;
# curl ends it after 2 s with code 28, which set -e must not see.
case "$(curl -s "$api/tcp")" in
  *"\"boot\":\"$newboot\""*'"sessions":[]'*) ;;
  *) fail "a TCP connection is open after the restart: $(curl -s "$api/tcp")" ;;
esac
grep -q '^closed$' "$root/device.log" || fail "the device saw no FIN"
[ "$(grep -c '^accepted$' "$root/device.log")" = 1 ] || fail "the device got another connection"
out=$(curl -sN --max-time 2 "$api/events?after=$oldboot.5" || true)
case "$out" in
  *'event: tcp.snapshot'*'"sessions":[]'*'event: ready'*) ;;
  *) fail "the event stream after the restart: $out" ;;
esac
echo "after the restart: no TCP connection open, the device saw the FIN, the event stream starts from a snapshot"

echo "PASS: 0.9.0 updated itself to $version"
