#!/bin/sh
# Install Kokoro Hub on a clean Debian/Ubuntu host.
set -eu

REPO="Vincentkeio/kokoro"
RELEASE_TAG="v1.0.2"
INSTALL_DIR="/opt/kokoro"
DATA_DIR="/var/lib/kokoro"
SERVICE_FILE="/etc/systemd/system/kokoro-hub.service"
CADDY_FILE="/etc/caddy/Caddyfile"
DOMAIN=""
ADMIN_USER="admin"
ASSETS_DIR=""
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
# 记录本次运行创建了什么，失败时才能只回滚自己的产物。
EXISTING_DATA=0
CREDENTIALS_EXISTING=0
CADDY_CONFIG_WRITTEN=0
CADDY_BACKUP=""
DATA_LINK_CREATED=0
HUB_FILES_INSTALLED=0
SERVICE_INSTALLED=0
UPDATE_UNITS_INSTALLED=0
# Caddyfile 合并：Kokoro 只占标记之间这一段，别人的配置原样保留。
CADDY_BLOCK_BEGIN='# >>> Kokoro-Installer begin >>>'
CADDY_BLOCK_END='# <<< Kokoro-Installer end <<<'
CADDY_MODE=owned
CADDY_CERT=""
CADDY_KEY=""
CADDY_REMOVE_OWN_CONFIG=0
# Nginx 模式下如果域名已经由现有站点服务，就沿用它，Kokoro 不另建站点。
NGINX_REUSE=0
NGINX_DOMAIN_OWNER=""
NGINX_DOMAIN_CONFLICT=0
CADDY_KOKORO_SITE=1
CADDY_HTTP_PORT=9080
CADDY_HTTPS_PORT=9443

fail() { printf '错误：%s\n' "$*" >&2; exit 1; }
info() { printf '==> %s\n' "$*"; }

usage() {
  cat <<'EOF'
Kokoro Hub 从零安装器（Debian / Ubuntu）

用法：install-hub.sh [--domain panel.example.com] [--admin-user 用户名] [--assets-dir 目录]

未指定 --domain 时，安装器会交互询问面板域名。
要求：全新主机、systemd、域名 DNS 已指向本机，入站 TCP 80/443 可达。
安装器配置 Kokoro 和 systemd 服务；无 Nginx 时由 Caddy 提供 HTTPS，有 Nginx 时保留 Nginx 作为公网入口。
安装器不会改动系统防火墙。

--assets-dir 目录   从本地发布包安装（目录内是 Release 的 12 个制品），不访问 GitHub。
                    校验规则与线上下载完全一致，仍然逐文件校验 SHA-256。
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
    --assets-dir)
      [ "$#" -ge 2 ] || fail "--assets-dir 需要参数"
      ASSETS_DIR=$2; shift 2 ;;
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
[ "${#ADMIN_USER}" -le 32 ] || fail "管理员用户名不能超过 32 个字符（与 kokoro passwd 的限制一致）"

[ -r /etc/os-release ] || fail "无法识别系统发行版"
. /etc/os-release
case "${ID:-}" in debian|ubuntu) ;; *) fail "仅支持 Debian 和 Ubuntu，当前系统：${PRETTY_NAME:-未知}" ;; esac
command -v systemctl >/dev/null 2>&1 || fail "需要 systemd"
command -v apt-get >/dev/null 2>&1 || fail "需要 apt-get"

if [ -e "$INSTALL_DIR/kokoro" ] || [ -e "$SERVICE_FILE" ]; then
  fail "检测到已有 Kokoro Hub 安装；请先运行 kokoro 菜单里的「卸载面板」再重装"
fi
# 上一次安装（或卸载）留下的产物不是"别人的站点"：$INSTALL_DIR/data 指向
# Kokoro 自己的数据目录、Caddyfile 带 Kokoro 标记。卸载后重装不该被它们挡住，
# 所以这两种情况放行；来源无法确认的配置仍然一律拒绝。
if [ -L "$INSTALL_DIR/data" ]; then
  if [ "$(readlink "$INSTALL_DIR/data" 2>/dev/null)" != "$DATA_DIR" ]; then
    fail "$INSTALL_DIR/data 指向 $(readlink "$INSTALL_DIR/data" 2>/dev/null)；为避免覆盖残留数据，安装已停止"
  fi
  EXISTING_DATA=1
  info "检测到上次安装的数据链接 $INSTALL_DIR/data -> $DATA_DIR，将复用原数据目录"
