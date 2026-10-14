#!/usr/bin/env bash
#
# kokoro-bench.sh —— Kokoro 探针的一键测试脚本
#
# 为什么不用社区现成的一键脚本（YABS / IPQuality / 融合怪）？
#   它们的输出是**给人看的彩色终端**：转圈动画、进度条、赞助商广告，
#   JSON 夹在中间，而且字段结构随版本变。探针要的是机器可读的结果，
#   硬解析它们等于逆着设计走 —— 实测抓一屏下来 90% 是广告和 \r 动画帧。
#
# 所以这个脚本的设计原则只有一条：**输出即接口**。
#
#   stdout  = 每行一个 JSON 事件（NDJSON），探针逐行读、逐行入库
#   stderr  = 给人看的进度（转圈、彩色，随便刷，不影响解析）
#
# 事件类型：
#   {"event":"start","schema":1,"tests":["sysinfo","disk",...]}
#   {"event":"progress","test":"disk","pct":40,"msg":"fio 顺序读…"}
#   {"event":"result","test":"disk","ok":true,"data":{...}}
#   {"event":"done","ok":true,"elapsed_ms":123456,"failed":["net"]}
#
# 每一项独立失败：某一项挂了只影响那一项，其余照跑照报。
#   跑分脚本最忌讳"跑到一半整个挂掉，什么结果都没有"。
#
# 用法：
#   bash kokoro-bench.sh                    # 全部
#   bash kokoro-bench.sh --only ip,disk     # 只跑指定项
#   bash kokoro-bench.sh --list             # 列出可跑的项
#
# 依赖：curl（必需）；fio / sysbench / speedtest / nexttrace 按需自动安装。
# 装不上就降级到内置的兜底方案，不会因此整体失败。

set -o pipefail
umask 077
export LC_ALL=C.UTF-8

# The script is fetched into a per-run temp directory by the Agent. Keep any
# fallback tool beside it so a user-level install never depends on PATH setup.
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"

SCHEMA=1
ONLY=""
WANT_INSTALL=1
PREPARE_ONLY=0
APT_REFRESHED=0
FAILED=""
PKG_ERROR=""

# ---------- 基础工具 ----------

