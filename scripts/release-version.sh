#!/bin/sh
# Reads VERSION.md, compares it with the newest v* tag and decides whether a
# release is due. Prints key=value lines on stdout (the workflows append them
# to $GITHUB_OUTPUT); diagnostics go to stderr. Exit code 1 means the
# repository must not be released as it is: a lowered version, or a version
# without its CHANGELOG.md section.
set -eu
cd "$(dirname "$0")/.."

version=$(grep -m1 -v '^[[:space:]]*$' VERSION.md | sed 's/^[#[:space:]]*//; s/[[:space:]]*$//; s/^v//')
case "$version" in
  *[!0-9.]*|"") echo "::error::VERSION.md must hold a version like 1.2.3, got '$version'" >&2; exit 1 ;;
esac
dots=$(printf '%s' "$version" | tr -cd '.' | wc -c | tr -d ' ')
if [ "$dots" -ne 2 ]; then
  echo "::error::VERSION.md must hold major.minor.patch, got '$version'" >&2
  exit 1
fi

previous=$(git tag -l 'v[0-9]*' | sed 's/^v//' | sort -V | tail -n 1)
publishing=false
if [ -z "$previous" ]; then
  publishing=true
elif [ "$version" = "$previous" ]; then
  publishing=false
elif [ "$(printf '%s\n%s\n' "$previous" "$version" | sort -V | tail -n 1)" = "$version" ]; then
  publishing=true
else
  echo "::error::VERSION.md ($version) is lower than the newest release ($previous)" >&2
  exit 1
fi

if [ "$publishing" = true ]; then
  pattern="^## \\[?$(printf '%s' "$version" | sed 's/\./\\./g')\\]?( |$)"
  if ! grep -Eq "$pattern" CHANGELOG.md; then
    echo "::error::CHANGELOG.md has no '## [$version]' section for the version in VERSION.md" >&2
    exit 1
  fi
fi

echo "version=$version"
echo "tag=v$version"
echo "previous=${previous:-none}"
echo "publishing=$publishing"