elif [ -e "$INSTALL_DIR/data" ]; then
  fail "检测到已有 Hub 数据路径 $INSTALL_DIR/data；为避免覆盖残留数据，安装已停止"
fi
# 已有 Caddy 配置一律不拦。能确认整份文件都是 Kokoro 自己的（上次安装留下的、
# 或系统自带的欢迎页）就整体重写；其它一律走合并：原内容一个字不动，只把 Kokoro
# 的站点块追加进去，写完用 caddy validate 校验，不过就回滚原文件。
CADDY_MODE=owned
if [ -e "$CADDY_FILE" ] || [ -L "$CADDY_FILE" ]; then
  CADDY_MODE=merge
  if grep -Fq "$CADDY_BLOCK_BEGIN" "$CADDY_FILE" 2>/dev/null; then
    # 已经合并过一次：把旧块摘掉重写，别人的内容继续保留。
    CADDY_MODE=merge
    info "检测到已有 Caddy 配置：保留原有站点，只更新 Kokoro 的站点块"
  elif [ -f "$CADDY_FILE" ] && [ ! -L "$CADDY_FILE" ] \
    && head -n 3 "$CADDY_FILE" | grep -Fq 'Managed-By=Kokoro-Installer'; then
    CADDY_MODE=replace
    info "检测到上次 Kokoro 安装留下的 Caddy 配置，将备份后覆盖"
  elif [ ! -L "$CADDY_FILE" ] \
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
    CADDY_MODE=replace
  else
    info "检测到已有 Caddy 配置：保留原有站点，只追加 Kokoro 站点块"
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
    NGINX_CONFIG_FILE="$NGINX_VHOST_DIR/kokoro-hub-$DOMAIN.conf"
    # 域名已经被别的站点占用时不再报错退出：先看那个站点是不是已经在反代 Kokoro，
    # 是就直接沿用（一个字都不改现有配置），不是就保留它并把该怎么改讲清楚。
    NGINX_DOMAIN_OWNER=""
    NGINX_DOMAIN_CONFLICT=0
    while IFS= read -r config_path; do
      [ -n "$config_path" ] || continue
      [ "$config_path" != "$NGINX_CONFIG_FILE" ] || continue
      awk -v domain="$DOMAIN" '
        /^[[:space:]]*server_name[[:space:]]/ {
          for (i=2; i<=NF; i++) { gsub(/[;,]/, "", $i); if ($i == domain) found=1 }
        }
        END { exit !found }
      ' "$config_path" 2>/dev/null || continue
      NGINX_DOMAIN_OWNER=$config_path
      break
    done <<NGINX_CONFIG_LIST
$NGINX_CONFIG_FILES
NGINX_CONFIG_LIST
    if [ -n "$NGINX_DOMAIN_OWNER" ]; then
      NGINX_REUSE=1
      # 复用已有站点时不再创建 kokoro-hub-*.conf，避免出现两个同 server_name 的
      # 站点让 nginx 只认其中一个。
      NGINX_CONFIG_FILE=""
      if grep -Fq '127.0.0.1:8799' "$NGINX_DOMAIN_OWNER"; then
        info "域名 $DOMAIN 已由 $NGINX_DOMAIN_OWNER 反代到 Kokoro，沿用该站点，不改动现有配置"
      else
        NGINX_DOMAIN_CONFLICT=1
        info "域名 $DOMAIN 已由 $NGINX_DOMAIN_OWNER 使用，Kokoro 没有改动它"
      fi
    fi
    if [ "$NGINX_REUSE" -eq 1 ] && [ "$NGINX_DOMAIN_CONFLICT" -eq 0 ]; then
      # 已有 Nginx 站点直接反代 Hub 时，不需要 Caddy 再为同一域名申请证书。
      # 旧版安装可能留下了 Kokoro Caddy 块，稍后只移除自己的标记块。
      CADDY_KOKORO_SITE=0
      info "现有 Nginx 已直接反代 Kokoro：不再为该域名配置 Caddy 或申请证书"
    fi
    if [ "$CADDY_KOKORO_SITE" -eq 1 ]; then
      info "检测到 Nginx：保留 Nginx 作为公网入口，Caddy 将使用本地备用端口管理证书"
    else
      info "检测到 Nginx：沿用已有站点，不为 Kokoro 配置 Caddy 证书"
    fi
  fi