# jesc 把任意字符串转成 JSON 字符串安全的形式。
# 用 bash 参数展开而不是 sed/awk：快，且不会有子进程编码问题。
jesc() {
  local s=$1
  s=${s//\\/\\\\}
  s=${s//\"/\\\"}
  s=${s//$'\r'/}
  s=${s//$'\t'/\\t}
  s=${s//$'\n'/\\n}
  printf '%s' "$s"
}

# emit 往 stdout 打一个事件。stdout 上**只允许**出现这里打出来的东西。
emit() { printf '%s\n' "$1"; }

# say 往 stderr 打人看的进度，绝不出现在 stdout。
say() { printf '\033[36m▸\033[0m %s\n' "$*" >&2; }

ev_start()   { emit "{\"event\":\"start\",\"schema\":$SCHEMA,\"tests\":[$1]}"; }
ev_progress(){ emit "{\"event\":\"progress\",\"test\":\"$(jesc "$1")\",\"pct\":$2,\"msg\":\"$(jesc "$3")\"}"; }
ev_result()  {
  if [ -z "$3" ]; then ev_error "$1" "测试未生成有效的结构化结果"; return 1; fi
  emit "{\"event\":\"result\",\"test\":\"$(jesc "$1")\",\"ok\":$2,\"data\":$3}"
}
ev_error()   {
  emit "{\"event\":\"result\",\"test\":\"$(jesc "$1")\",\"ok\":false,\"error\":\"$(jesc "$2")\"}"
  case " $FAILED " in *" $1 "*) ;; *) FAILED="$FAILED $1" ;; esac
}

# now_ms 取毫秒时间戳。date +%s%3N 在 GNU coreutils 上有，BusyBox 没有。
now_ms() {
  local value
  value=$(date +%s%3N 2>/dev/null)
  case "$value" in ''|*[!0-9]*) echo $(( $(date +%s) * 1000 )) ;; *) echo "$value" ;; esac
}

have() { command -v "$1" >/dev/null 2>&1; }

fetch_verified() {
  local url=$1 expected=$2 target=$3 actual
  have sha256sum || { say "缺少 sha256sum，拒绝下载未校验的组件"; return 1; }
  curl -fsSL --proto '=https' --proto-redir '=https' --retry 2 --max-time 120 "$url" -o "$target.part" || { rm -f -- "$target.part"; return 1; }
  actual=$(sha256sum "$target.part" | awk '{print $1}')
  if [ "$actual" != "$expected" ]; then
    rm -f -- "$target.part"
    say "组件 SHA-256 校验失败，拒绝执行"
    return 1
  fi
  mv -- "$target.part" "$target"
}

# Package installs are local, fixed dependencies; bounded waits avoid hanging
# on a package lock or a broken mirror. Refresh apt indexes after a failed try.
pkg_run() {
  local log rc budget=180
  [ "$1" = systemctl ] && budget=500
  log=$(mktemp) || return 1
  if have timeout; then timeout -k 5s "${budget}s" "$@" >"$log" 2>&1; else "$@" >"$log" 2>&1; fi
  rc=$?
  PKG_ERROR=""
  if [ "$rc" != 0 ]; then
    if grep -qi 'read-only file system' "$log"; then PKG_ERROR="文件系统只读"
    elif grep -qi 'no space left' "$log"; then PKG_ERROR="磁盘空间不足"
    elif grep -qi 'could not resolve\|temporary failure resolving' "$log"; then PKG_ERROR="软件源 DNS 解析失败"
    elif grep -qi 'could not get lock\|unable to acquire.*lock' "$log"; then PKG_ERROR="包管理器正在被其他进程占用"
    elif grep -qi 'unable to locate package\|no match for argument' "$log"; then PKG_ERROR="软件源中找不到所需组件"
    elif grep -qi 'seteuid .*operation not permitted\|setresuid .*operation not permitted' "$log"; then
      PKG_ERROR="受限 VPS 禁止 apt 切换到 _apt 用户，将改用 root 下载"
      APT_NEEDS_ROOT_SANDBOX=1
    elif [ "$rc" = 124 ] || [ "$rc" = 137 ]; then PKG_ERROR="组件安装超时"
    else PKG_ERROR="软件源下载或包管理器执行失败（退出码 $rc）"; fi
    say "组件安装命令失败（退出码 $rc）：$*"
    tail -n 25 "$log" >&2
  fi
  rm -f -- "$log"
  return "$rc"
}

install_pkg() {
  [ "$WANT_INSTALL" = "1" ] || return 1
  [ "$(id -u)" = "0" ] || { say "安装依赖需要 root 权限: $*"; return 1; }
  local pkgs=("$@")
  say "安装依赖: ${pkgs[*]}"
  if [ ! -w /usr ] && have systemctl && [ "$(systemctl show kokoro-test-dependencies.service -p LoadState --value 2>/dev/null)" = loaded ]; then
    say "通过本机固定组件安装服务准备测试依赖"
    if pkg_run systemctl start kokoro-test-dependencies.service; then return 0; fi
    PKG_ERROR="固定组件安装服务失败，请查看 journalctl -u kokoro-test-dependencies.service"
    if have journalctl; then
      say "固定组件安装服务最近日志："
      journalctl -u kokoro-test-dependencies.service -n 40 --no-pager -o cat >&2 || true
    fi
    return 1
  fi
  # 重试 3 次：**并发跑测试时 apt 锁会冲突**（实测两个脚本同时装包，
  # 后到的那个会静默失败，然后降级到很差的兜底方案）。
  local i=0
  while [ $i -lt 3 ]; do
    if have apt-get; then
      if [ "${APT_NEEDS_ROOT_SANDBOX:-0}" = "1" ]; then
        DEBIAN_FRONTEND=noninteractive pkg_run apt-get -o APT::Sandbox::User=root -o DPkg::Lock::Timeout=60 -o Acquire::Retries=1 -o Acquire::http::Timeout=20 install -y -qq "${pkgs[@]}" && return 0
      else
        DEBIAN_FRONTEND=noninteractive pkg_run apt-get -o DPkg::Lock::Timeout=60 -o Acquire::Retries=1 -o Acquire::http::Timeout=20 install -y -qq "${pkgs[@]}" && return 0
      fi
      if [ "$APT_REFRESHED" = "0" ]; then
        APT_REFRESHED=1
        say "更新软件包索引后重试安装"
        if [ "${APT_NEEDS_ROOT_SANDBOX:-0}" = "1" ]; then
          DEBIAN_FRONTEND=noninteractive pkg_run apt-get -o APT::Sandbox::User=root -o DPkg::Lock::Timeout=60 -o Acquire::Retries=1 -o Acquire::http::Timeout=20 update -qq || true
        else
          DEBIAN_FRONTEND=noninteractive pkg_run apt-get -o DPkg::Lock::Timeout=60 -o Acquire::Retries=1 -o Acquire::http::Timeout=20 update -qq || true
        fi
      fi
    elif have dnf; then
      pkg_run dnf install -y -q "${pkgs[@]}" && return 0
    elif have yum; then
      pkg_run yum install -y -q "${pkgs[@]}" && return 0
    elif have apk; then
      pkg_run apk add --no-cache "${pkgs[@]}" && return 0
    else
      return 1
    fi
    i=$((i + 1))
    sleep 3
  done
  say "依赖安装失败: ${pkgs[*]}（${PKG_ERROR:-请检查软件源、网络与包管理器锁}）"
  return 1
}

# jsonq 用 python3 从 stdin 的 JSON 里按点号路径取值，取不到就返回空。
# 之所以借 python3 而不是 jq：python3 在主流发行版上是标配，jq 不是。
jsonq() {
  local path=$1
  python3 -c '
import json,sys
try:
    d=json.load(sys.stdin)
except Exception:
    sys.exit(0)
for k in sys.argv[1].split("."):
    if isinstance(d,list):
        try: d=d[int(k)]
        except Exception: sys.exit(0)
    elif isinstance(d,dict):
        d=d.get(k)
    else:
        sys.exit(0)
    if d is None: sys.exit(0)
print(d if not isinstance(d,(dict,list)) else json.dumps(d,ensure_ascii=False))
' "$path" 2>/dev/null
}

# ---------- 各项测试 ----------

# 0) 环境预检：把"这台机器缺什么"直接报出来。
#
# 为什么值得单独一项：实测遇到过 /dev/zero 缺失、apt 锁冲突这类环境问题，
# 表现出来是"某个测试莫名其妙失败"，排查半天。预检把它变成一条明确的信息。
test_env() {
  local issues="" add_issue
  add_issue() { issues="${issues}${issues:+,}\"$(jesc "$1")\""; }

  [ -e /dev/zero ]    || add_issue "缺少 /dev/zero（devtmpfs 异常，dd 类测试会失败）"
  [ -e /dev/urandom ] || add_issue "缺少 /dev/urandom"
  [ -c /dev/null ]    || add_issue "/dev/null 不是设备节点（环境不标准）"
  [ "$(id -u)" = "0" ] || add_issue "不是 root，部分测试会降级或失败"
  have python3        || add_issue "没有 python3，JSON 解析会降级"
  have curl           || add_issue "没有 curl，多数测试无法进行"

  local virt=""
  virt=$(systemd-detect-virt 2>/dev/null || echo unknown)
  ev_result env true "$(printf '{"virt":"%s","root":%s,"issues":[%s]}' \
    "$(jesc "$virt")" "$([ "$(id -u)" = 0 ] && echo true || echo false)" "$issues")"
}

# 1) 系统信息：不测性能，只报"这台机器是什么"
test_sysinfo() {
  local cpu="" cores="" mem="" disk="" virt="" os=""
  cores=$(nproc 2>/dev/null || echo 0)
  cpu=$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2- | sed 's/^ *//')
  [ -n "$cpu" ] || cpu=$(grep -m1 'Hardware' /proc/cpuinfo 2>/dev/null | cut -d: -f2- | sed 's/^ *//')
  mem=$(awk '/MemTotal/{print $2*1024}' /proc/meminfo 2>/dev/null)
  disk=$(df -B1 / 2>/dev/null | awk 'NR==2{print $2}')
  os=$(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME")
  virt=$(systemd-detect-virt 2>/dev/null || echo "")

  ev_result sysinfo true "$(printf '{"cpu":"%s","cores":%s,"mem_total":%s,"disk_total":%s,"os":"%s","virt":"%s"}' \
    "$(jesc "$cpu")" "${cores:-0}" "${mem:-0}" "${disk:-0}" "$(jesc "$os")" "$(jesc "$virt")")"
}

# 2) 磁盘 IO：fio 优先，装不上就 dd 兜底
#
# 用 512MB 的小文件，避免把用户的小鸡写满 —— 探针不该成为磁盘杀手。
test_disk() {
  ev_progress disk 10 "准备磁盘测试"
  local dir=/tmp/kokoro-bench
  mkdir -p "$dir" || { ev_error disk "无法创建临时目录"; return; }

  if ! have fio; then
    install_pkg fio || true
  fi

  if have fio; then
    say "fio 顺序读写（512MB）"
    ev_progress disk 30 "fio 顺序读写"
    # 用 fio 自带的 --output= 而不是 shell 重定向：
    # fio 会把进度也打到 stderr，重定向容易把两者搅在一起；
    # 显式指定输出文件最干净。
    fio --name=seq --directory="$dir" --rw=readwrite --bs=1M --size=256M \
        --numjobs=1 --iodepth=16 --runtime=20 --time_based --group_reporting \
        --output-format=json --output="$dir/fio.json" >/dev/null 2>&1

    say "fio 4K 随机读写"
    ev_progress disk 65 "fio 4K 随机"
    fio --name=rand --directory="$dir" --rw=randrw --bs=4k --size=64M \
        --numjobs=1 --iodepth=32 --runtime=20 --time_based --group_reporting \
        --output-format=json --output="$dir/fio4k.json" >/dev/null 2>&1

    local seq_r seq_w r4k r4k_w
    # 文件为空说明 fio 根本没跑起来，直接走兜底，别报一个 0 出来
    if [ ! -s "$dir/fio.json" ]; then
      say "fio 没有输出，转兜底方案"
    fi
    seq_r=$(jsonq "jobs.0.read.bw" <"$dir/fio.json" 2>/dev/null)
    seq_w=$(jsonq "jobs.0.write.bw" <"$dir/fio.json")
    r4k=$(jsonq "jobs.0.read.iops" <"$dir/fio4k.json")
    r4k_w=$(jsonq "jobs.0.write.iops" <"$dir/fio4k.json")

    if [ -n "$seq_r" ] || [ -n "$seq_w" ]; then
      # fio 的 bw 单位是 KiB/s，换算成 MB/s 便于阅读
      local sr sw
      sr=$(awk "BEGIN{printf \"%.0f\", ${seq_r:-0}/1024}")
      sw=$(awk "BEGIN{printf \"%.0f\", ${seq_w:-0}/1024}")
      ev_result disk true "$(printf '{"seq_read_mbs":%s,"seq_write_mbs":%s,"rand4k_read_iops":%s,"rand4k_write_iops":%s,"tool":"fio"}' \
        "${sr:-0}" "${sw:-0}" "${r4k:-0}" "${r4k_w:-0}")"
      rm -rf "$dir"
      return
    fi
  fi

  # ⚠️ 有些小鸡上 /dev/zero 根本不存在（devtmpfs 没挂好，
  # 连 /dev/null 都是普通文件而不是设备节点）。这时候 dd 会直接报
  # "failed to open '/dev/zero'"，**必须显式判断**，否则会报出一个 0 的结果 ——
  # 那比"跳过"更糟：看起来像测了，其实是假的。
  if [ ! -e /dev/zero ]; then
    ev_result disk true '{"skipped":true,"reason":"这台机器没有 /dev/zero（devtmpfs 异常），fio 也装不上，无法测磁盘"}'
    return
  fi

  # 兜底：dd。不准（受缓存影响），但总比没有强 —— 结果里会标 tool=dd 让人知道。
  say "fio 不可用，降级用 dd"
  ev_progress disk 60 "dd 兜底测试"
  local w r
  w=$(dd if=/dev/zero of="$dir/t.bin" bs=1M count=256 oflag=direct 2>&1 | \
      awk -F, '/copied/{gsub(/[^0-9.]/,"",$4); print $4}')
  [ -n "$w" ] || w=$(dd if=/dev/zero of="$dir/t.bin" bs=1M count=256 2>&1 | \
      awk -F, '/copied/{gsub(/[^0-9.]/,"",$4); print $4}')
  sync
  r=$(dd if="$dir/t.bin" of=/dev/null bs=1M iflag=direct 2>&1 | \
      awk -F, '/copied/{gsub(/[^0-9.]/,"",$4); print $4}')
  [ -n "$r" ] || r=$(dd if="$dir/t.bin" of=/dev/null bs=1M 2>&1 | \
      awk -F, '/copied/{gsub(/[^0-9.]/,"",$4); print $4}')
  rm -rf "$dir"
  ev_result disk true "$(printf '{"seq_read_mbs":%s,"seq_write_mbs":%s,"tool":"dd"}' \
    "${r:-0}" "${w:-0}")"
}

# 3) CPU：sysbench 优先，装不上就用 openssl 兜底
test_cpu() {
  ev_progress cpu 20 "准备 CPU 测试"
  if ! have sysbench; then
    install_pkg sysbench || true
  fi

  if have sysbench; then
    say "sysbench CPU 单核"
    ev_progress cpu 50 "sysbench 单核"
    local single multi
    single=$(sysbench cpu --cpu-max-prime=10000 --threads=1 --time=10 run 2>/dev/null | \
      awk '/events per second/{print $4}')
    say "sysbench CPU 多核"
    ev_progress cpu 80 "sysbench 多核"
    local n
    n=$(nproc 2>/dev/null || echo 1)
    multi=$(sysbench cpu --cpu-max-prime=10000 --threads="$n" --time=10 run 2>/dev/null | \
      awk '/events per second/{print $4}')
    if [ -n "$single" ]; then
      ev_result cpu true "$(printf '{"single_eps":%s,"multi_eps":%s,"threads":%s,"tool":"sysbench"}' \
        "${single:-0}" "${multi:-0}" "$n")"
      return
    fi
  fi

  ev_error cpu "CPU 跑分失败：sysbench 未安装或执行失败；${PKG_ERROR:-未返回有效跑分结果}"
}

# Memory bandwidth uses the same installed sysbench as CPU. No Agent-specific
# extra command is needed; standalone and panel runs produce identical events.
test_memory() {
  ev_progress memory 10 "准备内存读写测试"
  if ! have sysbench; then install_pkg sysbench || true; fi
  if ! have sysbench; then
    ev_error memory "内存读写测试失败：sysbench 自动安装失败；${PKG_ERROR:-请检查软件源、网络与包管理器锁}"
    return
  fi
  local operation out rate read_rate="" write_rate=""
  for operation in read write; do
    ev_progress memory 50 "sysbench 内存 $operation"
    out=$(sysbench memory --threads=1 --memory-oper="$operation" --memory-block-size=1M --memory-total-size=1T --time=3 run 2>/dev/null)
    rate=$(printf '%s\n' "$out" | awk '/MiB\/sec/ {s=$0; sub(/.*\(/,"",s); split(s,v," "); print v[1]; exit}')
    if ! [[ "$rate" =~ ^[0-9]+([.][0-9]+)?$ ]]; then
      ev_error memory "内存 $operation 测试失败：sysbench 未返回可解析的带宽结果"
      return
    fi
    if [ "$operation" = read ]; then read_rate="$rate"; else write_rate="$rate"; fi
  done
  ev_result memory true "$(printf '{"read_mib_s":%s,"write_mib_s":%s,"tool":"sysbench"}' "$read_rate" "$write_rate")"
}

# 4) 网络测速：speedtest 优先，装不上就用 curl 下载兜底
test_net() {
  ev_progress net 20 "准备测速"
  if ! have speedtest && ! have speedtest-cli; then
    install_pkg speedtest-cli || true
  fi

  local out=""
  if have speedtest; then
    say "speedtest（Ookla 官方 CLI）"
    ev_progress net 50 "speedtest 测速中"
    out=$(speedtest --format=json --accept-license --accept-gdpr 2>/dev/null)
    if [ -n "$out" ]; then
      local dl ul ping srv
      dl=$(printf '%s' "$out" | jsonq "download.bandwidth")
      ul=$(printf '%s' "$out" | jsonq "upload.bandwidth")
      ping=$(printf '%s' "$out" | jsonq "ping.latency")
      srv=$(printf '%s' "$out" | jsonq "server.name")
      # bandwidth 单位是 byte/s
      ev_result net true "$(printf '{"down_mbps":%s,"up_mbps":%s,"ping_ms":%s,"server":"%s","tool":"speedtest"}' \
        "$(awk "BEGIN{printf \"%.1f\", ${dl:-0}*8/1000000}")" \
        "$(awk "BEGIN{printf \"%.1f\", ${ul:-0}*8/1000000}")" \
        "${ping:-0}" "$(jesc "$srv")")"
      return
    fi
  fi

  if have speedtest-cli; then
    say "speedtest-cli"
    ev_progress net 50 "speedtest-cli 测速中"
    out=$(speedtest-cli --json 2>/dev/null)
    if [ -n "$out" ]; then
      local dl ul
      dl=$(printf '%s' "$out" | jsonq "download")
      ul=$(printf '%s' "$out" | jsonq "upload")
      ev_result net true "$(printf '{"down_mbps":%s,"up_mbps":%s,"tool":"speedtest-cli"}' \
        "$(awk "BEGIN{printf \"%.1f\", ${dl:-0}/1000000}")" \
        "$(awk "BEGIN{printf \"%.1f\", ${ul:-0}/1000000}")")"
      return
    fi
  fi

  # 兜底：从几个知名测速点下一个大文件，量一下速度
  say "speedtest 不可用，降级用 curl 下载测速"
  ev_progress net 55 "curl 下载测速"
  local url="https://speed.cloudflare.com/__down?bytes=52428800"
  local t0 t1 secs mbps
  t0=$(now_ms)
  if curl -fsS --max-time 60 -o /dev/null "$url" 2>/dev/null; then
    t1=$(now_ms)
    secs=$(awk "BEGIN{printf \"%.3f\", ($t1-$t0)/1000}")
    mbps=$(awk "BEGIN{printf \"%.1f\", ($secs>0 ? 50*8/$secs : 0)}")
    ev_result net true "$(printf '{"down_mbps":%s,"tool":"curl","note":"Cloudflare 50MB 单线程"}' "$mbps")"
  else
    ev_error net "测速失败：speedtest 不可用且 curl 下载超时"
  fi
}

# 5) IP 质量与解锁
#
# 这块最麻烦：流媒体解锁要打一堆厂商的接口、判断各种"地区不可用"的文案，
# 自己实现很容易失效。所以这里**复用 xykt 的 IPQuality**（社区事实标准），
# 但把它那坨彩色输出里的 JSON 抠出来，转成我们的事件格式 ——
# 我们只做"翻译"，不做"重新发明"。
test_ip() {
  if ! have python3; then install_pkg python3 || true; fi
  if ! have python3; then ev_error ip "IPQuality 结果解析需要 python3，自动安装失败"; return; fi
  if ! have curl || ! have jq || ! have bc || ! have dig || ! have nc || ! have ip; then
    local ip_packages=(curl jq bc)
    if have apk; then ip_packages+=(bind-tools netcat-openbsd iproute2)
    elif have apt-get; then ip_packages+=(bind9-dnsutils netcat-openbsd iproute2)
    else ip_packages+=(bind-utils nmap-ncat iproute); fi
    install_pkg "${ip_packages[@]}" || true
    for component in curl jq bc dig nc ip; do
      if ! have "$component"; then ev_error ip "IPQuality 所需组件 $component 自动安装失败"; return; fi
    done
  fi
  ev_progress ip 10 "拉取 IP 质量脚本"
  local work js rc
  work=$(mktemp -d) || { ev_error ip "无法创建 IPQuality 临时目录"; return; }
  if ! fetch_verified "https://raw.githubusercontent.com/xykt/IPQuality/87397e2c3196ec796f5477c83343c2354df601ea/ip.sh" \
    "b30df5a3c2204276c54e99dcc5080b46f8a627667730aee7de63b109b8ecaecf" "$work/ipquality.sh" || \
    ! fetch_verified "https://raw.githubusercontent.com/xykt/IPQuality/87397e2c3196ec796f5477c83343c2354df601ea/ref/dnsbl.list" \
    "a92ca482843310167309c82f05cb7e208d7decc7f8bb7eada0f96af42f1143ad" "$work/dnsbl.list"; then
    rm -rf -- "$work"
    ev_error ip "无法下载并校验固定版本的 IPQuality 组件"
    return
  fi
  # IPQuality interpolates its DNSBL catalog in bash -c. Use the verified local
  # catalog and freeze the revision of its other data files as well.
  if ! python3 - "$work/ipquality.sh" "$work/dnsbl.list" <<'PY'
from pathlib import Path
import re, sys
catalog = Path(sys.argv[2])
domains = [domain.strip() for domain in catalog.read_text(encoding="utf-8").splitlines()]
domains = [domain for domain in domains if domain and len(domain) <= 253 and re.fullmatch(r'[A-Za-z0-9](?:[A-Za-z0-9_.-]*[A-Za-z0-9])?', domain) and '..' not in domain]
if not domains:
    raise SystemExit("DNSBL catalog contains no safe domains; refusing to execute")
catalog.write_text('\n'.join(domains)+'\n', encoding="utf-8")
p = Path(sys.argv[1])
text = p.read_text(encoding="utf-8")
needle = 'curl $CurlARG -sL "${rawgithub}main/ref/dnsbl.list"'
if text.count(needle) != 1:
    raise SystemExit("IPQuality DNSBL interface changed; refusing to execute")
text = text.replace(needle, 'cat -- "$KOKORO_IPQUALITY_DNSBL"')
text = text.replace('${rawgithub}main/ref/', '${rawgithub}87397e2c3196ec796f5477c83343c2354df601ea/ref/')
p.write_text(text, encoding="utf-8")
PY
  then
    rm -rf -- "$work"
    ev_error ip "无法安全配置固定版本的 IPQuality 组件"
    return
  fi
  say "运行 IPQuality（约 2~5 分钟）"
  ev_progress ip 30 "检测 IP 数据库与流媒体解锁"

  # Use IPQuality's documented JSON file interface. Private mode disables
  # uploading a public report; progress/advertisements never enter our parser.
  KOKORO_IPQUALITY_DNSBL="$work/dnsbl.list" bash "$work/ipquality.sh" -j -y -p -o "$work/result.json" >"$work/console.log" 2>&1
  rc=$?
  if [ ! -s "$work/result.json" ]; then
    rm -rf -- "$work"
    ev_error ip "IPQuality 未生成 JSON 结果文件（退出码 $rc），请检查依赖和网络后重试"
    return
  fi
  js=$(cat "$work/result.json")
  rm -rf -- "$work"

  # 把它的结构翻译成我们的：只保留卡片上要用的字段
  #
  # ⚠️ **不输出任何 IP**：IPQuality 的原始 JSON 里有地址，这里只取
  # 类型/ASN/组织/风险分/黑名单/解锁这些**描述性**字段。
  # 最后再统一过一遍正则兜底，宁可少点信息也不泄露地址。
  local brief
  brief=$(printf '%s' "$js" | python3 -c '
import json,re,sys
IPRE = re.compile(r"\b(?:\d{1,3}\.){3}\d{1,3}\b")
try:
    # Upstream appends IPv4 then IPv6 as separate JSON documents to one file.
    d,end=json.JSONDecoder().raw_decode(sys.stdin.read().lstrip())
    if not isinstance(d,dict): raise ValueError("report is not an object")
except Exception:
    sys.exit(1)
info=d.get("Info") or {}
score=d.get("Score") or {}
media=d.get("Media") or {}
mail=d.get("Mail") or {}
blk=mail.get("DNSBlacklist") or {}

short={"DisneyPlus":"Disney+","AmazonPrimeVideo":"Prime","Youtube":"YouTube"}
unlocked=[]; locked=[]
for k in sorted(media):
    v=media.get(k) or {}
    name=short.get(k,k)
    (unlocked if (v.get("Status") or "")=="解锁" else locked).append(name)

def num(x):
    try: return float(str(x).replace("%",""))
    except Exception: return None

def text(value):
    if isinstance(value,dict): value=value.get("Name") or ""
    return value if isinstance(value,str) and value!="null" else ""

# IP 用途：各数据库怎么分类这个 IP（机房/家宽/商业…），取众数当结论
usage={}
for db,v in (d.get("Type") or {}).get("Usage",{}).items():
    if isinstance(v,str) and v and v!="null": usage[db]=v
consensus=""
if usage:
    from collections import Counter
    consensus=Counter(usage.values()).most_common(1)[0][0]

out={
  "ip_type": text(info.get("Type")),
  "usage": consensus,
  "usage_detail": usage,
  "org": text(info.get("Organization")),
  "asn": text(info.get("ASN")),
  "city": text(info.get("City")),
  "country": text(info.get("Country") or info.get("Region")),
  "risk_scamalytics": num(score.get("SCAMALYTICS")),
  "risk_abuseipdb": num(score.get("AbuseIPDB")),
  "risk_dbip": num(score.get("DBIP")),
  "blacklist_total": blk.get("Total"),
  "blacklist_clean": blk.get("Clean"),
  "blacklist_marked": blk.get("Marked"),
  "blacklist_listed": blk.get("Blacklisted"),
  "unlocked": unlocked,
  "locked": locked,
  "unlock_total": len(media),
}
txt=json.dumps(out,ensure_ascii=False)
print(IPRE.sub("[已隐去]", txt))
' 2>/dev/null)

  if [ -z "$brief" ]; then
    ev_error ip "IPQuality 的 JSON 结构不认识（脚本可能改版了）"
    return
  fi
  ev_result ip true "$brief"
}

# 6) 回程线路：沿用 NodeQuality/NetQuality 的三地运营商专用目标和判定方法。
# NetQuality 使用北京、上海、广东的 CT/CU/CM 专用域名，通过 NextTrace TCP 80
# 跟踪；线路类型按首个中国大陆路由跳分类，首跳不明确时再按整条路径 ASN 兜底。
test_route() {
  if ! have python3; then install_pkg python3 || true; fi
  if ! have python3; then ev_error route "回程路由解析需要 python3，自动安装失败"; return; fi
  ev_progress route 10 "准备回程线路检测"
  local nexttrace_bin=""
  if have nexttrace; then
    nexttrace_bin=$(command -v nexttrace)
  else
    install_pkg nexttrace || true
    if have nexttrace; then
      nexttrace_bin=$(command -v nexttrace)
    fi
  fi
  if [ -z "$nexttrace_bin" ] && [ "$WANT_INSTALL" = 1 ]; then
    say "下载并校验固定版本的 NextTrace 成品"
    local nt_arch nt_sha
    case "$(uname -m)" in
      x86_64|amd64) nt_arch=amd64; nt_sha=1f4c559cbdf6f667a1a9e050567c9cf1fc11741e8cc1e50f5fdcaf2dbb247232 ;;
      aarch64|arm64) nt_arch=arm64; nt_sha=9c2f1b79e7d0e37f59ebe685aec1d5c41fb8f3407f54e17b34656712eaa66fd9 ;;
      *) nt_arch="" ;;
    esac
    if [ -n "$nt_arch" ] && fetch_verified "https://github.com/nxtrace/NTrace-core/releases/download/v1.7.1/nexttrace_linux_$nt_arch" "$nt_sha" "$SCRIPT_DIR/nexttrace"; then
      chmod 0700 "$SCRIPT_DIR/nexttrace"
      nexttrace_bin="$SCRIPT_DIR/nexttrace"
    fi
  fi
  if [ -z "$nexttrace_bin" ]; then
    ev_error route "NextTrace 安装失败：软件源或官方安装器不可用，回程线路未检测"
    return
  fi

  # NetQuality -R 回程模式（NodeQuality 的 run_net_trace 调用）使用同一
  # 组目标域名和 NextTrace 参数；下方分类按 get_route_mode 的 ASN 规则实现。
  # 目标由域名维护，避免静态公共 DNS IP 与运营商回程线路不匹配。
  local targets=(
    "北京:电信:bj-ct-v4.ip.zstaticcdn.com" "北京:联通:bj-cu-v4.ip.zstaticcdn.com" "北京:移动:bj-cm-v4.ip.zstaticcdn.com"
    "上海:电信:sh-ct-v4.ip.zstaticcdn.com" "上海:联通:sh-cu-v4.ip.zstaticcdn.com" "上海:移动:sh-cm-v4.ip.zstaticcdn.com"
    "广东:电信:gd-ct-v4.ip.zstaticcdn.com" "广东:联通:gd-cu-v4.ip.zstaticcdn.com" "广东:移动:gd-cm-v4.ip.zstaticcdn.com"
  )
  local route_tmp
  route_tmp=$(mktemp -d "$SCRIPT_DIR/route.XXXXXX") || { ev_error route "无法创建回程路由临时目录"; return; }
  local i=0 telecom="" unicom="" mobile=""
  trace_nexttrace() (
    local index=$1 host=$2 nexttrace=$3 output_dir=$4 out="" attempt=0
    while (( attempt < 10 )); do
      out=$(timeout -s SIGKILL 50 "$nexttrace" -p 80 -q 8 -4 --tcp --psize 1400 "$host" 2>/dev/null)
      if [[ "$out" == *"traceroute to"* && "$out" != *"*please try again later*"* ]]; then break; fi
      attempt=$((attempt + 1))
      if (( attempt < 10 )); then sleep 5; fi
    done
    printf '%s' "$out" > "$output_dir/$index"
  )
  for target in "${targets[@]}"; do
    IFS=: read -r city nm host <<< "$target"
    say "回程线路 → ${city}${nm}"
    ev_progress route $(( 10 + i * 9 )) "TCP 探测 ${city}${nm} 回程"
    trace_nexttrace "$i" "$host" "$nexttrace_bin" "$route_tmp" &
    i=$((i + 1))
  done
  wait
  i=0
  for target in "${targets[@]}"; do
    IFS=: read -r city nm host <<< "$target"
    local out brief
    out=$(cat "$route_tmp/$i" 2>/dev/null || true)
