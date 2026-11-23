#!/usr/bin/env bash
# Prints the next release tag (vMAJOR.MINOR.PATCH) from the latest v* tag and
# a commit message: "#major" or "BREAKING CHANGE" bumps the major version,
# "#minor" the minor version, anything else the patch version.
# Without any tag yet, the first release is v0.1.0 (v1.0.0 with "#major").
set -euo pipefail

msg="${1:-}"
last=$(git tag --list 'v*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true)

bump=patch
if grep -qiE '#major|BREAKING[ -]CHANGE' <<<"$msg"; then
  bump=major
elif grep -qiE '#minor' <<<"$msg"; then
  bump=minor
fi

if [ -z "$last" ]; then
  if [ "$bump" = major ]; then echo v1.0.0; else echo v0.1.0; fi
  exit 0
fi

IFS=. read -r major minor patch <<<"${last#v}"
case "$bump" in
  major) echo "v$((major + 1)).0.0" ;;
  minor) echo "v${major}.$((minor + 1)).0" ;;
  *)     echo "v${major}.${minor}.$((patch + 1))" ;;
esac
