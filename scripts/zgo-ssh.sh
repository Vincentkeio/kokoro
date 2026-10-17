#!/usr/bin/env bash
set -euo pipefail
ap=$(mktemp)
trap 'rm -f "$ap"' EXIT
printf '#!/bin/sh\nprintf "%%s\\n" "$ZGO_PASS"\n' > "$ap"
chmod 700 "$ap"
SSH_ASKPASS="$ap" SSH_ASKPASS_REQUIRE=force DISPLAY=:0 \
  ssh -T -o StrictHostKeyChecking=accept-new -o ConnectTimeout=25 \
    -o NumberOfPasswordPrompts=1 -p "$ZGO_PORT" root@"$ZGO_HOST" 'tr -d "\r" | bash -s'
