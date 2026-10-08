#!/bin/sh
# Kokoro agent 一键安装脚本（纯 POSIX sh，不依赖 bash）
#
# 典型用法（Hub 后台生成的命令就是这一行）:
#   curl -fsSL https://vps.mjfuns.lat/i/it_XXXXXXXX | sh
#
# 本地用法:
#   sh install.sh --hub https://vps.mjfuns.lat --token it_XXXXXXXX
#   sh install.sh --hub https://vps.mjfuns.lat --token it_XXXXXXXX --interval 5000 --name hk-01
#   sh install.sh --upgrade
#   sh install.sh --uninstall
#
# 说明:
#   - 重复执行等同升级: 停服务 -> 换二进制 -> 启服务，配置缺省沿用旧值。
#   - 制品优先从 Hub 的 /api/v1/dl/ 拉取，失败或指定 --github 时回退 GitHub Release。
#   - 大陆机器可设 GH_PROXY=https://ghproxy.net/ 之类的前缀镜像。

set -e

# ---- 默认值 ----

HUB=""
TOKEN=""
INTERVAL=""
NODE_NAME=""
NO_SERVICE=0
UPGRADE=0
UNINSTALL=0
INSECURE=0
USE_GITHUB=0

GH_REPO="${GH_REPO:-kokoro-probe/kokoro}"
GH_PROXY="${GH_PROXY:-}"
GH_TAG="${GH_TAG:-latest}"

BIN_DIR="/usr/local/bin"
BIN_NAME="kokoro-agent"
BIN_PATH="$BIN_DIR/$BIN_NAME"
CONF_DIR="/etc/kokoro"
CONF_FILE="$CONF_DIR/config.toml"
SERVICE_NAME="kokoro-agent"
SYSTEMD_UNIT="/etc/systemd/system/$SERVICE_NAME.service"
OPENRC_INIT="/etc/init.d/$SERVICE_NAME"

OS=""
ARCH=""
ASSET=""
TMP_DIR=""

# ---- 基础工具 ----

if [ -t 1 ]; then
    C_RESET="\033[0m"; C_RED="\033[31m"; C_GREEN="\033[32m"
    C_YELLOW="\033[33m"; C_BLUE="\033[34m"
else
    C_RESET=""; C_RED=""; C_GREEN=""; C_YELLOW=""; C_BLUE=""
fi

log()  { printf '%s\n' "$*"; }
info() { printf '%s[info]%s %s\n' "$C_BLUE" "$C_RESET" "$*"; }
ok()   { printf '%s[ ok ]%s %s\n' "$C_GREEN" "$C_RESET" "$*"; }
warn() { printf '%s[warn]%s %s\n' "$C_YELLOW" "$C_RESET" "$*" >&2; }
die()  { printf '%s[fail]%s %s\n' "$C_RED" "$C_RESET" "$*" >&2; exit 1; }

need_cmd() {
    command -v "$1" >/dev/null 2>&1 || die "缺少必要命令: $1"
}

usage() {
    cat <<EOF
Kokoro agent 安装脚本

用法:
  curl -fsSL https://<hub>/i/<install_token> | sh
  sh install.sh --hub <hub地址> --token <install_token> [选项]
  sh install.sh --upgrade [选项]
  sh install.sh --uninstall

选项:
  --hub <url>        Hub 地址，例如 https://vps.mjfuns.lat（末尾斜杠会被去掉）
  --token <token>    安装令牌 it_xxx；升级时可省略，沿用已有配置
  --interval <ms>    上报间隔（毫秒），默认 2000
  --name <name>      节点名，默认取主机名
  --no-service       只装二进制和配置，不注册 systemd/OpenRC
  --upgrade          显式升级（默认行为就是升级，此选项只是为了语义清晰）
  --uninstall        卸载：停服务、删 unit、删二进制、删配置（配置先备份）
  --insecure         自签 CA / 证书不可信场景临时放行（等价 curl -k）
  --github           跳过 Hub 分发，直接从 GitHub Release 下载
  -h, --help         显示本帮助

环境变量:
  GH_REPO   GitHub 仓库，默认 kokoro-probe/kokoro
  GH_TAG     Release 标签，默认 latest
  GH_PROXY   下载前缀镜像，例如 https://ghproxy.net/
EOF
}

