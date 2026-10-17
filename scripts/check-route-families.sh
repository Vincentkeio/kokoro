#!/usr/bin/env bash
set -euo pipefail
export PYTHONIOENCODING=utf-8
source "$(dirname -- "$0")/kokoro-bench.sh"
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT
export KOKORO_ROUTE_CALLS="$test_dir/calls"
python3() { python "$@"; }
have() { return 0; }
ip() { [ "${KOKORO_NO_V6:-0}" != 1 ]; }
timeout() { shift 3; "$@"; }
nexttrace() {
 local family=4 target=""
 for arg in "$@"; do [ "$arg" != -6 ] || family=6; target=$arg; done
 printf '%s %s\n' "$family" "$target" >> "$KOKORO_ROUTE_CALLS"
 echo 'traceroute to fixture'
 if [ "$family" = 6 ]; then echo '1 2001:db8::1 AS4134 中国 上海 10ms'; else echo '1 192.0.2.1 AS4809 中国 上海 10ms'; fi
 echo '2 192.0.2.2 AS4809 中国 广东 20ms'
}
if ! test_route > "$test_dir/report"; then exit 1; fi
python3 - "$test_dir/report" "$test_dir/calls" <<'PY'
import pathlib,json,sys
r=[json.loads(x) for x in pathlib.Path(sys.argv[1]).read_text(encoding='utf-8').splitlines()]
r=[x for x in r if x['event']=='result']
assert [x['test'] for x in r]==['route4','route6'],r
assert 'CN2' in r[0]['data']['电信']['line']
assert '163' in r[1]['data']['电信']['line']
calls=pathlib.Path(sys.argv[2]).read_text().splitlines()
assert len(calls)==18
assert all('-v'+line[0]+'.ip.' in line for line in calls),calls
PY
export KOKORO_NO_V6=1
if ! test_route6 > "$test_dir/unavailable"; then exit 1; fi
python3 - "$test_dir/unavailable" <<'PY'
import json,pathlib,sys
r=json.loads(pathlib.Path(sys.argv[1]).read_text(encoding='utf-8'))
assert r['test']=='route6' and r['ok'] and r['data']['available'] is False,r
PY
printf 'Route protocol isolation, IPv6 targets and unavailable state passed.\n'
