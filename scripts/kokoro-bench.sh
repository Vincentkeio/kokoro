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

SCHEMA=1
ONLY=""
WANT_INSTALL=1

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
ev_result()  { emit "{\"event\":\"result\",\"test\":\"$(jesc "$1")\",\"ok\":$2,\"data\":$3}"; }
ev_error()   { emit "{\"event\":\"result\",\"test\":\"$(jesc "$1")\",\"ok\":false,\"error\":\"$(jesc "$2")\"}"; }

# now_ms 取毫秒时间戳。date +%s%3N 在 GNU coreutils 上有，BusyBox 没有。
now_ms() { date +%s%3N 2>/dev/null || echo $(( $(date +%s) * 1000 )); }

have() { command -v "$1" >/dev/null 2>&1; }

# install_pkg 尽力装包，失败不报错（调用方会降级）。
install_pkg() {
  [ "$WANT_INSTALL" = "1" ] || return 1
  local pkgs="$*"
  say "安装依赖: $pkgs"
  # 重试 3 次：**并发跑测试时 apt 锁会冲突**（实测两个脚本同时装包，
  # 后到的那个会静默失败，然后降级到很差的兜底方案）。
  local i=0
  while [ $i -lt 3 ]; do
    if have apt-get; then
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq $pkgs >/dev/null 2>&1 && return 0
    elif have dnf; then
      dnf install -y -q $pkgs >/dev/null 2>&1 && return 0
    elif have yum; then
      yum install -y -q $pkgs >/dev/null 2>&1 && return 0
    elif have apk; then
      apk add --no-cache $pkgs >/dev/null 2>&1 && return 0
    else
      return 1
    fi
    i=$((i + 1))
    sleep 3
  done
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

  # ⚠️ 有些小鸡上 /dev/zero 根本不存在（devtmpfs 没挂好，实测 zouter 就是这样，
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

  # 兜底：openssl 算 sha256，纯 CPU 绑定，几乎处处都有
  say "sysbench 不可用，降级用 openssl"
  ev_progress cpu 60 "openssl 兜底"
  local t0 t1 rate
  t0=$(now_ms)
  dd if=/dev/zero bs=1M count=64 2>/dev/null | openssl sha256 >/dev/null 2>&1
  t1=$(now_ms)
  local ms=$(( t1 - t0 ))
  rate=$(awk "BEGIN{printf \"%.1f\", ($ms>0 ? 64*1000/$ms : 0)}")
  ev_result cpu true "$(printf '{"sha256_mbs":%s,"elapsed_ms":%s,"tool":"openssl"}' "$rate" "$ms")"
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
  ev_progress ip 10 "拉取 IP 质量脚本"
  local raw
  raw=$(curl -fsSL --max-time 60 IP.Check.Place 2>/dev/null)
  if [ -z "$raw" ]; then
    ev_error ip "下载 IPQuality 脚本失败（网络不通？）"
    return
  fi
  say "运行 IPQuality（约 2~5 分钟）"
  ev_progress ip 30 "检测 IP 数据库与流媒体解锁"

  local out
  out=$(printf '%s' "$raw" | bash -s -- -j -y 2>/dev/null)
  if [ -z "$out" ]; then
    ev_error ip "IPQuality 没有输出"
    return
  fi

  # 抠出最外层 JSON（前面有彩色进度，后面有广告）
  local js
  js=$(printf '%s' "$out" | python3 -c '
import sys
s=sys.stdin.read()
i=s.find("{"); j=s.rfind("}")
sys.stdout.write(s[i:j+1] if i>=0 and j>i else "")
' 2>/dev/null)
  if [ -z "$js" ]; then
    ev_error ip "IPQuality 输出里找不到 JSON"
    return
  fi

  # 把它的结构翻译成我们的：只保留卡片上要用的字段
  local brief
  brief=$(printf '%s' "$js" | python3 -c '
import json,sys
try:
    d=json.load(sys.stdin)
except Exception as e:
    sys.exit(1)
info=d.get("Info") or {}
score=d.get("Score") or {}
media=d.get("Media") or {}
short={"DisneyPlus":"Disney+","AmazonPrimeVideo":"Prime","Youtube":"YouTube"}
unlocked=[]; locked=[]; detail={}
for k in sorted(media):
    v=media.get(k) or {}
    st=v.get("Status") or ""
    name=short.get(k,k)
    detail[name]={"status":st,"region":v.get("Region") or ""}
    (unlocked if st=="解锁" else locked).append(name)
def num(x):
    try: return float(str(x).replace("%",""))
    except Exception: return None
out={
  "ip_type": info.get("Type") or "",
  "org": info.get("Organization") or "",
  "asn": info.get("ASN") or "",
  "city": info.get("City") or "",
  "country": info.get("Country") or info.get("CountryCode") or "",
  "risk_scamalytics": num(score.get("SCAMALYTICS")),
  "risk_abuseipdb": num(score.get("AbuseIPDB")),
  "risk_dbip": num(score.get("DBIP")),
  "unlocked": unlocked,
  "locked": locked,
  "unlock_total": len(media),
  "unlock_detail": detail,
}
print(json.dumps(out,ensure_ascii=False))
' 2>/dev/null)

  if [ -z "$brief" ]; then
    ev_error ip "IPQuality 的 JSON 结构不认识（脚本可能改版了）"
    return
  fi
  ev_result ip true "$brief"
}

# 6) 回程路由：nexttrace 优先，装不上就跳过（不硬凑）
test_route() {
  ev_progress route 10 "准备回程路由"
  if ! have nexttrace; then
    install_pkg nexttrace || true
  fi
  if ! have nexttrace; then
    # 官方一键装法；装不上就明确说"跳过"，不要编造结果
    say "尝试用官方脚本安装 nexttrace"
    curl -fsSL --max-time 90 nxtrace.org/nt 2>/dev/null | bash >/dev/null 2>&1 || true
  fi
  if ! have nexttrace; then
    ev_error route "nexttrace 装不上，已跳过（不影响其它项）"
    return
  fi

  # 三个国内代表目标，分别对应电信/联通/移动的骨干
  local targets="202.96.209.133 123.125.99.1 211.136.192.6"
  local names="电信 联通 移动"
  local i=0 json_parts=""
  set -- $targets
  local name_arr=(电信 联通 移动)
  for ip in $targets; do
    local nm=${name_arr[$i]}
    say "回程路由 → $nm ($ip)"
    ev_progress route $(( 20 + i * 25 )) "追踪到 $nm"
    local t
    t=$(nexttrace --json --no-color -q 1 -m 20 "$ip" 2>/dev/null)
    local hops
    hops=$(printf '%s' "$t" | python3 -c '
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit(0)
h=d.get("Hops") or d.get("hops") or []
print(len(h))
' 2>/dev/null)
    [ -n "$json_parts" ] && json_parts="$json_parts,"
    json_parts="$json_parts\"$(jesc "$nm")\":{\"target\":\"$ip\",\"hops\":${hops:-0}}"
    i=$((i+1))
  done
  ev_result route true "{$json_parts}"
}

# ---------- 调度 ----------

ALL_TESTS="env sysinfo disk cpu net ip route"

usage() {
  cat >&2 <<EOF
用法: bash kokoro-bench.sh [选项]

  --only a,b,c   只跑指定项（默认全部）
  --list         列出可跑的项
  --no-install   不自动安装依赖（缺什么就降级/跳过）

可跑的项: $ALL_TESTS
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --only) ONLY="$2"; shift 2 ;;
    --list) echo "$ALL_TESTS"; exit 0 ;;
    --no-install) WANT_INSTALL=0; shift ;;
    -h|--help) usage; exit 0 ;;
    *) shift ;;
  esac
done

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
