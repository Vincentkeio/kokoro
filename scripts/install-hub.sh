#!/bin/sh
# Install Kokoro Hub on a clean Debian/Ubuntu host.
set -eu

REPO="Vincentkeio/kokoro"
RELEASE_TAG="v0.1.3-preview.35"
INSTALL_DIR="/opt/kokoro"
DATA_DIR="/var/lib/kokoro"
SERVICE_FILE="/etc/systemd/system/kokoro-hub.service"
CADDY_FILE="/etc/caddy/Caddyfile"
DOMAIN=""
ADMIN_USER="admin"
HAS_TTY=0
TMP_DIR=""
GENERATED_PASSWORD=0
NGINX_MODE=0
NGINX_BIN=""
NGINX_VHOST_DIR=""
NGINX_CONFIG_FILE=""
INSTALL_SUCCEEDED=0
CADDY_STORAGE="/var/lib/caddy/kokoro-caddy"
MENU_PATH="/usr/local/bin/kokoro"
MENU_PREVIOUS_TARGET=""
MENU_LINK_CHANGED=0

fail() { printf '错误：%s\n' "$*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }

usage() {
  cat <<'EOF'
Kokoro Hub 从零安装器（Debian / Ubuntu）

用法：install-hub.sh [--domain panel.example.com] [--admin-user 用户名]

未指定 --domain 时，安装器会交互询问面板域名。
要求：全新主机、systemd、域名 DNS 已指向本机，入站 TCP 80/443 可达。
安装器配置 Kokoro 和 systemd 服务；无 Nginx 时由 Caddy 提供 HTTPS，有 Nginx 时保留 Nginx 作为公网入口。
安装器不会改动系统防火墙。
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --domain)
      [ "$#" -ge 2 ] || fail "--domain 需要参数"
      DOMAIN=$2; shift 2 ;;
    --admin-user)
      [ "$#" -ge 2 ] || fail "--admin-user 需要参数"
      ADMIN_USER=$2; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "未知选项：$1（使用 --help 查看用法）" ;;
  esac
done

if ( : 2>/dev/null </dev/tty >/dev/tty ); then
  HAS_TTY=1
fi

[ "$(id -u)" -eq 0 ] || fail "请以 root 身份运行，例如：curl ... | sudo sh"
if [ -z "$DOMAIN" ]; then
  if [ "$HAS_TTY" -eq 1 ]; then
    printf '请输入面板域名（例如 panel.example.com）：' >/dev/tty
    IFS= read -r DOMAIN </dev/tty || true
    [ -n "$DOMAIN" ] || fail "面板域名不能为空"
  else
    fail "当前没有可交互终端；请在命令中指定 --domain panel.example.com"
  fi
fi
printf '%s\n' "$DOMAIN" | awk -F. 'NF < 2 { exit 1 } { for (i = 1; i <= NF; i++) if (length($i) > 63 || $i !~ /^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$/) exit 1 }' \
  || fail "域名格式无效：$DOMAIN"
case "$DOMAIN" in *[A-Za-z]*) ;; *) fail "请提供可用于 HTTPS 证书的 DNS 域名，不要填 IP 地址" ;; esac
case "$ADMIN_USER" in
  ''|*[!A-Za-z0-9_.@-]*) fail "管理员用户名仅可包含字母、数字、点、下划线、@ 和连字符" ;;
esac
[ "${#ADMIN_USER}" -le 64 ] || fail "管理员用户名不能超过 64 个字符"

[ -r /etc/os-release ] || fail "无法识别系统发行版"
. /etc/os-release
case "${ID:-}" in debian|ubuntu) ;; *) fail "仅支持 Debian 和 Ubuntu，当前系统：${PRETTY_NAME:-未知}" ;; esac
command -v systemctl >/dev/null 2>&1 || fail "需要 systemd"
command -v apt-get >/dev/null 2>&1 || fail "需要 apt-get"

if [ -e "$INSTALL_DIR/kokoro" ] || [ -e "$SERVICE_FILE" ]; then
  fail "检测到已有 Kokoro Hub 安装；本脚本仅用于从零安装，不会覆盖或升级现有数据"