fi

# 复用已直连 Hub 的 Nginx 时，Caddy 不再需要为 Kokoro 服务。
# 只在发现 Kokoro 自己以前写入的配置时询问；其它 Caddy 站点从不覆盖或改写。
caddy_has_own_config() {
  [ -e "$CADDY_FILE" ] || [ -L "$CADDY_FILE" ] || return 1
  if grep -Fq "$CADDY_BLOCK_BEGIN" "$CADDY_FILE" \
    && grep -Fq "$CADDY_BLOCK_END" "$CADDY_FILE"; then
    return 0
  fi
  [ "$CADDY_MODE" = "replace" ] \
    && head -n 3 "$CADDY_FILE" | grep -Fq 'Managed-By=Kokoro-Installer'
}
if [ "$CADDY_KOKORO_SITE" -eq 0 ] && caddy_has_own_config; then
  if [ "$HAS_TTY" -eq 1 ]; then
    printf '检测到 Kokoro 以前写入的 Caddy 配置。是否移除 Kokoro 自己的配置？同一文件中的其它站点会保留。 [y/N] ' >/dev/tty
    IFS= read -r CADDY_REMOVE_REPLY </dev/tty || true
    case "$CADDY_REMOVE_REPLY" in
      y|Y|yes|YES)
        CADDY_REMOVE_OWN_CONFIG=1
        info "将备份并移除 Kokoro 自己的 Caddy 配置，保留其它站点"
        ;;
      *) info "保留现有 Kokoro Caddy 配置和其它站点" ;;
    esac
  else
    info "检测到 Kokoro 自己的 Caddy 配置；当前没有交互终端，将保留原配置"
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
  # 失败退出时必须把这次写的东西收回去：否则残留的服务文件和程序会让
  # 下一次安装被"检测到已有 Kokoro Hub 安装"挡在门外。
  if [ "$INSTALL_SUCCEEDED" -ne 1 ] && [ "$CADDY_CONFIG_WRITTEN" -eq 1 ]; then
    if [ -n "$CADDY_BACKUP" ] && [ -f "$CADDY_BACKUP" ]; then
      # 还原内容（写入目标可能是软链接，链接本身要留着）。
      cat "$CADDY_BACKUP" > "$CADDY_TARGET" 2>/dev/null || true
      systemctl reload caddy >/dev/null 2>&1 || true
    else
      rm -f "$CADDY_TARGET"
    fi
  fi
  if [ "$INSTALL_SUCCEEDED" -ne 1 ] && [ "$UPDATE_UNITS_INSTALLED" -eq 1 ]; then
    systemctl disable --now kokoro-update.path >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/kokoro-update.service /etc/systemd/system/kokoro-update.path
  fi
  if [ "$INSTALL_SUCCEEDED" -ne 1 ] && [ "$SERVICE_INSTALLED" -eq 1 ]; then
    systemctl disable --now kokoro-hub >/dev/null 2>&1 || true
    rm -f "$SERVICE_FILE"
  fi
  if [ "$INSTALL_SUCCEEDED" -ne 1 ] && [ "$HUB_FILES_INSTALLED" -eq 1 ]; then
    rm -f "$INSTALL_DIR/kokoro" "$INSTALL_DIR/kokoro.new" "$INSTALL_DIR/.managed-by-kokoro-installer"
  fi
  if [ "$INSTALL_SUCCEEDED" -ne 1 ] && [ "$DATA_LINK_CREATED" -eq 1 ]; then
    rm -f "$INSTALL_DIR/data"
  fi
  if [ "$INSTALL_SUCCEEDED" -ne 1 ]; then
    systemctl daemon-reload >/dev/null 2>&1 || true
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
  if [ -n "$ASSETS_DIR" ]; then
    # 本地发布包：制品和校验文件都必须齐全，缺一个就停，
    # 绝不用"没有 sha256 就跳过校验"的方式放行。
    [ -f "$ASSETS_DIR/$asset" ] || fail "本地发布目录缺少 $asset：$ASSETS_DIR"
    [ -f "$ASSETS_DIR/$asset.sha256" ] || fail "本地发布目录缺少 $asset.sha256：$ASSETS_DIR"
    cp -f "$ASSETS_DIR/$asset" "$TMP_DIR/$asset"
    cp -f "$ASSETS_DIR/$asset.sha256" "$TMP_DIR/$asset.sha256"
  else
    base="https://github.com/$REPO/releases/download/$RELEASE_TAG"
    curl -fL --retry 3 --proto '=https' --tlsv1.2 "$base/$asset" -o "$TMP_DIR/$asset" \
      || fail "下载 $asset 失败（Release $RELEASE_TAG）"
    curl -fL --retry 3 --proto '=https' --tlsv1.2 "$base/$asset.sha256" -o "$TMP_DIR/$asset.sha256" \
      || fail "Release 缺少 $asset.sha256 校验文件"
  fi
  expected=$(awk 'NR == 1 {print $1}' "$TMP_DIR/$asset.sha256")
  case "$expected" in *[!0-9a-fA-F]*|'') fail "$asset SHA-256 格式错误" ;; esac
  [ "${#expected}" -eq 64 ] || fail "$asset SHA-256 长度错误"
  printf '%s  %s\n' "$expected" "$TMP_DIR/$asset" | sha256sum -c - >/dev/null \
    || fail "$asset SHA-256 校验失败"
}

