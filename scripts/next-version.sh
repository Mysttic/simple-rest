#!/bin/sh
# Predicts what the Release workflow publishes once HEAD is on master. Reads
# the commits since the newest v* tag the way semantic-release does with the
# conventionalcommits preset: "!" after the type or a BREAKING CHANGE footer
# raises the major number, feat the minor one, fix and perf the patch; other
# types (chore, ci, docs, test, refactor, ...) publish nothing.
# Prints key=value lines on stdout (ci.yml appends them to $GITHUB_OUTPUT):
# previous (the newest tag), bump (major, minor, patch or none) and next
# (the version to be published, empty when nothing is).
set -eu
cd "$(dirname "$0")/.."

previous=$(git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*' HEAD 2>/dev/null || true)
range=${previous:+$previous..}HEAD

rank() {
  case $1 in major) echo 3 ;; minor) echo 2 ;; patch) echo 1 ;; *) echo 0 ;; esac
}

bump=none
for sha in $(git rev-list --no-merges "$range"); do
  message=$(git log -1 --format=%B "$sha")
  subject=$(printf '%s\n' "$message" | head -n 1)
  this=none
  if printf '%s\n' "$subject" | grep -Eq '^[a-z]+(\([^)]*\))?!:' ||
     printf '%s\n' "$message" | grep -Eq '^BREAKING[ -]CHANGE:'; then
    this=major
  elif printf '%s\n' "$subject" | grep -Eq '^feat(\([^)]*\))?:'; then
    this=minor
  elif printf '%s\n' "$subject" | grep -Eq '^(fix|perf)(\([^)]*\))?:'; then
    this=patch
  fi
  if [ "$(rank "$this")" -gt "$(rank "$bump")" ]; then
    bump=$this
  fi
done

next=
if [ "$bump" != none ]; then
  if [ -z "$previous" ]; then
    next=1.0.0
  else
    IFS=. read -r major minor patch <<EOF
${previous#v}
EOF
    case $bump in
      major) next="$((major + 1)).0.0" ;;
      minor) next="$major.$((minor + 1)).0" ;;
      patch) next="$major.$minor.$((patch + 1))" ;;
    esac
  fi
fi

echo "previous=${previous:-none}"
echo "bump=$bump"
echo "next=$next"