fi
if [ -e "$INSTALL_DIR/data" ] || [ -L "$INSTALL_DIR/data" ]; then
  fail "检测到已有 Hub 数据路径 $INSTALL_DIR/data；为避免覆盖残留数据，安装已停止"
fi
if [ -e "$CADDY_FILE" ] || [ -L "$CADDY_FILE" ]; then
  DEFAULT_CADDY_CONFIG=0
  if [ ! -L "$CADDY_FILE" ] \
    && grep -Fq 'The Caddyfile is an easy way to configure your Caddy web server.' "$CADDY_FILE" \
    && awk '
      /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
      /^[[:space:]]*:80[[:space:]]*\{/ { blocks++; next }
      /^[[:space:]]*\}/ { next }
      {
        line=$0
        sub(/^[[:space:]]+/, "", line)
        sub(/[[:space:]]+$/, "", line)
        if (line != "root * /usr/share/caddy" && line != "file_server") invalid=1
      }
      END { exit !(blocks == 1 && !invalid) }
    ' "$CADDY_FILE"; then
    DEFAULT_CADDY_CONFIG=1
  fi
  if [ "$DEFAULT_CADDY_CONFIG" -ne 1 ]; then
    fail "检测到已有 Caddy 配置：$CADDY_FILE。为避免影响其他站点，请先为 Kokoro 准备独立主机，或手动合并反代配置"
  fi
fi
if [ -e "$MENU_PATH" ] || [ -L "$MENU_PATH" ]; then
  [ -L "$MENU_PATH" ] || fail "$MENU_PATH 已存在且不是软链接；为避免覆盖未知程序，安装已停止"
  MENU_PREVIOUS_TARGET=$(readlink "$MENU_PATH")
  case "$MENU_PREVIOUS_TARGET" in
    /usr/local/bin/kokoro-agent|/opt/kokoro/kokoro|/www/wwwroot/kokoro/kokoro) ;;
    *) fail "$MENU_PATH 指向非 Kokoro 程序（$MENU_PREVIOUS_TARGET）；为避免覆盖，安装已停止" ;;
  esac