cleanup() {
    if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
        rm -rf "$TMP_DIR"
    fi
}

# ---- 参数解析 ----

while [ $# -gt 0 ]; do
    case "$1" in
        --hub)        [ $# -ge 2 ] || die "--hub 需要一个参数"; HUB="$2"; shift 2 ;;
        --hub=*)      HUB="${1#--hub=}"; shift ;;
        --token)      [ $# -ge 2 ] || die "--token 需要一个参数"; TOKEN="$2"; shift 2 ;;
        --token=*)    TOKEN="${1#--token=}"; shift ;;
        --interval)   [ $# -ge 2 ] || die "--interval 需要一个参数"; INTERVAL="$2"; shift 2 ;;
        --interval=*) INTERVAL="${1#--interval=}"; shift ;;
        --name)       [ $# -ge 2 ] || die "--name 需要一个参数"; NODE_NAME="$2"; shift 2 ;;
        --name=*)     NODE_NAME="${1#--name=}"; shift ;;
        --no-service) NO_SERVICE=1; shift ;;
        --upgrade)    UPGRADE=1; shift ;;
        --uninstall)  UNINSTALL=1; shift ;;
        --insecure)   INSECURE=1; shift ;;
        --github)     USE_GITHUB=1; shift ;;
        -h|--help)    usage; exit 0 ;;
        *)            die "未知参数: $1（用 -h 查看用法）" ;;
    esac
done

# 去掉 Hub 地址末尾的斜杠
while [ -n "$HUB" ]; do
    case "$HUB" in
        */) HUB="${HUB%/}" ;;
        *)  break ;;
    esac
done

# ---- 平台探测 ----

detect_os() {
    _u="$(uname -s)"
    case "$_u" in
        Linux)                    OS="linux" ;;
        Darwin)                   OS="darwin" ;;
        MINGW*|MSYS*|CYGWIN*|Windows_NT) OS="windows" ;;
        *) die "暂不支持的操作系统: $_u" ;;
    esac
}

detect_arch() {
    _m="$(uname -m)"
    case "$_m" in
        x86_64|amd64)          ARCH="amd64" ;;
        aarch64|arm64|armv8*)  ARCH="arm64" ;;
        armv7l|armv7|armhf)    ARCH="armv7" ;;
        armv6l|armv6)          ARCH="armv6" ;;
        i386|i486|i586|i686)   ARCH="386" ;;
        *) die "暂不支持的 CPU 架构: $_m" ;;
    esac
}

# ---- 权限 ----

check_root() {
    if [ "$(id -u)" = "0" ]; then
        SUDO=""
        return
    fi
    if command -v sudo >/dev/null 2>&1; then
        SUDO="sudo"
        info "当前非 root，将使用 sudo 执行需要权限的步骤"
        if ! sudo -n true 2>/dev/null; then
            warn "sudo 可能需要交互式输入密码；建议直接用 root 运行本脚本"
        fi
    else
        die "需要 root 权限（或安装 sudo）才能安装到 $BIN_DIR 与 $CONF_DIR；" \
            "请以 root 身份重新执行：curl -fsSL https://<hub>/i/<token> | sudo sh"
    fi
}

as_root() {
    if [ -n "$SUDO" ]; then
        sudo "$@"
    else
        "$@"
    fi
}

# ---- 下载 ----

CURL=""
WGET=""

init_downloader() {
    if command -v curl >/dev/null 2>&1; then
        CURL="curl"
    elif command -v wget >/dev/null 2>&1; then
        WGET="wget"
    else
        die "既没有 curl 也没有 wget，无法下载 agent，请先安装其中之一"
    fi
}

