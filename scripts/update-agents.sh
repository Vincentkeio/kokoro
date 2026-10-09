#!/bin/bash
# 更新一批机器上的 kokoro-agent 二进制（**不重跑 install.sh！**）
#
# ⚠️ 绝不重跑 install.sh：它会拿安装令牌重新注册，生成重复节点。
#    /etc/kokoro/config.toml 里的 nt_ 节点令牌必须原样保留。
#    这里只换二进制 + 重启。
#
# 用法: bash scripts/update-agents.sh

set -u
cd "$(dirname "$0")/.." || exit 1

# 远端要跑的：停 → 下载 → 换二进制 → 起 → 报状态与哈希
REMOTE='set -e
systemctl stop kokoro-agent 2>/dev/null || true
curl -fsSL https://vps.mjfuns.lat/api/v1/dl/kokoro-agent-linux-amd64 -o /tmp/ka.new
install -m 0755 /tmp/ka.new /usr/local/bin/kokoro-agent
rm -f /tmp/ka.new
systemctl start kokoro-agent
sleep 2
echo "  状态: $(systemctl is-active kokoro-agent)"
echo "  哈希: $(sha256sum /usr/local/bin/kokoro-agent | cut -c1-16)"'

KEY="/c/Users/yifen/.ssh/quadwit-vps.pem"

upd_key() {  # port host
  ssh -i "$KEY" -p "$1" -o BatchMode=yes -o StrictHostKeyChecking=no \
      -o ConnectTimeout=25 root@"$2" "$REMOTE"
}

upd_pass() {  # port host password
  local ap
  ap=$(mktemp)
  printf '#!/bin/sh\ncat <<"PWEOL"\n%s\nPWEOL\n' "$3" > "$ap"
  chmod 700 "$ap"
  SSH_ASKPASS="$ap" SSH_ASKPASS_REQUIRE=force DISPLAY=:0 \
    ssh -T -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        -o ConnectTimeout=25 -o NumberOfPasswordPrompts=1 \
        -p "$1" root@"$2" "$REMOTE"
  local rc=$?
  rm -f "$ap"
  return $rc
}

echo "=================== DMIT（Hub 本身，密钥）==================="
upd_key 4256 179.255.97.80

echo "=================== 东京 zouter ==================="
upd_pass 4377 216.23.83.149 '6iKz-8z7x-zEhg'

echo "=================== zgo · 洛杉矶 ==================="
upd_pass 35003 64.83.30.108 '2rzFJn8pcjpzNG7C'

echo "=================== wawo · 香港 ==================="
upd_pass 6777 185.92.47.101 '9cVJEmgTEoilUMSLUXie'

echo "=================== ByteVirt · 新加坡 ==================="
upd_pass 30851 natsg1.bytevirt.net 'w|#-4=7Po,V@'

echo "=================== 全部完成 ==================="