fi
if command -v ss >/dev/null 2>&1; then
  PORT_OWNERS=$(ss -H -ltnp 2>/dev/null | awk '
    $4 ~ /(^|:)(80|443)$/ {
      if ($0 ~ /"nginx"/) nginx=1
      else if ($0 !~ /"caddy"/) other=1
    }
    END { printf "%d %d", nginx, other }
  ')
  set -- $PORT_OWNERS
  if [ "${2:-0}" -eq 1 ]; then
    fail "TCP 80 或 443 被非 Nginx 服务占用。请先确认端口用途，避免影响现有网站"
  fi
  if [ "${1:-0}" -eq 1 ]; then
    NGINX_MODE=1
    if command -v nginx >/dev/null 2>&1; then
      NGINX_BIN=$(command -v nginx)
    elif [ -x /www/server/nginx/sbin/nginx ]; then
      NGINX_BIN=/www/server/nginx/sbin/nginx
    else
      fail "检测到 Nginx 正监听 80/443，但找不到 nginx 管理程序"
    fi
    NGINX_CONFIG_FILES=$("$NGINX_BIN" -T 2>&1 | awk '/^# configuration file / { sub(/^# configuration file /, ""); sub(/:$/, ""); print }')
    if printf '%s\n' "$NGINX_CONFIG_FILES" | grep -Fq '/www/server/panel/vhost/nginx/'; then
      NGINX_VHOST_DIR=/www/server/panel/vhost/nginx
    elif printf '%s\n' "$NGINX_CONFIG_FILES" | grep -Fq '/etc/nginx/sites-enabled/'; then
      NGINX_VHOST_DIR=/etc/nginx/sites-enabled
    elif printf '%s\n' "$NGINX_CONFIG_FILES" | grep -Fq '/etc/nginx/conf.d/'; then
      NGINX_VHOST_DIR=/etc/nginx/conf.d
    else
      fail "检测到 Nginx，但无法确认安全的虚拟主机配置目录；没有修改现有配置"
    fi
    [ -d "$NGINX_VHOST_DIR" ] || fail "Nginx 虚拟主机目录不存在：$NGINX_VHOST_DIR"
    printf '%s\n' "$NGINX_CONFIG_FILES" | while IFS= read -r config_path; do
      [ -n "$config_path" ] || continue
      if awk -v domain="$DOMAIN" '
        /^[[:space:]]*server_name[[:space:]]/ {
          for (i=2; i<=NF; i++) { gsub(/[;,]/, "", $i); if ($i == domain) found=1 }
        }
        END { exit !found }
      ' "$config_path" 2>/dev/null; then
        printf '发现已有 Nginx 域名配置：%s\n' "$config_path" >&2
        exit 1
      fi
    done || fail "该域名已在 Nginx 配置中使用；为避免覆盖现有网站，安装已停止"
    NGINX_CONFIG_FILE="$NGINX_VHOST_DIR/kokoro-hub-$DOMAIN.conf"
    [ ! -e "$NGINX_CONFIG_FILE" ] || fail "Nginx 配置文件已存在：$NGINX_CONFIG_FILE"
    info "检测到 Nginx：保留 Nginx 作为公网入口，Caddy 将使用本地备用端口管理证书"
  fi
fi

TMP_DIR=$(mktemp -d)
cleanup() {
  if [ "$HAS_TTY" -eq 1 ] && command -v stty >/dev/null 2>&1; then stty echo </dev/tty 2>/dev/null || true; fi
  if [ "$INSTALL_SUCCEEDED" -ne 1 ] && [ -n "$NGINX_CONFIG_FILE" ] && [ -e "$NGINX_CONFIG_FILE" ]; then
    rm -f "$NGINX_CONFIG_FILE"
    [ -z "$NGINX_BIN" ] || "$NGINX_BIN" -s reload >/dev/null 2>&1 || true
  fi
  if [ "$INSTALL_SUCCEEDED" -ne 1 ] && [ "$NGINX_MODE" -eq 1 ]; then
    systemctl disable --now kokoro-nginx-reload.path >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/kokoro-nginx-reload.service /etc/systemd/system/kokoro-nginx-reload.path
    systemctl daemon-reload >/dev/null 2>&1 || true
  fi
  if [ "$INSTALL_SUCCEEDED" -ne 1 ] && [ "$MENU_LINK_CHANGED" -eq 1 ]; then
    if [ -n "$MENU_PREVIOUS_TARGET" ]; then
      ln -sfn "$MENU_PREVIOUS_TARGET" "$MENU_PATH"
    else
      rm -f "$MENU_PATH"
    fi
  fi
  [ -z "$TMP_DIR" ] || rm -rf "$TMP_DIR"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

ARCH=$(uname -m)
case "$ARCH" in
  x86_64|amd64) HUB_ARCH=amd64 ;;
  aarch64|arm64) HUB_ARCH=arm64 ;;
  *) fail "暂不支持 CPU 架构：$ARCH（支持 amd64、arm64）" ;;
esac
ASSET="kokoro-linux-$HUB_ARCH"

info "安装系统依赖和官方 Caddy 软件源"
export DEBIAN_FRONTEND=noninteractive
apt-get update
apt-get install -y ca-certificates curl gnupg debian-keyring debian-archive-keyring apt-transport-https openssl

download_verified() {
  asset=$1
  base="https://github.com/$REPO/releases/download/$RELEASE_TAG"
  curl -fL --retry 3 --proto '=https' --tlsv1.2 "$base/$asset" -o "$TMP_DIR/$asset" \
    || fail "下载 $asset 失败（Release $RELEASE_TAG）"
  curl -fL --retry 3 --proto '=https' --tlsv1.2 "$base/$asset.sha256" -o "$TMP_DIR/$asset.sha256" \
    || fail "Release 缺少 $asset.sha256 校验文件"
  expected=$(awk 'NR == 1 {print $1}' "$TMP_DIR/$asset.sha256")
  case "$expected" in *[!0-9a-fA-F]*|'') fail "$asset SHA-256 格式错误" ;; esac
  [ "${#expected}" -eq 64 ] || fail "$asset SHA-256 长度错误"
  printf '%s  %s\n' "$expected" "$TMP_DIR/$asset" | sha256sum -c - >/dev/null \
    || fail "$asset SHA-256 校验失败"
}