brief=$(printf '%s' "$out" | python3 -c '
import json, re, sys

def field(obj, *names):
    if not isinstance(obj, dict):
        return ""
    values = {str(k).lower(): v for k, v in obj.items()}
    for name in names:
        value = values.get(name.lower())
        if value not in (None, ""):
            return value
    return ""

raw_output = sys.stdin.read()
target_isp = sys.argv[1] if len(sys.argv) > 1 else "中国运营商"

if not raw_output.lstrip().startswith("{"):
    # NodeQuality invokes NetQuality -R, whose route-mode parser reads the
    # normal NextTrace table: hop, IP, ASN token and remaining location text.
    # It preserves the same first-China-hop classification rule.
    hops = []
    lines = raw_output.splitlines()
    for line in lines:
        line = re.sub(r"\x1b\[[0-9;]*m", "", line)
        m = re.match(r"\s*(\d+)\s+(\S+)\s+(.*)", line)
        if not m or m.group(2) == "*":
            continue
        rest = m.group(3)
        asn_match = re.search(r"\bAS(\d+)\b", rest, re.I)
        ip = m.group(2)
        desc = []
        for token in rest.split():
            if re.fullmatch(r"AS\d+", token, re.I) or re.fullmatch(r"\[[^]]*\]", token):
                continue
            if re.fullmatch(r"\d+(?:\.\d+)?\s*ms", token, re.I):
                continue
            desc.append(token)
        region = " ".join(desc)
        owner = region
        hops.append({"hop": int(m.group(1)), "ip": ip, "asn": asn_match.group(1) if asn_match else "", "region": region, "owner": owner})
else:
    try:
        d = json.loads(raw_output)
    except Exception:
        d = {}
    raw_hops = field(d, "Hops", "Hop", "Trace", "Stats", "Records")
    if isinstance(raw_hops, dict):
        raw_hops = list(raw_hops.values())
    if not isinstance(raw_hops, list):
        raw_hops = []
    hops = []
    def visit(value):
        if isinstance(value, list):
            for child in value: visit(child)
            return
        if not isinstance(value, dict): return
        geo = field(value, "Geo", "geo", "Location")
        if not isinstance(geo, dict): geo = {}
        raw_asn = field(geo, "asnumber", "ASNumber", "asn", "as_number", "AS") or field(value, "asnumber", "ASNumber", "asn", "as_number", "AS")
        m = re.search(r"(?i)(?:AS\s*)?(\d{1,10})", str(raw_asn or ""))
        ip = str(field(value, "IP", "Address", "addr") or "")
        region = " ".join(str(field(geo, n) or field(value, n) or "") for n in ("country", "prov", "city"))
        owner = str(field(geo, "owner", "org", "organization", "isp") or field(value, "owner", "org", "organization", "isp") or "")
        if ip or region or owner:
            hops.append({"hop": len(hops)+1, "ip": ip, "asn": m.group(1) if m else "", "region": region, "owner": owner})
        for key, child in value.items():
            if str(key).lower() in ("hops", "hop", "probes", "results", "stats", "records", "record") and isinstance(child, (list, dict)): visit(child)
    visit(raw_hops)

for h in hops:
    if h["ip"].startswith("59.43."): h["asn"] = "4809"
    if "CTGNet" in h["owner"]: h["asn"] = "23764"
all_asns = [h["asn"] for h in hops if h["asn"]]
cn_hop = next((i for i, h in enumerate(hops) if "中国" in h["region"] and not any(x in h["region"] for x in ("香港", "澳门", "台湾"))), None)
all_set = set(all_asns)
label = ""
hidden = cn_hop is None or cn_hop >= len(hops)-1

# Match NetQuality -R: find the first mainland hop, skip AS17676, classify its
# ASN with neighboring/path-wide context, then use the same whole-path fallback
# order when the ingress ASN is missing or not recognized.
if cn_hop is not None:
    if hops[cn_hop]["asn"] == "17676": cn_hop += 1
    cn_hop_number = hops[cn_hop]["hop"] if cn_hop < len(hops) else 0
    asn = hops[cn_hop]["asn"] if cn_hop < len(hops) else ""
    preceding = next((h["asn"] for h in hops if h["hop"] == cn_hop_number-1), "")
    if asn == "4134": label = "电信 163（AS4134）"
    elif asn == "4837": label = "联通 9929（AS9929）" if preceding == "10099" else "联通 4837（AS4837）"
    elif asn == "58453": label = "移动 CMI（AS58453）"
    elif asn == "58807": label = "移动 CMIN2（AS58807）"
    elif asn == "9808": label = "移动 CMIN2（AS58807）" if "58807" in all_set else "移动 CMI（AS9808）"
    elif asn == "9929": label = "联通 9929（AS9929）"
    elif asn == "10099": label = "联通 9929（AS9929）" if "9929" in all_set else "联通国际（AS10099）"
    elif asn in ("4809", "23764"):
        label = "电信 CTG GIA（AS23764）" if "23764" in all_set and cn_hop_number > 1 else "电信 CN2 GIA（AS4809）"
        for h in hops[cn_hop:]:
            if h["asn"] in ("4809", "23764"): continue
            if h["ip"].startswith("202.97."): label = "电信 CN2 GT（AS4809）"
            break
    elif asn == "4538": label = "CERNET（AS4538）"
    elif asn == "7497": label = "CSTNET（AS7497）"

# get_route_mode falls back across the entire path in this priority order.
if not label:
    for candidate, fallback in (("58807","移动 CMIN2（AS58807）"),("9929","联通 9929（AS9929）"),("10099","联通国际（AS10099）"),("4809","电信 CN2（AS4809）"),("9808","移动 CMI（AS9808）"),("4134","电信 163（AS4134）"),("4837","联通 4837（AS4837）")):
        if candidate in all_set: label = fallback; break
if not label:
    evidence_hops = hops[cn_hop:] if cn_hop is not None else hops
    evidence = next(("AS" + h["asn"] + (" (" + h["owner"] + ")" if h["owner"] else "") for h in evidence_hops if h["asn"]), "")
    if hidden:
        label = ("未定位到中国大陆路由；仅识别到 " + evidence) if evidence else "未能定位中国大陆路由，线路 ASN 不可见"
    else:
        label = ("仅识别到路径 " + evidence + "；未匹配到" + target_isp + "线路 ASN") if evidence else "路径有响应，但 ASN 信息缺失"
print(label)
' "$nm" 2>/dev/null)
    [ -n "$brief" ] || brief='路径未识别'
    case "$nm" in
      电信) [ -n "$telecom" ] && telecom="$telecom / "; telecom="${telecom}${city} ${brief}" ;;
      联通) [ -n "$unicom" ] && unicom="$unicom / "; unicom="${unicom}${city} ${brief}" ;;
      移动) [ -n "$mobile" ] && mobile="$mobile / "; mobile="${mobile}${city} ${brief}" ;;
    esac
    i=$((i + 1))
  done
  rm -f -- "$route_tmp"/*
  rmdir -- "$route_tmp" 2>/dev/null || true
  # 构造字段 JSON 时直接用 python 做转义，防止线路字符串导致 JSON 损坏。
  local route_json
  route_json=$(python3 -c 'import json,sys; print(json.dumps({"电信":{"line":sys.argv[1]},"联通":{"line":sys.argv[2]},"移动":{"line":sys.argv[3]}},ensure_ascii=False,separators=(",",":")))' "$telecom" "$unicom" "$mobile")
  if [ -z "$route_json" ]; then ev_error route "回程路由结果无法编码为 JSON"; return; fi
  ev_result route true "$route_json"
}

# ---------- 调度 ----------

ALL_TESTS="env sysinfo disk cpu memory net ip route"

usage() {
  cat >&2 <<EOF
用法: bash kokoro-bench.sh [选项]

  --only a,b,c   只跑指定项（默认全部）
  --list         列出可跑的项
  --no-install   不自动安装依赖（缺什么就降级/跳过）

可跑的项: $ALL_TESTS
EOF
}

# Tests can source the fixed helpers without running benchmarks or installers.
if [ "${BASH_SOURCE[0]}" != "$0" ]; then return 0; fi

while [ $# -gt 0 ]; do
  case "$1" in
    --only) ONLY="$2"; shift 2 ;;
    --list) echo "$ALL_TESTS"; exit 0 ;;
    --no-install) WANT_INSTALL=0; shift ;;
    --prepare-only) PREPARE_ONLY=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) shift ;;
  esac
done

if [ "$PREPARE_ONLY" = 1 ]; then
  packages=()
  for component in curl python3 jq bc sysbench fio speedtest-cli; do
    have "$component" || packages+=("$component")
  done
  if ! have dig; then
    if have apk; then packages+=(bind-tools)
    elif have apt-get; then packages+=(bind9-dnsutils)
    else packages+=(bind-utils); fi
  fi
  if ! have nc; then
    if have apk || have apt-get; then packages+=(netcat-openbsd); else packages+=(nmap-ncat); fi
  fi
  if ! have ip; then
    if have apk || have apt-get; then packages+=(iproute2); else packages+=(iproute); fi
  fi
  if [ "${#packages[@]}" -gt 0 ]; then
    install_pkg "${packages[@]}" || exit 1
    for component in curl python3 jq bc sysbench fio speedtest-cli dig nc ip; do have "$component" || exit 1; done
  fi
  say "固定测试组件已就绪"
  exit 0
fi

# 组装要跑的列表
RUN=""
if [ -n "$ONLY" ]; then
  IFS=',' read -ra want <<< "$ONLY"
  for w in "${want[@]}"; do
    w=$(printf '%s' "$w" | tr -d ' ')
    for t in $ALL_TESTS; do
      [ "$w" = "$t" ] && RUN="$RUN $t"
    done
  done
else
  RUN="$ALL_TESTS"
fi

# start 事件的 tests 数组
tests_json=""
for t in $RUN; do
  [ -n "$tests_json" ] && tests_json="$tests_json,"
  tests_json="$tests_json\"$t\""
done
ev_start "$tests_json"

START=$(now_ms)
FAILED=""
for t in $RUN; do
  say "=== $t ==="
  # 每项独立：挂了只记一笔，继续跑下一项。
  # 不这么干的话，某个工具装不上会让整轮测试"什么都没有"。
  if ! "test_$t"; then
    FAILED="$FAILED $t"
    ev_error "$t" "测试项异常退出"
  fi
done

ELAPSED=$(( $(now_ms) - START ))
failed_json=""
for t in $FAILED; do
  [ -n "$failed_json" ] && failed_json="$failed_json,"
  failed_json="$failed_json\"$t\""
done
ok=true
[ -n "$FAILED" ] && ok=false
emit "{\"event\":\"done\",\"ok\":$ok,\"elapsed_ms\":$ELAPSED,\"failed\":[$failed_json]}"