if [ -n "$ASSETS_DIR" ]; then
  [ -d "$ASSETS_DIR" ] || fail "本地发布目录不存在：$ASSETS_DIR"
  info "从本地发布包安装：$ASSETS_DIR（逐个校验 SHA-256）"
else
  info "下载并校验当前架构的 Hub 与所需脚本（Release $RELEASE_TAG）"
fi
download_verified "$ASSET"
download_verified install.sh
download_verified kokoro-bench.sh

if [ "$CADDY_KOKORO_SITE" -eq 1 ]; then
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
else
  info "现有 Nginx 站点已直连 Kokoro，跳过 Caddy 安装"
fi

if [ "$HAS_TTY" -eq 1 ]; then
  printf '管理员用户名 [%s]：' "$ADMIN_USER" >/dev/tty
  IFS= read -r INPUT_USER </dev/tty || true
  [ -z "${INPUT_USER:-}" ] || ADMIN_USER=$INPUT_USER
  case "$ADMIN_USER" in ''|*[!A-Za-z0-9_.@-]*) fail "管理员用户名格式无效" ;; esac
  [ "${#ADMIN_USER}" -le 32 ] || fail "管理员用户名不能超过 32 个字符（与 kokoro passwd 的限制一致）"
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
  # 口令是从 /dev/tty 直接读的：终端的括号粘贴等模式可能把 ESC 转义序列一起送进来。
  # 这种口令能存进数据库，却永远无法在登录框里重新输出来，等于把自己锁在门外。
  if printf '%s' "$ADMIN_PASSWORD" | LC_ALL=C grep -q '[[:cntrl:]]'; then
    fail "管理员密码含有控制字符（多半是终端粘贴带入的转义序列）；请手动输入密码，或先关闭终端括号粘贴后再粘贴"
  fi
else
  ADMIN_PASSWORD=$(openssl rand -hex 24)
  GENERATED_PASSWORD=1
  info "未连接交互终端，已生成随机管理员凭据；安装结束时只显示一次"
fi