info "下载并校验 Kokoro Hub、Agent 和安装资源（Release $RELEASE_TAG）"
download_verified "$ASSET"
for TARGET_ARCH in amd64 arm64; do
  download_verified "kokoro-agent-linux-$TARGET_ARCH"
done
download_verified install.sh
download_verified kokoro-bench.sh

info "安装 Caddy"
install -d -m 0755 /usr/share/keyrings
curl -fsSL --proto '=https' --tlsv1.2 https://dl.cloudsmith.io/public/caddy/stable/gpg.key \
  | gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
chmod 0644 /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -fsSL --proto '=https' --tlsv1.2 https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt \
  -o /etc/apt/sources.list.d/caddy-stable.list
apt-get update
apt-get install -y caddy
install -d -o caddy -g caddy -m 0750 "$CADDY_STORAGE"

if [ "$HAS_TTY" -eq 1 ]; then
  printf '管理员用户名 [%s]：' "$ADMIN_USER" >/dev/tty
  IFS= read -r INPUT_USER </dev/tty || true
  [ -z "${INPUT_USER:-}" ] || ADMIN_USER=$INPUT_USER
  case "$ADMIN_USER" in ''|*[!A-Za-z0-9_.@-]*) fail "管理员用户名格式无效" ;; esac
  [ "${#ADMIN_USER}" -le 64 ] || fail "管理员用户名不能超过 64 个字符"
  printf '设置管理员密码（至少 8 位）：' >/dev/tty
  stty -echo </dev/tty
  IFS= read -r ADMIN_PASSWORD </dev/tty || true
  stty echo </dev/tty
  printf '\n再次输入管理员密码：' >/dev/tty
  stty -echo </dev/tty
  IFS= read -r ADMIN_PASSWORD_CONFIRM </dev/tty || true
  stty echo </dev/tty
  printf '\n' >/dev/tty
  [ "$ADMIN_PASSWORD" = "${ADMIN_PASSWORD_CONFIRM:-}" ] || fail "两次输入的密码不一致"
  [ "${#ADMIN_PASSWORD}" -ge 8 ] || fail "管理员密码至少 8 位"
else
  ADMIN_PASSWORD=$(openssl rand -hex 24)
  GENERATED_PASSWORD=1
  info "未连接交互终端，已生成随机管理员凭据；安装结束时只显示一次"
fi

info "安装 Kokoro Hub 和 systemd 服务"
install -d -m 0755 "$INSTALL_DIR" "$DATA_DIR/dist"
ln -s "$DATA_DIR" "$INSTALL_DIR/data"
if ! id kokoro >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/kokoro --no-create-home --shell /usr/sbin/nologin kokoro
fi
install -o root -g root -m 0755 "$TMP_DIR/$ASSET" "$INSTALL_DIR/kokoro.new"
mv -f "$INSTALL_DIR/kokoro.new" "$INSTALL_DIR/kokoro"
install -o root -g root -m 0644 "$TMP_DIR/install.sh" "$DATA_DIR/dist/install.sh"
install -o root -g root -m 0644 "$TMP_DIR/install.sh.sha256" "$DATA_DIR/dist/install.sh.sha256"
install -o root -g root -m 0644 "$TMP_DIR/kokoro-bench.sh" "$DATA_DIR/dist/kokoro-bench.sh"
for TARGET_ARCH in amd64 arm64; do
  AGENT_ASSET="kokoro-agent-linux-$TARGET_ARCH"
  install -o root -g root -m 0644 "$TMP_DIR/$AGENT_ASSET" "$DATA_DIR/dist/$AGENT_ASSET"
  install -o root -g root -m 0644 "$TMP_DIR/$AGENT_ASSET.sha256" "$DATA_DIR/dist/$AGENT_ASSET.sha256"
