#!/bin/sh
# Prints the CHANGELOG.md section of one version, without its heading and
# without leading or trailing blank lines. Usage: sh scripts/changelog-section.sh 1.2.3
set -eu
cd "$(dirname "$0")/.."
version=$(printf '%s' "${1:?version}" | sed 's/^v//')
awk -v v="$version" '
  function is_heading(line) {
    return index(line, "## [" v "]") == 1 || index(line, "## " v " ") == 1 || line == "## " v
  }
  is_heading($0) { on = 1; next }
  on && /^## / { exit }
  on { lines[n++] = $0 }
  END {
    start = 0
    while (start < n && lines[start] ~ /^[[:space:]]*$/) start++
    end = n
    while (end > start && lines[end - 1] ~ /^[[:space:]]*$/) end--
    for (i = start; i < end; i++) print lines[i]
  }
' CHANGELOG.md