info "安装 Kokoro Hub 和 systemd 服务"
install -d -m 0755 "$INSTALL_DIR" "$DATA_DIR/dist"
if [ -L "$INSTALL_DIR/data" ]; then
  info "复用上次安装的数据链接 $INSTALL_DIR/data"
else
  ln -s "$DATA_DIR" "$INSTALL_DIR/data"
  DATA_LINK_CREATED=1
fi
if ! id kokoro >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/kokoro --no-create-home --shell /usr/sbin/nologin kokoro
fi
install -o root -g root -m 0755 "$TMP_DIR/$ASSET" "$INSTALL_DIR/kokoro.new"
mv -f "$INSTALL_DIR/kokoro.new" "$INSTALL_DIR/kokoro"
HUB_FILES_INSTALLED=1
install -o root -g root -m 0644 "$TMP_DIR/install.sh" "$DATA_DIR/dist/install.sh"
install -o root -g root -m 0644 "$TMP_DIR/install.sh.sha256" "$DATA_DIR/dist/install.sh.sha256"
install -o root -g root -m 0644 "$TMP_DIR/kokoro-bench.sh" "$DATA_DIR/dist/kokoro-bench.sh"
# Install and update commands fetch Hub/Agent binaries directly from GitHub.
# Keep only the scripts the Hub itself serves; remove binaries left by older installers.
rm -f "$DATA_DIR"/dist/kokoro-agent-linux-* "$DATA_DIR"/dist/kokoro-linux-*
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
SERVICE_INSTALLED=1

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
UPDATE_UNITS_INSTALLED=1

# 复用旧数据目录时库里可能已经有管理员账户：这时 --if-unset 会静默跳过，
# 必须提前告诉操作者，否则他以为刚输入的密码生效了，登录时才发现进不去。
if "$INSTALL_DIR/kokoro" passwd --check --data "$DATA_DIR" 2>/dev/null | grep -qx 'initialized'; then
  CREDENTIALS_EXISTING=1
  info "检测到已有管理员账户：沿用原用户名和密码，本次输入的密码不会生效"
fi
if [ "$CREDENTIALS_EXISTING" -eq 1 ]; then
  GENERATED_PASSWORD=0
else
  "$INSTALL_DIR/kokoro" passwd --if-unset --data "$DATA_DIR" --user "$ADMIN_USER" --password "$ADMIN_PASSWORD"
fi
set_data_ownership

CADDY_TARGET="$CADDY_FILE"
if [ -e "$CADDY_FILE" ] || [ -L "$CADDY_FILE" ]; then
  if [ -L "$CADDY_FILE" ]; then
    CADDY_TARGET=$(readlink -f "$CADDY_FILE" 2>/dev/null || true)
    [ -n "$CADDY_TARGET" ] || CADDY_TARGET="$CADDY_FILE"
  fi
  # 备份内容而不是链接本身：回滚要还原的是文件内容。
  CADDY_BACKUP=$(mktemp "$CADDY_FILE.kokoro-backup.XXXXXX")
  cp "$CADDY_TARGET" "$CADDY_BACKUP"
  info "已备份原 Caddy 配置：$CADDY_BACKUP"
fi

# 只有整份配置都归 Kokoro 时才写全局选项。和别人共用一份 Caddyfile 时绝不能碰
# 全局块：那里的 default_bind / http_port 会连带把别人的站点一起改掉。
caddy_global_block() {
  printf '{\n  storage file_system %s\n' "$CADDY_STORAGE"
  if [ "$NGINX_MODE" -eq 1 ]; then
    printf '  http_port %s\n  https_port %s\n  default_bind 127.0.0.1\n' "$CADDY_HTTP_PORT" "$CADDY_HTTPS_PORT"
  fi
  printf '}\n\n'
}