done
set_data_ownership() {
  chown kokoro:kokoro "$DATA_DIR"
  find "$DATA_DIR" -mindepth 1 -maxdepth 1 ! -name swapfile ! -name swapfile.new ! -name swapfile.old \
    -exec chown -hR kokoro:kokoro {} +
  for LEGACY_SWAP in "$DATA_DIR/swapfile" "$DATA_DIR/swapfile.new" "$DATA_DIR/swapfile.old"; do
    if [ -f "$LEGACY_SWAP" ] && [ ! -L "$LEGACY_SWAP" ]; then
      chown root:root "$LEGACY_SWAP"
      chmod 0600 "$LEGACY_SWAP"
    fi
  done
}
set_data_ownership
touch "$INSTALL_DIR/.managed-by-kokoro-installer"

cat >"$SERVICE_FILE" <<EOF
[Unit]
Description=Kokoro Hub
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
# Managed-By=Kokoro-Installer
Type=simple
User=kokoro
Group=kokoro
WorkingDirectory=$INSTALL_DIR
Environment=KOKORO_DATA_DIR=$DATA_DIR
ExecStart=$INSTALL_DIR/kokoro serve --listen 127.0.0.1:8799 --data $DATA_DIR --domain $DOMAIN --tls proxy
Restart=always
RestartSec=5
KillSignal=SIGINT
TimeoutStopSec=15
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true
CapabilityBoundingSet=
ReadWritePaths=$DATA_DIR
ReadOnlyPaths=$DATA_DIR/dist
InaccessiblePaths=-$DATA_DIR/swapfile -$DATA_DIR/swapfile.new -$DATA_DIR/swapfile.old -/var/lib/kokoro/swapfile -/var/lib/kokoro/swapfile.new -/var/lib/kokoro/swapfile.old
LimitNOFILE=65535
StandardOutput=journal
StandardError=journal
SyslogIdentifier=kokoro-hub

[Install]
WantedBy=multi-user.target
EOF

install -d -o kokoro -g kokoro -m 0750 "$DATA_DIR/update-requests"
cat > /etc/systemd/system/kokoro-update.service <<EOF
[Unit]
Description=Kokoro verified panel self-update
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=$INSTALL_DIR/kokoro apply-update-request --request=$DATA_DIR/update-requests/hub.json --state=$DATA_DIR/web-update-hub.json --data=$DATA_DIR --health=http://127.0.0.1:8799/
TimeoutStartSec=15min
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=$INSTALL_DIR $DATA_DIR
NoNewPrivileges=true
EOF
cat > /etc/systemd/system/kokoro-update.path <<EOF
[Unit]
Description=Watch for a verified Kokoro panel update request

[Path]
PathExists=$DATA_DIR/update-requests/hub.json
Unit=kokoro-update.service

[Install]
WantedBy=multi-user.target
EOF

"$INSTALL_DIR/kokoro" passwd --if-unset --data "$DATA_DIR" --user "$ADMIN_USER" --password "$ADMIN_PASSWORD"
set_data_ownership

if [ -e "$CADDY_FILE" ] || [ -L "$CADDY_FILE" ]; then
  CADDY_BACKUP=$(mktemp "$CADDY_FILE.kokoro-backup.XXXXXX")
  cp -a "$CADDY_FILE" "$CADDY_BACKUP"
  info "已备份原 Caddy 配置：$CADDY_BACKUP"
  [ ! -L "$CADDY_FILE" ] || rm -f "$CADDY_FILE"
fi
if [ "$NGINX_MODE" -eq 1 ]; then
  CADDY_CERT_DIR="$CADDY_STORAGE/certificates/acme-v02.api.letsencrypt.org-directory/$DOMAIN"
  CADDY_CERT="$CADDY_CERT_DIR/$DOMAIN.crt"
  CADDY_KEY="$CADDY_CERT_DIR/$DOMAIN.key"
  cat >"$CADDY_FILE" <<EOF
