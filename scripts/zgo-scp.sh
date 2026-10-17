#!/usr/bin/env bash
set -euo pipefail
if [ "$#" -ne 2 ]; then
  echo "usage: zgo-scp.sh LOCAL_FILE REMOTE_PATH" >&2
  exit 2
fi
ap=$(mktemp)
trap 'rm -f "$ap"' EXIT
printf '#!/bin/sh\nprintf "%%s\\n" "$ZGO_PASS"\n' > "$ap"
chmod 700 "$ap"
SSH_ASKPASS="$ap" SSH_ASKPASS_REQUIRE=force DISPLAY=:0 \
  scp -o StrictHostKeyChecking=accept-new -o ConnectTimeout=25 \
    -o NumberOfPasswordPrompts=1 -P "$ZGO_PORT" "$1" "root@$ZGO_HOST:$2"