# fetch <url> <dest>：成功返回 0，失败返回非 0
fetch() {
    _url="$1"
    _dest="$2"
    if [ -n "$CURL" ]; then
        if [ "$INSECURE" = "1" ]; then
            "$CURL" -fsSL -k --retry 3 --connect-timeout 15 -o "$_dest" "$_url" && return 0
        else
            "$CURL" -fsSL --retry 3 --connect-timeout 15 -o "$_dest" "$_url" && return 0
        fi
    fi
    if [ -n "$WGET" ]; then
        if [ "$INSECURE" = "1" ]; then
            "$WGET" -q --no-check-certificate -O "$_dest" "$_url" && return 0
        else
            "$WGET" -q -O "$_dest" "$_url" && return 0
        fi
    fi
    return 1
}

sha256_of() {
    _f="$1"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$_f" | awk '{print $1}'
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 "$_f" | awk '{print $1}'
    elif command -v openssl >/dev/null 2>&1; then
        openssl dgst -sha256 "$_f" | awk '{print $NF}'
    else
        die "找不到 sha256sum / shasum / openssl，无法校验制品完整性"
    fi
}

lower() {
    printf '%s' "$1" | tr 'ABCDEFGHIJKLMNOPQRSTUVWXYZ' 'abcdefghijklmnopqrstuvwxyz'
}

# 下载制品与其 sha256，成功后把二进制留在 $TMP_DIR/$ASSET
download_agent() {
    _ok=0
    if [ "$USE_GITHUB" = "0" ] && [ -n "$HUB" ]; then
        _base="$HUB/api/v1/dl/$ASSET"
        info "从 Hub 下载: $_base"
        if fetch "$_base" "$TMP_DIR/$ASSET"; then
            if fetch "$_base.sha256" "$TMP_DIR/$ASSET.sha256"; then
                _ok=1
            else
                warn "Hub 未提供 $ASSET.sha256，跳过校验（不推荐生产使用）"
                _ok=1
            fi
        else
            warn "Hub 下载失败，将回退到 GitHub Release"
        fi
    fi

    if [ "$_ok" = "0" ]; then
        if [ -z "$GH_REPO" ]; then
            die "没有可用的下载源（Hub 不可用且未设置 GH_REPO）"
        fi
        if [ "$GH_TAG" = "latest" ]; then
            _gh="https://github.com/$GH_REPO/releases/latest/download/$ASSET"
            _gh_sha="https://github.com/$GH_REPO/releases/latest/download/$ASSET.sha256"
        else
            _gh="https://github.com/$GH_REPO/releases/download/$GH_TAG/$ASSET"
            _gh_sha="https://github.com/$GH_REPO/releases/download/$GH_TAG/$ASSET.sha256"
        fi
        if [ -n "$GH_PROXY" ]; then
            _gh="$GH_PROXY$_gh"
            _gh_sha="$GH_PROXY$_gh_sha"
        fi
        info "从 GitHub Release 下载: $_gh"
        fetch "$_gh" "$TMP_DIR/$ASSET" || die "GitHub 下载失败，请检查网络或设置 GH_PROXY 镜像"
        fetch "$_gh_sha" "$TMP_DIR/$ASSET.sha256" \
            && _ok=1 \
            || warn "GitHub 未提供 $ASSET.sha256，跳过校验（不推荐生产使用）"
        _ok=1
    fi

    # 校验 sha256：失败绝不留下半截文件
    if [ -f "$TMP_DIR/$ASSET.sha256" ]; then
        _want="$(awk '{print $1}' "$TMP_DIR/$ASSET.sha256" | head -n 1)"
        _got="$(sha256_of "$TMP_DIR/$ASSET")"
        if [ -z "$_want" ]; then
            die "sha256 文件为空或格式异常，拒绝安装"
        fi
        if [ "$(lower "$_want")" != "$(lower "$_got")" ]; then
            rm -f "$TMP_DIR/$ASSET"
            die "sha256 校验失败：期望 $_want，实际 $_got（已删除下载的临时文件）"
        fi
        ok "sha256 校验通过: $_got"
    fi

    [ -s "$TMP_DIR/$ASSET" ] || die "下载的制品为空，拒绝安装"
}