# Managed-By=Kokoro-Installer
{
  storage file_system $CADDY_STORAGE
  http_port 9080
  https_port 9443
  default_bind 127.0.0.1
}

https://$DOMAIN {
  bind 127.0.0.1
  tls {
    issuer acme {
      alt_http_port 9080
      disable_tlsalpn_challenge
    }
  }
  reverse_proxy 127.0.0.1:8799 {
    flush_interval -1
  }
}

http://$DOMAIN {
  bind 127.0.0.1
}
EOF
  cat >"$NGINX_CONFIG_FILE" <<EOF
# Managed-By=Kokoro-Installer
server {
  listen 80;
  listen [::]:80;
  server_name $DOMAIN;

  location ^~ /.well-known/acme-challenge/ {
    proxy_pass http://127.0.0.1:9080;
    proxy_set_header Host \$host;
  }

  location / {
    return 301 https://\$host\$request_uri;
  }
}
EOF
  "$NGINX_BIN" -t || fail "Nginx 临时 ACME 路由校验失败；已恢复原配置"
  "$NGINX_BIN" -s reload || fail "无法加载 Nginx 临时 ACME 路由；请检查 Nginx 状态"
else
  cat >"$CADDY_FILE" <<EOF
# Managed-By=Kokoro-Installer
{
  storage file_system $CADDY_STORAGE
}

$DOMAIN {
  reverse_proxy 127.0.0.1:8799 {
    flush_interval -1
  }
}
EOF
fi

chmod 0644 "$SERVICE_FILE" "$CADDY_FILE"
systemctl daemon-reload
systemctl enable --now kokoro-update.path
systemctl enable --now kokoro-hub
systemctl enable caddy
caddy validate --config "$CADDY_FILE" --adapter caddyfile
systemctl reload caddy || systemctl restart caddy

if [ "$NGINX_MODE" -eq 1 ]; then
  info "等待 Caddy 通过 Nginx HTTP 路由申请 HTTPS 证书"
  attempt=0
  until [ -s "$CADDY_CERT" ] && [ -s "$CADDY_KEY" ] && openssl x509 -in "$CADDY_CERT" -noout -checkhost "$DOMAIN" >/dev/null 2>&1; do
    curl -ksf --resolve "$DOMAIN:9443:127.0.0.1" "https://$DOMAIN:9443/" >/dev/null 2>&1 || true
    attempt=$((attempt + 1))
    [ "$attempt" -lt 180 ] || fail "证书申请超时；查看 journalctl -u caddy，当前 Nginx 的 80/443 服务保持运行"
    sleep 1
  done

  NGINX_TLS_LISTEN="ssl"
  if "$NGINX_BIN" -V 2>&1 | grep -q -- '--with-http_v2_module'; then
    NGINX_TLS_LISTEN="ssl http2"
  fi
  cat >"$NGINX_CONFIG_FILE" <<EOF
# Managed-By=Kokoro-Installer
server {
  listen 80;
  listen [::]:80;
  server_name $DOMAIN;

  location ^~ /.well-known/acme-challenge/ {
    proxy_pass http://127.0.0.1:9080;
    proxy_set_header Host \$host;
  }

  location / {
    return 301 https://\$host\$request_uri;
  }
}

server {
  listen 443 $NGINX_TLS_LISTEN;
  listen [::]:443 $NGINX_TLS_LISTEN;
  server_name $DOMAIN;

  ssl_certificate $CADDY_CERT;
  ssl_certificate_key $CADDY_KEY;

  location / {
    proxy_pass http://127.0.0.1:8799;
    proxy_http_version 1.1;
    proxy_set_header Host \$host;
    proxy_set_header X-Real-IP \$remote_addr;
    proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto https;
    proxy_set_header Connection "";
    proxy_buffering off;
    proxy_read_timeout 3600s;
  }
}
EOF
  "$NGINX_BIN" -t || fail "Kokoro Nginx 站点配置校验失败；已恢复原配置"
  "$NGINX_BIN" -s reload || fail "无法加载 Kokoro Nginx 站点配置"

  cat >/etc/systemd/system/kokoro-nginx-reload.service <<EOF