caddy_site_block() {
  if [ "$NGINX_MODE" -eq 1 ]; then
    if [ "$CADDY_SHARED" -eq 1 ]; then
      # 共用一份配置时没有全局 http_port/https_port，端口和 bind 只能写进站点地址。
      CADDY_SITE_ADDR="https://$DOMAIN:$CADDY_HTTPS_PORT"
      CADDY_HTTP_ADDR="http://$DOMAIN:$CADDY_HTTP_PORT"
    else
      CADDY_SITE_ADDR="https://$DOMAIN"
      CADDY_HTTP_ADDR="http://$DOMAIN"
    fi
    printf '%s {\n  bind 127.0.0.1\n' "$CADDY_SITE_ADDR"
    printf '  tls {\n    issuer acme {\n      alt_http_port %s\n      disable_tlsalpn_challenge\n    }\n  }\n' "$CADDY_HTTP_PORT"
    printf '  reverse_proxy 127.0.0.1:8799 {\n    flush_interval -1\n  }\n}\n\n'
    printf '%s {\n  bind 127.0.0.1\n}\n' "$CADDY_HTTP_ADDR"
  else
    printf '%s {\n  reverse_proxy 127.0.0.1:8799 {\n    flush_interval -1\n  }\n}\n' "$DOMAIN"
  fi
}

# write_caddyfile 把 Kokoro 站点写进 Caddyfile。
#
# 先摘掉上一次留下的 Kokoro 段，再看文件里还剩不剩别人的内容：
# 还剩 -> 只追加站点块，全局选项一个字都不动（共用一份配置）；
# 不剩 -> 这份配置归 Kokoro，连全局块一起重写。
# 安装中途失败时由 EXIT 陷阱还原备份，别人的站点不会停在半成品状态。
write_caddyfile() {
  CADDY_MERGE_TMP="$CADDY_FILE.kokoro-merge.$$"
  CADDY_SHARED=0
  : > "$CADDY_MERGE_TMP"
  if [ -e "$CADDY_FILE" ] || [ -L "$CADDY_FILE" ]; then
    if [ "$CADDY_MODE" = "replace" ]; then
      # 整份文件都是 Kokoro 的（或系统自带的欢迎页）：旧内容整份作废。
      # 不这么做的话，旧版留下的站点块会和新站点块重复，站点地址撞车。
      : > "$CADDY_MERGE_TMP"
    else
      awk -v b="$CADDY_BLOCK_BEGIN" -v e="$CADDY_BLOCK_END" '
        { line = $0; sub(/[[:space:]]+$/, "", line)
          if (line == b) { skip = 1; next }
          if (line == e) { skip = 0; next }
          if (!skip) print }
      ' "$CADDY_FILE" > "$CADDY_MERGE_TMP" || { rm -f "$CADDY_MERGE_TMP"; fail "读取 $CADDY_FILE 失败"; }
      if [ -n "$(tr -d ' \t\r\n' < "$CADDY_MERGE_TMP")" ]; then
        CADDY_SHARED=1
      fi
    fi
  fi
  # 原文件可能没有结尾换行，那样标记行会跟上一行粘在一起。
  CADDY_MERGE_LAST=$(tail -c 1 "$CADDY_MERGE_TMP" 2>/dev/null | od -An -c 2>/dev/null | tr -d ' \n')
  {
    if [ "$CADDY_SHARED" -eq 1 ]; then
      cat "$CADDY_MERGE_TMP"
      [ "$CADDY_MERGE_LAST" = '\n' ] || printf '\n'
    fi
    # 标记之间的整段都归 Kokoro，包括全局选项块：注释可以出现在全局块之前，
    # 而把全局块也放进标记里，卸载时才能干净摘走、不留半个空壳。
    printf '%s\n' "$CADDY_BLOCK_BEGIN"
    printf '# Managed-By=Kokoro-Installer\n'
    if [ "$CADDY_SHARED" -eq 0 ]; then
      caddy_global_block
    fi
    caddy_site_block
    printf '\n%s\n' "$CADDY_BLOCK_END"
  } > "$CADDY_FILE"
  rm -f "$CADDY_MERGE_TMP"
}