# ---- 配置 ----

# conf_get <key>：从已有配置里读一个值，不存在则输出空
conf_get() {
    [ -f "$CONF_FILE" ] || return 0
    sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//p" "$CONF_FILE" \
        | head -n 1 \
        | sed 's/^"//; s/"$//; s/[[:space:]]*$//'
}

load_existing_conf() {
    if [ ! -f "$CONF_FILE" ]; then
        return 0
    fi
    [ -n "$HUB" ]       || HUB="$(conf_get hub)"
    [ -n "$TOKEN" ]     || TOKEN="$(conf_get token)"
    [ -n "$INTERVAL" ]  || INTERVAL="$(conf_get interval_ms)"
    [ -n "$NODE_NAME" ] || NODE_NAME="$(conf_get node_name)"
    if [ "$(conf_get insecure)" = "true" ] && [ "$INSECURE" = "0" ]; then
        INSECURE=1
    fi
}

write_config() {
    as_root mkdir -p "$CONF_DIR"
    as_root chmod 0700 "$CONF_DIR"

    _tmp="$TMP_DIR/config.toml"
    {
        printf '# Kokoro agent 配置\n'
        printf '# 由 scripts/install.sh 生成，修改后执行 systemctl restart kokoro-agent 生效\n'
        printf 'hub = "%s"\n' "$HUB"
        printf 'token = "%s"\n' "$TOKEN"
        printf 'interval_ms = %s\n' "$INTERVAL"
        if [ -n "$NODE_NAME" ]; then
            printf 'node_name = "%s"\n' "$NODE_NAME"
        fi
        if [ "$INSECURE" = "1" ]; then
            printf 'insecure = true\n'
        else
            printf 'insecure = false\n'
        fi
    } > "$_tmp"

    if [ -f "$CONF_FILE" ] && [ "$(cat "$CONF_FILE")" != "$(cat "$_tmp")" ]; then
        _bak="$CONF_FILE.bak.$(date +%Y%m%d%H%M%S)"
        as_root cp "$CONF_FILE" "$_bak"
        info "旧配置已备份到 $_bak"
    fi
    as_root cp "$_tmp" "$CONF_FILE"
    as_root chmod 0600 "$CONF_FILE"
    ok "配置已写入 $CONF_FILE"
}

# ---- 服务管理 ----

have_systemd() {
    command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]
}

have_openrc() {
    [ -x /sbin/rc-service ] || command -v rc-service >/dev/null 2>&1
}

service_stop() {
    if have_systemd; then
        systemctl stop "$SERVICE_NAME" 2>/dev/null || true
    elif have_openrc; then
        rc-service "$SERVICE_NAME" stop 2>/dev/null || true
    else
        pkill -x "$BIN_NAME" 2>/dev/null || true
    fi
}

install_binary() {
    as_root mkdir -p "$BIN_DIR"
    if [ -f "$BIN_PATH" ]; then
        as_root rm -f "$BIN_PATH"
    fi
    if command -v install >/dev/null 2>&1; then
        as_root install -m 0755 "$TMP_DIR/$ASSET" "$BIN_PATH"
    else
        as_root cp "$TMP_DIR/$ASSET" "$BIN_PATH"
        as_root chmod 0755 "$BIN_PATH"
    fi
    ok "二进制已安装到 $BIN_PATH"
}