[Unit]
Description=Reload Nginx after Kokoro TLS certificate renewal

[Service]
Type=oneshot
ExecStart=$NGINX_BIN -t
ExecStart=$NGINX_BIN -s reload
EOF
  cat >/etc/systemd/system/kokoro-nginx-reload.path <<EOF
[Unit]
Description=Watch Kokoro TLS certificate managed by Caddy

[Path]
PathChanged=$CADDY_CERT
Unit=kokoro-nginx-reload.service

[Install]
WantedBy=multi-user.target
EOF
  chmod 0644 /etc/systemd/system/kokoro-nginx-reload.service /etc/systemd/system/kokoro-nginx-reload.path
  systemctl daemon-reload
  systemctl enable --now kokoro-nginx-reload.path
fi

info "等待 Hub 服务启动"
attempt=0
until curl -fsS http://127.0.0.1:8799/ >/dev/null; do
  attempt=$((attempt + 1))
  [ "$attempt" -lt 20 ] || fail "Hub 启动失败；查看日志：journalctl -u kokoro-hub -n 100 --no-pager"
  sleep 1
done

info "安装 Kokoro 快捷管理命令"
install -d -m 0755 /usr/local/bin /usr/local/lib/kokoro
ln -sfn "$INSTALL_DIR/kokoro" "$MENU_PATH"
MENU_LINK_CHANGED=1
"$MENU_PATH" version >/dev/null 2>&1 || fail "kokoro 快捷命令检查失败"
cat >/usr/local/lib/kokoro/uninstall-menu.sh <<'MENU_EOF'
#!/bin/sh
set -eu
[ "$(id -u)" = "0" ] || { echo "请以 root 身份运行此脚本。" >&2; exit 1; }
printf '确认删除快捷命令和 Kokoro 管理脚本？服务与程序不会卸载。（输入 yes 确认）：'
IFS= read -r answer || answer=""
[ "$answer" = yes ] || { echo "已取消。"; exit 0; }
menu=/usr/local/bin/kokoro
if [ -L "$menu" ]; then rm -f "$menu"; elif [ -e "$menu" ]; then echo "$menu 不是软链接，为避免误删已保留。" >&2; exit 1; fi
rm -f /usr/local/lib/kokoro/install.sh /usr/local/lib/kokoro/uninstall-menu.sh
rmdir /usr/local/lib/kokoro 2>/dev/null || true
echo "已移除 Kokoro 快捷命令和管理脚本；服务与程序仍保留。"
MENU_EOF
chmod 0755 /usr/local/lib/kokoro/uninstall-menu.sh

cat <<EOF

Kokoro Hub 安装完成
面板地址：https://$DOMAIN
服务状态：systemctl status kokoro-hub $([ "$NGINX_MODE" -eq 1 ] && printf 'nginx caddy' || printf 'caddy')
日志查看：journalctl -u kokoro-hub -f
安装目录：$INSTALL_DIR
数据目录：$DATA_DIR
管理员用户名：$ADMIN_USER
EOF
INSTALL_SUCCEEDED=1
if [ "$GENERATED_PASSWORD" -eq 1 ]; then
  printf '管理员密码（请立即保存）：%s\n' "$ADMIN_PASSWORD"
fi
unset ADMIN_PASSWORD ADMIN_PASSWORD_CONFIRM 2>/dev/null || true
if [ "$NGINX_MODE" -eq 1 ]; then
  printf 'Nginx 保持现有网站服务并代理 Kokoro；Caddy 在本地备用端口管理证书，续期后会自动重载 Nginx。\n'
else
  printf '请确认 DNS 已指向本机，并允许 TCP 80/443 入站；Caddy 将自动申请 HTTPS 证书并长期提供面板服务。\n'
fi