# 已有 Nginx 直接服务面板时，只摘除 Kokoro 自己的 Caddy 标记块。
# 如果文件里没有其它内容，就停用只为 Kokoro 安装的 Caddy；共用配置则保留运行。
remove_kokoro_caddy_block() {
  [ -e "$CADDY_FILE" ] || [ -L "$CADDY_FILE" ] || return 0
  if [ "$CADDY_MODE" = "replace" ] \
    && head -n 3 "$CADDY_FILE" | grep -Fq 'Managed-By=Kokoro-Installer'; then
    # 文件头标记确认整份旧配置都由 Kokoro 安装器生成，可以安全移除。
    rm -f "$CADDY_FILE"
    CADDY_CONFIG_WRITTEN=1
    return 0
  fi
  CADDY_MERGE_TMP="$CADDY_FILE.kokoro-merge.$$"
  awk -v b="$CADDY_BLOCK_BEGIN" -v e="$CADDY_BLOCK_END" '
    { line = $0; sub(/[[:space:]]+$/, "", line)
      if (line == b) { skip = 1; next }
      if (line == e) { skip = 0; next }
      if (!skip) print }
  ' "$CADDY_FILE" > "$CADDY_MERGE_TMP" || { rm -f "$CADDY_MERGE_TMP"; fail "读取 $CADDY_FILE 失败"; }
  if [ -n "$(tr -d ' \t\r\n' < "$CADDY_MERGE_TMP")" ]; then
    cat "$CADDY_MERGE_TMP" > "$CADDY_FILE"
  else
    rm -f "$CADDY_FILE"
  fi
  rm -f "$CADDY_MERGE_TMP"
  CADDY_CONFIG_WRITTEN=1
}

if [ "$NGINX_MODE" -eq 1 ] && [ "$NGINX_REUSE" -eq 0 ]; then
  cat >"$NGINX_CONFIG_FILE" <<EOF
# Managed-By=Kokoro-Installer
server {
  listen 80;
  listen [::]:80;
  server_name $DOMAIN;

  location ^~ /.well-known/acme-challenge/ {
    proxy_pass http://127.0.0.1:$CADDY_HTTP_PORT;
    proxy_set_header Host \$host;
  }

  location / {
    return 301 https://\$host\$request_uri;
  }
}
EOF
fi
if [ "$CADDY_KOKORO_SITE" -eq 1 ]; then
  write_caddyfile
  CADDY_CONFIG_WRITTEN=1
fi
if [ "$CADDY_KOKORO_SITE" -eq 1 ] && [ "$CADDY_SHARED" -eq 0 ] && [ "$NGINX_MODE" -eq 1 ]; then
  # 整份配置归 Kokoro 时存储目录是我们指定的，证书路径确定；
  # 共用别人的配置时用不了自定义 storage，证书要去 Caddy 自己的存储里找。
  CADDY_CERT_DIR="$CADDY_STORAGE/certificates/acme-v02.api.letsencrypt.org-directory/$DOMAIN"
  CADDY_CERT="$CADDY_CERT_DIR/$DOMAIN.crt"
  CADDY_KEY="$CADDY_CERT_DIR/$DOMAIN.key"
fi
if [ -n "$NGINX_CONFIG_FILE" ]; then
  "$NGINX_BIN" -t || fail "Nginx 临时 ACME 路由校验失败；已恢复原配置"
  "$NGINX_BIN" -s reload || fail "无法加载 Nginx 临时 ACME 路由；请检查 Nginx 状态"
fi

chmod 0644 "$SERVICE_FILE"
systemctl daemon-reload
systemctl enable --now kokoro-update.path
systemctl enable --now kokoro-hub
if [ "$CADDY_KOKORO_SITE" -eq 1 ]; then
  chmod 0644 "$CADDY_FILE"
  systemctl enable caddy
  caddy validate --config "$CADDY_FILE" --adapter caddyfile
  # 不要盲目 restart：Caddy 已经在跑别人的站点，restart 失败会把它们一起弄下线。
  # 加载失败时退出，由 EXIT 陷阱把原配置还原回去。
  if systemctl is-active --quiet caddy; then
    systemctl reload caddy || fail "Caddy 加载新配置失败；已还原原来的 Caddy 配置"
  else
    systemctl restart caddy || fail "Caddy 启动失败"
  fi