write_systemd_unit() {
    _unit="$TMP_DIR/$SERVICE_NAME.service"
    cat > "$_unit" <<EOF
[Unit]
Description=Kokoro Agent (server probe)
Documentation=https://github.com/$GH_REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
ExecStart=$BIN_PATH run --config $CONF_FILE
Restart=always
RestartSec=5
KillSignal=SIGINT
TimeoutStopSec=10

# 安全加固
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true
ReadWritePaths=$CONF_DIR

LimitNOFILE=65535
StandardOutput=journal
StandardError=journal
SyslogIdentifier=$SERVICE_NAME

[Install]
WantedBy=multi-user.target
EOF
    as_root cp "$_unit" "$SYSTEMD_UNIT"
    as_root chmod 0644 "$SYSTEMD_UNIT"
    as_root systemctl daemon-reload >/dev/null 2>&1 || true
    as_root systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 || true
    as_root systemctl restart "$SERVICE_NAME" \
        || die "systemd 启动 $SERVICE_NAME 失败，请看 journalctl -u $SERVICE_NAME -n 50"
    ok "已注册并启动 systemd 服务 $SERVICE_NAME"
}

write_openrc_init() {
    _init="$TMP_DIR/$SERVICE_NAME.initd"
    cat > "$_init" <<EOF
#!/sbin/openrc-run
# Kokoro Agent (server probe)

description="Kokoro Agent"
command="$BIN_PATH"
command_args="run --config $CONF_FILE"
command_background="true"
pidfile="/run/$SERVICE_NAME.pid"
output_log="/var/log/$SERVICE_NAME.log"
error_log="/var/log/$SERVICE_NAME.log"

depend() {
    need net
    after firewall
}

start_pre() {
    checkpath --file --mode 0600 "$CONF_FILE"
}
EOF
    as_root cp "$_init" "$OPENRC_INIT"
    as_root chmod 0755 "$OPENRC_INIT"
    as_root rc-update add "$SERVICE_NAME" default >/dev/null 2>&1 || true
    as_root rc-service "$SERVICE_NAME" restart || die "OpenRC 启动 $SERVICE_NAME 失败"
    ok "已注册并启动 OpenRC 服务 $SERVICE_NAME"
}

start_with_nohup() {
    _log="/var/log/$SERVICE_NAME.log"
    as_root mkdir -p /var/log
    as_root sh -c "nohup $BIN_PATH run --config $CONF_FILE >> $_log 2>&1 &"
    warn "本机既没有 systemd 也没有 OpenRC，已用 nohup 后台启动；重启后不会自动拉起。"
    warn "日志: tail -f $_log（如需开机自启，请自行加入 /etc/rc.local 或 crontab @reboot）"
}

register_service() {
    if [ "$NO_SERVICE" = "1" ]; then
        info "--no-service：跳过服务注册"
        return 0
    fi
    if have_systemd; then
        write_systemd_unit
    elif have_openrc; then
        write_openrc_init
    else
        start_with_nohup
    fi
}

print_status() {
    log ""
    log "---------------------------------------------"
    if [ "$NO_SERVICE" = "1" ]; then
        ok "安装完成（未注册服务）"
        log "手动启动: $BIN_PATH run --config $CONF_FILE"
        return 0
    fi
    if have_systemd; then
        _st="$(systemctl is-active "$SERVICE_NAME" 2>/dev/null || true)"
        if [ "$_st" = "active" ]; then
            ok "$SERVICE_NAME 正在运行"
        else
            warn "$SERVICE_NAME 状态: ${_st:-unknown}"
        fi
        log "查看状态: systemctl status $SERVICE_NAME"
        log "实时日志: journalctl -u $SERVICE_NAME -f -n 50"
    elif have_openrc; then
        rc-service "$SERVICE_NAME" status || true
        log "实时日志: tail -f /var/log/$SERVICE_NAME.log"
    else
        log "实时日志: tail -f /var/log/$SERVICE_NAME.log"
    fi
    log "配置文件: $CONF_FILE"
    log "卸载命令: sh install.sh --uninstall"
    log "---------------------------------------------"
}

# ---- 卸载 ----

