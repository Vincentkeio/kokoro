#!/usr/bin/env bash
set -euo pipefail
export PYTHONIOENCODING=utf-8
source "$(dirname -- "$0")/kokoro-bench.sh"
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT
python3() { python "$@"; }
have() { return 0; }
fetch_verified() {
 if [[ "$1" == */ip.sh ]]; then
  cat > "$3" <<'MOCK'
#!/usr/bin/env bash
# curl $CurlARG -sL "${rawgithub}main/ref/dnsbl.list"
family=""; output=""
while [ $# -gt 0 ]; do
 case "$1" in -4) family=4;; -6) family=6;; -o) output=$2; shift;; esac
 shift
done
echo "$family" >> "$KOKORO_TEST_CALLS"
if [ "$family" = 6 ] && [ "${KOKORO_TEST_FAIL6:-0}" = 1 ]; then exit 60; fi
printf '{"Info":{"Organization":"Private 2001:db8::7 / 192.0.2.9"},"Score":{"SCAMALYTICS":%s},"Media":{"Netflix":{"Status":"解锁"}}}' "$family" > "$output"
MOCK
 else printf 'safe.example\n' > "$3"; fi
}
export KOKORO_TEST_CALLS="$test_dir/calls"
if ! test_ip > "$test_dir/results"; then exit 1; fi
python3 - "$test_dir/results" "$test_dir/calls" <<'PY'
import json,sys,pathlib
events=[json.loads(s) for s in pathlib.Path(sys.argv[1]).read_text(encoding='utf-8').splitlines()]
results=[x for x in events if x['event']=='result']
assert [x['test'] for x in results]==['ip4','ip6'],results
assert [x['data']['risk_scamalytics'] for x in results]==[4,6]
assert pathlib.Path(sys.argv[2]).read_text().split()==['4','6']
assert '2001:db8' not in str(results) and '192.0.2.9' not in str(results)
PY
export KOKORO_TEST_FAIL6=1
if ! test_ip > "$test_dir/partial"; then exit 1; fi
python3 - "$test_dir/partial" <<'PY'
import json,sys,pathlib
r=[json.loads(s) for s in pathlib.Path(sys.argv[1]).read_text(encoding='utf-8').splitlines()]
r=[x for x in r if x['event']=='result']
assert r[0]['ok'] and r[0]['test']=='ip4'
assert not r[1]['ok'] and r[1]['test']=='ip6',r
PY
printf 'IPv4 / IPv6 isolation, partial failure and IP redaction passed.\n'