else
  if [ "$CADDY_REMOVE_OWN_CONFIG" -eq 1 ]; then
    remove_kokoro_caddy_block
    if [ -e "$CADDY_FILE" ] || [ -L "$CADDY_FILE" ]; then
      chmod 0644 "$CADDY_FILE"
      if command -v caddy >/dev/null 2>&1; then
        caddy validate --config "$CADDY_FILE" --adapter caddyfile
      fi
      if systemctl is-active --quiet caddy; then
        systemctl reload caddy || fail "清理 Kokoro Caddy 站点失败；已还原原来的 Caddy 配置"
      fi
    else
      systemctl disable --now caddy >/dev/null 2>&1 || true
    fi
  fi
fi

# 找到 Caddy 为这个域名签下的证书。整份配置归 Kokoro 时路径是确定的；
# 合并模式下 Caddy 用的是它自己的存储（不能动人家的全局 storage），逐个候选找。
locate_caddy_cert() {
  if [ -n "$CADDY_CERT" ] && [ -s "$CADDY_CERT" ] && [ -s "$CADDY_KEY" ]; then
    return 0
  fi
  for root in "$CADDY_STORAGE" /var/lib/caddy/.local/share/caddy /var/lib/caddy \
    /root/.local/share/caddy "${HOME:-/root}/.local/share/caddy"; do
    [ -d "$root/certificates" ] || continue
    for dir in "$root"/certificates/*/"$DOMAIN"; do
      if [ -s "$dir/$DOMAIN.crt" ] && [ -s "$dir/$DOMAIN.key" ]; then
        CADDY_CERT="$dir/$DOMAIN.crt"
        CADDY_KEY="$dir/$DOMAIN.key"
        return 0
      fi
    done
  done
  return 1
}

caddy_cert_ready() {
  locate_caddy_cert || return 1
  openssl x509 -in "$CADDY_CERT" -noout -checkhost "$DOMAIN" >/dev/null 2>&1
}

if [ -n "$NGINX_CONFIG_FILE" ]; then
  info "等待 Caddy 通过 Nginx HTTP 路由申请 HTTPS 证书"
  attempt=0
  until caddy_cert_ready; do
    curl -ksf --resolve "$DOMAIN:$CADDY_HTTPS_PORT:127.0.0.1" "https://$DOMAIN:$CADDY_HTTPS_PORT/" >/dev/null 2>&1 || true
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
    proxy_pass http://127.0.0.1:$CADDY_HTTP_PORT;
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
elif [ "$NGINX_REUSE" -eq 1 ]; then
  info "沿用现有 Nginx 站点：没有生成 Kokoro 站点配置和证书联动单元"
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
EOF
if [ "$CREDENTIALS_EXISTING" -eq 1 ]; then
  cat <<'EOF'
管理员账户：沿用安装前已有的账户（本次输入的用户名和密码没有生效）
如需换成本次输入的凭据，在 VPS 运行 kokoro，选择「重置唯一管理员账户」，
或执行：kokoro passwd --user 新用户名 --password '新密码'
EOF
else
  printf '管理员用户名：%s\n' "$ADMIN_USER"
fi
if [ "$EXISTING_DATA" -eq 1 ]; then
  printf '数据目录：%s（沿用了安装前的数据，节点和文章都还在）\n' "$DATA_DIR"
fi
if [ "$CADDY_SHARED" -eq 1 ]; then
  printf 'Caddy：已把 Kokoro 站点合并进现有的 %s；原有站点保持不动，原文件备份在 %s\n' "$CADDY_FILE" "$CADDY_BACKUP"
fi
if [ "$NGINX_DOMAIN_CONFLICT" -eq 1 ]; then
  printf '⚠ 域名 %s 已由 %s 服务，Kokoro 没有改动它，所以面板可能还打不开。\n' "$DOMAIN" "$NGINX_DOMAIN_OWNER"
  printf '  要指向面板：把那个站点的 location / 反代到 http://127.0.0.1:8799，然后执行 nginx -s reload。\n'
fi
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
