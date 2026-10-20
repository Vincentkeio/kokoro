#!/bin/bash
set -euo pipefail

cd /root
hub_asset=kokoro-linux-amd64
dist_assets=(
  install.sh install.sh.sha256
  kokoro-bench.sh kokoro-bench.sh.sha256
)

for asset in "$hub_asset" "${dist_assets[@]}"; do
  case "$asset" in *.sha256) continue ;; esac
  sha256sum -c "$asset.sha256"
done

# 这个脚本不只升级：面板不在（或已被卸载）时直接把面板装上。
# 复用官方安装器 + 本地发布包，仍然逐个校验 SHA-256，只是不访问 GitHub。
if [ -f /root/deploy.env ]; then
  . /root/deploy.env
fi
DOMAIN="${KOKORO_DOMAIN:-kokoro.mjfuns.lat}"
ADMIN_USER="${KOKORO_ADMIN_USER:-admin}"
case "$DOMAIN" in *[!A-Za-z0-9.-]*|'') echo "ERROR: 面板域名不合法：$DOMAIN" >&2; exit 1 ;; esac
if [ ! -x /opt/kokoro/kokoro ] || [ ! -s /var/lib/kokoro/kokoro.db ]; then
  echo "== 未检测到已安装的面板，改为全新安装（域名 $DOMAIN）=="
  [ -f /root/install-hub.sh ] || { echo 'ERROR: 缺少 install-hub.sh，无法安装面板' >&2; exit 1; }
  sh /root/install-hub.sh --domain "$DOMAIN" --admin-user "$ADMIN_USER" --assets-dir /root
  echo "hub=$(systemctl is-active kokoro-hub) version=$(/opt/kokoro/kokoro version 2>&1 | head -n1)"
  echo "面板安装完成；管理员密码见上面安装器输出（只会显示一次）。"
  rm -f /root/kokoro-linux-amd64 /root/kokoro-linux-amd64.sha256 \
    /root/kokoro-agent-linux-amd64 /root/kokoro-agent-linux-amd64.sha256 \
    /root/kokoro-agent-linux-arm64 /root/kokoro-agent-linux-arm64.sha256 \
    /root/kokoro-linux-arm64 /root/kokoro-linux-arm64.sha256 \
    /root/install.sh /root/install.sh.sha256 /root/kokoro-bench.sh /root/kokoro-bench.sh.sha256 \
    /root/install-hub.sh /root/deploy.env
  exit 0
fi

systemctl is-active --quiet kokoro-hub
test -x /opt/kokoro/kokoro
test -s /var/lib/kokoro/kokoro.db
free_kb=$(df -Pk /var/lib/kokoro | awk 'NR==2 {print $4}')
test "$free_kb" -gt 524288

# Do not keep rollback copies of Hub or database, including files from older deployers.
rm -f /opt/kokoro/kokoro.bak-* /var/lib/kokoro/backups/kokoro-preview-*.db \
  /var/lib/kokoro/backups/kokoro-preview-*.db-wal /var/lib/kokoro/backups/kokoro-preview-*.db-shm \
  /var/lib/kokoro/backups/hub-update-*.db /var/lib/kokoro/backups/hub-update-*.db-wal \
  /var/lib/kokoro/backups/hub-update-*.db-shm /root/kokoro-dist-backup-*

systemctl stop kokoro-hub

dist=/var/lib/kokoro/dist
mkdir -p "$dist"

install -o root -g root -m 0755 "$hub_asset" /opt/kokoro/kokoro.new
mv -f /opt/kokoro/kokoro.new /opt/kokoro/kokoro
for asset in "${dist_assets[@]}"; do
  install -o root -g root -m 0644 "$asset" "$dist/$asset.new"
  mv -f "$dist/$asset.new" "$dist/$asset"
done
chown -R kokoro:kokoro "$dist"

install -d -o kokoro -g kokoro -m 0750 /var/lib/kokoro/update-requests
cat >/etc/systemd/system/kokoro-update.service <<'EOF'
[Unit]
Description=Kokoro verified panel self-update
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=/opt/kokoro/kokoro apply-update-request --request=/var/lib/kokoro/update-requests/hub.json --state=/var/lib/kokoro/web-update-hub.json --data=/var/lib/kokoro --health=http://127.0.0.1:8799/
TimeoutStartSec=15min
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/opt/kokoro /var/lib/kokoro
NoNewPrivileges=true
EOF
cat >/etc/systemd/system/kokoro-update.path <<'EOF'
[Unit]
Description=Watch for a verified Kokoro panel update request

[Path]
PathExists=/var/lib/kokoro/update-requests/hub.json
Unit=kokoro-update.service

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now kokoro-update.path

if ! systemctl start kokoro-hub; then
  echo 'ERROR: Hub 启动失败；未保留旧程序或数据库副本。' >&2
  exit 1
fi
healthy=0
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  if systemctl is-active --quiet kokoro-hub && curl -fsS --max-time 10 http://127.0.0.1:8799/ -o /tmp/kokoro-preview-home.html; then
    healthy=1
    break
  fi
  sleep 2
done
if [ "$healthy" -ne 1 ]; then
  echo 'ERROR: Hub 健康检查失败；未保留旧程序或数据库副本。' >&2
  exit 1
fi

if ! grep -q 'style.css?v=262' /tmp/kokoro-preview-home.html; then
  echo 'ERROR: home page did not serve the expected CSS version; no backups were retained.' >&2
  exit 1
fi

# Hub/Agent executables are fetched directly from GitHub; dist only keeps Hub-served scripts.
rm -f "$dist"/kokoro-linux-* "$dist"/kokoro-agent-linux-*

echo "hub=$(systemctl is-active kokoro-hub) version=$(/opt/kokoro/kokoro version 2>&1 | head -n1)"
echo "sha256=$(sha256sum /opt/kokoro/kokoro | cut -d' ' -f1)"
echo "home_http=200 css=200"
rm -f /root/kokoro-linux-amd64 /root/kokoro-linux-amd64.sha256 \
  /root/kokoro-agent-linux-amd64 /root/kokoro-agent-linux-amd64.sha256 \
  /root/kokoro-agent-linux-arm64 /root/kokoro-agent-linux-arm64.sha256 \
  /root/kokoro-linux-arm64 /root/kokoro-linux-arm64.sha256 \
  /root/install.sh /root/install.sh.sha256 /root/kokoro-bench.sh /root/kokoro-bench.sh.sha256 \
  /root/install-hub.sh /root/deploy.env /root/kokoro-preview-deploy.sh /tmp/kokoro-preview-home.html
