#!/usr/bin/env bash
set -euo pipefail
source "$(dirname -- "$0")/kokoro-bench.sh"
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT
fixture="$test_dir/original"
printf 'verified executable bytes\n' > "$fixture"
expected=$(sha256sum "$fixture" | awk '{print $1}')
curl() {
  local dest=""
  while [ $# -gt 0 ]; do
    if [ "$1" = -o ]; then dest=$2; shift 2; else shift; fi
  done
  cp -- "$fixture" "$dest"
}
fetch_verified 'https://example.invalid/tool' "$expected" "$test_dir/good"
cmp -- "$fixture" "$test_dir/good"
printf 'untrusted executable bytes\n' > "$fixture"
if fetch_verified 'https://example.invalid/tool' "$expected" "$test_dir/bad"; then
  echo 'ERROR: altered executable accepted' >&2; exit 1
fi
[ ! -e "$test_dir/bad" ] && [ ! -e "$test_dir/bad.part" ]
curl() { return 42; }
if fetch_verified 'https://example.invalid/tool' "$expected" "$test_dir/unavailable"; then
  echo 'ERROR: failed download accepted' >&2; exit 1
fi
[ ! -e "$test_dir/unavailable" ] && [ ! -e "$test_dir/unavailable.part" ]
printf 'Verified downloads accept intact bytes and reject altered or unavailable code.\n'
