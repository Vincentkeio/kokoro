#!/bin/bash
set -euo pipefail

cd /root
release_assets=(
  kokoro-linux-amd64 kokoro-linux-amd64.sha256
  kokoro-agent-linux-amd64 kokoro-agent-linux-amd64.sha256
  kokoro-linux-arm64 kokoro-linux-arm64.sha256
  kokoro-agent-linux-arm64 kokoro-agent-linux-arm64.sha256
  install.sh install.sh.sha256
  kokoro-bench.sh kokoro-bench.sh.sha256
)

for asset in kokoro-linux-amd64 kokoro-agent-linux-amd64 kokoro-linux-arm64 kokoro-agent-linux-arm64 install.sh kokoro-bench.sh; do
  sha256sum -c "$asset.sha256"
done

systemctl is-active --quiet kokoro-hub
test -x /opt/kokoro/kokoro
test -s /var/lib/kokoro/kokoro.db
free_kb=$(df -Pk /var/lib/kokoro | awk 'NR==2 {print $4}')
test "$free_kb" -gt 524288

ts=$(date +%Y%m%d-%H%M%S)
backup_dir=/var/lib/kokoro/backups
mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
db_backup="$backup_dir/kokoro-preview-$ts.db"
bin_backup="/opt/kokoro/kokoro.bak-$ts"
dist_backup="/root/kokoro-dist-backup-$ts"
mkdir -m 700 "$dist_backup"

systemctl stop kokoro-hub
if command -v sqlite3 >/dev/null 2>&1; then
  sqlite3 /var/lib/kokoro/kokoro.db ".backup '$db_backup'"
else
  cp -a /var/lib/kokoro/kokoro.db "$db_backup"
  for suffix in -wal -shm; do
    if [ -f "/var/lib/kokoro/kokoro.db$suffix" ]; then
      cp -a "/var/lib/kokoro/kokoro.db$suffix" "$db_backup$suffix"
    fi
  done
fi
chmod 600 "$db_backup" "$db_backup"-wal "$db_backup"-shm 2>/dev/null || true
cp -a /opt/kokoro/kokoro "$bin_backup"

dist=/var/lib/kokoro/dist
mkdir -p "$dist"
for asset in "${release_assets[@]}"; do
  case "$asset" in *.sha256) base=${asset%.sha256} ;; *) base=$asset ;; esac
  if [ -f "$dist/$base" ]; then cp -a "$dist/$base" "$dist_backup/"; fi
  if [ -f "$dist/$asset" ]; then cp -a "$dist/$asset" "$dist_backup/"; fi
done

install -o root -g root -m 0755 kokoro-linux-amd64 /opt/kokoro/kokoro.new
mv -f /opt/kokoro/kokoro.new /opt/kokoro/kokoro
for asset in "${release_assets[@]}"; do
  install -o root -g root -m 0644 "$asset" "$dist/$asset.new"
  mv -f "$dist/$asset.new" "$dist/$asset"
done
chown -R kokoro:kokoro "$dist"

rollback() {
  systemctl stop kokoro-hub || true
  install -o root -g root -m 0755 "$bin_backup" /opt/kokoro/kokoro
  for asset in "${release_assets[@]}"; do
    rm -f "$dist/$asset.new"
    if [ -f "$dist_backup/$asset" ]; then
      install -o kokoro -g kokoro -m 0644 "$dist_backup/$asset" "$dist/$asset"
    else
      rm -f "$dist/$asset"
    fi
  done
  cp -a "$db_backup" /var/lib/kokoro/kokoro.db
  rm -f /var/lib/kokoro/kokoro.db-wal /var/lib/kokoro/kokoro.db-shm
  if [ -f "$db_backup-wal" ]; then cp -a "$db_backup-wal" /var/lib/kokoro/kokoro.db-wal; fi
  if [ -f "$db_backup-shm" ]; then cp -a "$db_backup-shm" /var/lib/kokoro/kokoro.db-shm; fi
  chown kokoro:kokoro /var/lib/kokoro/kokoro.db*
  systemctl start kokoro-hub || true
}

if ! systemctl start kokoro-hub; then rollback; exit 1; fi
healthy=0
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if systemctl is-active --quiet kokoro-hub && curl -fsS --max-time 10 http://127.0.0.1:8799/ -o /tmp/kokoro-preview-home.html; then
    healthy=1
    break
  fi
  sleep 2
done
if [ "$healthy" -ne 1 ]; then
  rollback
  exit 1
fi

if ! grep -q 'style.css?v=236' /tmp/kokoro-preview-home.html; then
  echo 'ERROR: home page did not serve the expected CSS version' >&2
  rollback
  exit 1
fi

echo "hub=$(systemctl is-active kokoro-hub) version=$(/opt/kokoro/kokoro version 2>&1 | head -n1)"
echo "sha256=$(sha256sum /opt/kokoro/kokoro | cut -d' ' -f1)"
echo "home_http=200 css=200 db_backup=$db_backup"
ls -1t /opt/kokoro/kokoro.bak-* 2>/dev/null | tail -n +4 | xargs -r rm -f
ls -1t "$backup_dir"/kokoro-preview-*.db 2>/dev/null | tail -n +4 | xargs -r rm -f
rm -rf "$dist_backup"
rm -f /root/kokoro-linux-amd64 /root/kokoro-linux-amd64.sha256 \
  /root/kokoro-agent-linux-amd64 /root/kokoro-agent-linux-amd64.sha256 \
  /root/kokoro-linux-arm64 /root/kokoro-linux-arm64.sha256 \
  /root/kokoro-agent-linux-arm64 /root/kokoro-agent-linux-arm64.sha256 \
  /root/install.sh /root/install.sh.sha256 /root/kokoro-bench.sh /root/kokoro-bench.sh.sha256 \
  /root/kokoro-preview-deploy.sh /tmp/kokoro-preview-home.html
