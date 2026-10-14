#!/bin/sh
# 本地冒烟：起一个临时 Hub，注册一台假小鸡，跑通「博客页 + 留言 + 赞踩 + 后台显示/隐藏」。
# 只在开发机跑，用完即删数据目录。
set -u
ROOT=$(cd "$(dirname "$0")/.." && pwd)
BIN="$ROOT/dist/kokoro.exe"
DATA=$(mktemp -d)
PORT=${PORT:-8899}
LOG="$DATA/hub.log"
CJ="$DATA/cj"

"$BIN" serve --listen 127.0.0.1:$PORT --data "$DATA" >"$LOG" 2>&1 &
PID=$!
trap 'kill $PID 2>/dev/null' EXIT
sleep 2

C() { curl -s --noproxy '*' --max-time 10 -c "$CJ" -b "$CJ" "$@"; }
BASE="http://127.0.0.1:$PORT"

echo "== 1. 首页 =="
C -o /dev/null -w "home %{http_code}\n" "$BASE/"

echo "== 2. 取安装令牌 =="
TOKEN=$(grep -o 'it_[0-9a-f]*' "$LOG" | head -1)
echo "token=$TOKEN"

echo "== 3. 注册假节点 =="
REG=$(C -X POST "$BASE/api/v1/register" -H 'Content-Type: application/json' \
  -d "{\"install_token\":\"$TOKEN\",\"hostname\":\"smoke\",\"os\":\"linux\",\"arch\":\"amd64\",\"cpu_cores\":2,\"mem_total\":2147483648,\"disk_total\":21474836480}")
echo "reg=$REG"
NODE=$(echo "$REG" | grep -o 'nd_[0-9a-f]*' | head -1)
ADMIN_PASS=$(grep -o '管理员口令: [0-9a-f]*' "$LOG" | head -1 | awk '{print $2}')

C -o /dev/null -X POST "$BASE/admin" -d "password=$ADMIN_PASS"
SLUG=$(C "$BASE/admin" | grep -o "/n/[A-Za-z0-9_-]*" | head -1 | cut -d/ -f3)
echo "node=$NODE slug=$SLUG"

echo "== 4. 详情页 =="
C -o /dev/null -w "node page %{http_code}\n" "$BASE/n/$SLUG"

echo "== 5. 留言 =="
C -o /dev/null -w "comment %{http_code}\n" -X POST "$BASE/n/$SLUG/comment" \
  --data-urlencode "author=smoke" --data-urlencode "contact=tg:@smoke" \
  --data-urlencode "content=这台机器我要了，多少钱？"

echo "== 6. 点赞/改票/撤票 =="
C -X POST "$BASE/n/$SLUG/vote" -d "target=node" -d "id=$NODE" -d "v=up" -d "json=1"; echo
C -X POST "$BASE/n/$SLUG/vote" -d "target=node" -d "id=$NODE" -d "v=up" -d "json=1"; echo
C -X POST "$BASE/n/$SLUG/vote" -d "target=node" -d "id=$NODE" -d "v=down" -d "json=1"; echo

echo "== 7. 详情页内容 =="
C "$BASE/n/$SLUG" > "$DATA/page.html"
grep -o 'class="cmt-body">[^<]*' "$DATA/page.html" | head -3
grep -o '<b data-count="up">[0-9]*' "$DATA/page.html" | head -2
grep -o '联系方式：[^<]*' "$DATA/page.html" | head -1

echo "== 8. 后台登录页 =="
C -o /dev/null -w "admin %{http_code}\n" "$BASE/admin"

echo "== 9. 显示/隐藏开关 =="
C -o /dev/null -w "toggle %{http_code}\n" -X POST "$BASE/admin/nodes" -d "action=toggle" -d "id=$NODE"
C -o /dev/null -w "hidden page %{http_code}\n" "$BASE/n/$SLUG"
C -o /dev/null -w "toggle back %{http_code}\n" -X POST "$BASE/admin/nodes" -d "action=toggle" -d "id=$NODE"
C -o /dev/null -w "visible page %{http_code}\n" "$BASE/n/$SLUG"

echo "== 10. 后台审核区 =="
C "$BASE/admin" | grep -c '留言审核'
C "$BASE/admin" | grep -o 'tg:@smoke' | head -1

echo "== 日志尾部 =="
tail -6 "$LOG"
rm -rf "$DATA"