do_uninstall() {
    info "开始卸载 $SERVICE_NAME"
    if have_systemd; then
        as_root systemctl stop "$SERVICE_NAME" 2>/dev/null || true
        as_root systemctl disable "$SERVICE_NAME" 2>/dev/null || true
        as_root rm -f "$SYSTEMD_UNIT"
        as_root systemctl daemon-reload >/dev/null 2>&1 || true
        as_root systemctl reset-failed "$SERVICE_NAME" 2>/dev/null || true
        info "已移除 systemd 单元"
    elif have_openrc; then
        as_root rc-service "$SERVICE_NAME" stop 2>/dev/null || true
        rc-update del "$SERVICE_NAME" default >/dev/null 2>&1 || true
        as_root rm -f "$OPENRC_INIT"
        info "已移除 OpenRC 服务"
    else
        pkill -x "$BIN_NAME" 2>/dev/null || true
    fi

    if [ -f "$CONF_FILE" ]; then
        _bak="$CONF_FILE.bak.$(date +%Y%m%d%H%M%S)"
        as_root cp "$CONF_FILE" "$_bak"
        as_root rm -f "$CONF_FILE"
        ok "配置已备份到 $_bak 并删除原文件"
    else
        info "没有找到配置文件 $CONF_FILE"
    fi

    if [ -f "$BIN_PATH" ]; then
        as_root rm -f "$BIN_PATH"
        ok "已删除 $BIN_PATH"
    else
        info "没有找到 $BIN_PATH"
    fi

    ok "卸载完成（$CONF_DIR 目录保留，如需彻底清理：rm -rf $CONF_DIR）"
}

# ---- 主流程 ----

main() {
    init_downloader

    if [ "$UNINSTALL" = "1" ]; then
        check_root
        do_uninstall
        exit 0
    fi

    detect_os
    detect_arch
    ASSET="$BIN_NAME-$OS-$ARCH"
    if [ "$OS" = "windows" ]; then
        ASSET="$ASSET.exe"
    fi

    load_existing_conf

    [ -n "$HUB" ] || die "未指定 Hub 地址，请用 --hub https://<你的域名>（升级时也可从已有配置读取）"
    [ -n "$TOKEN" ] || die "未指定安装令牌，请用 --token it_xxxx（升级时也可从已有配置读取）"
    case "$HUB" in
        http://*|https://*) ;;
        *) die "Hub 地址必须以 http:// 或 https:// 开头: $HUB" ;;
    esac

    if [ -z "$INTERVAL" ]; then
        INTERVAL=2000
    fi
    case "$INTERVAL" in
        ''|*[!0-9]*) die "--interval 必须是正整数（毫秒）: $INTERVAL" ;;
    esac
    [ "$INTERVAL" -ge 200 ] || die "--interval 不能小于 200 毫秒"

    if [ -z "$NODE_NAME" ]; then
        NODE_NAME="$(uname -n 2>/dev/null || hostname 2>/dev/null || echo kokoro-node)"
    fi
    # TOML 字符串里不允许裸引号与反斜杠
    NODE_NAME="$(printf '%s' "$NODE_NAME" | tr -d '"\\')"

    check_root

    if [ -f "$BIN_PATH" ] && [ "$UPGRADE" = "0" ]; then
        info "检测到已安装的 $BIN_PATH，本次将按升级处理"
    fi

    TMP_DIR="$(mktemp -d 2>/dev/null || true)"
    if [ -z "$TMP_DIR" ]; then
        TMP_DIR="${TMPDIR:-/tmp}/kokoro-install.$$"
        mkdir -p "$TMP_DIR"
    fi
    trap cleanup EXIT HUP INT TERM
    chmod 0700 "$TMP_DIR"

    info "目标平台: $OS/$ARCH  制品: $ASSET"
    download_agent

    if [ "$OS" = "windows" ]; then
        # Windows 没有 systemd，只放二进制并提示
        as_root mkdir -p "$CONF_DIR"
        as_root cp "$TMP_DIR/$ASSET" "$CONF_DIR/$ASSET"
        write_config
        ok "Windows 仅安装二进制与配置：$CONF_DIR/$ASSET"
        log "请以管理员身份运行: $CONF_DIR\\$ASSET run --config $CONF_FILE"
        exit 0
    fi

    service_stop
    install_binary
    write_config
    register_service
    print_status
}

main "$@"
