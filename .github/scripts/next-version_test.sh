#!/usr/bin/env bash
# Tests next-version.sh in a throwaway git repository.
set -euo pipefail
script="$(cd "$(dirname "$0")" && pwd)/next-version.sh"
dir=$(mktemp -d)
trap 'rm -rf "$dir"' EXIT
cd "$dir"
git init -q
git -c user.name=t -c user.email=t@example.com commit -q --allow-empty -m init

fail=0
check() { # message expected
  got=$("$script" "$1")
  if [ "$got" = "$2" ]; then echo "ok   $2"; else echo "FAIL got $got, want $2 (message: $1)"; fail=1; fi
}
check "Merge pull request #1" v0.1.0
check "Big change #major" v1.0.0
git tag v0.1.0; git tag v0.9.0; git tag v0.10.2; git tag v2.0.0-rc.1; git tag not-a-version
check $'Merge pull request #7 from a/b\n\nFix typo' v0.10.3
check "Add feature #minor" v0.11.0
check $'feat!: x\n\nBREAKING CHANGE: api' v1.0.0
check "#MAJOR" v1.0.0
check "" v0.10.3
exit $fail
