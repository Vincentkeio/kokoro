#!/usr/bin/env python
"""首页改版的端到端验证：真的注册一台小鸡、真的上报数据，然后看页面。

为什么不用 Go 测试：那只能证明服务端吐出的 HTML 里有这些标签，
证明不了 **WebGL 地球真的画出来了**。地球要靠 GPU/着色器，必须在真浏览器里跑。
所以这里起真实进程 → 走真实的注册/上报接口 → 用 Edge 无头截图。

用法： python scripts/home-rich-smoke.py
环境变量 KOKORO_BIN 可指定已构建好的二进制。
"""
import http.cookiejar
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
EDGE = r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe"


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def build():
    env_bin = os.environ.get("KOKORO_BIN", "")
    if env_bin and os.path.exists(env_bin):
        return env_bin
    out = os.path.join(tempfile.gettempdir(), "kokoro-home-smoke.exe")
    print("构建", out)
    subprocess.check_call(["go", "build", "-o", out, "./cmd/kokoro"], cwd=ROOT)
    return out


class Client:
    def __init__(self, base):
        self.base = base
        self.jar = http.cookiejar.CookieJar()
        self.op = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPCookieProcessor(self.jar),
            NoRedirect(),
        )

    def get(self, path):
        return self._open(urllib.request.Request(self.base + path, method="GET"))

    def post_json(self, path, obj, headers=None):
        body = json.dumps(obj).encode()
        req = urllib.request.Request(self.base + path, data=body, method="POST")
        req.add_header("Content-Type", "application/json")
        for k, v in (headers or {}).items():
            req.add_header(k, v)
        return self._open(req)

    def post_form(self, path, fields):
        body = urllib.parse.urlencode(fields).encode()
        req = urllib.request.Request(self.base + path, data=body, method="POST")
        req.add_header("Content-Type", "application/x-www-form-urlencoded")
        return self._open(req)

    def _open(self, req):
        try:
            resp = self.op.open(req, timeout=20)
            return resp.status, dict(resp.headers), resp.read()
        except urllib.error.HTTPError as e:
            return e.code, dict(e.headers), e.read()
        except ConnectionResetError:
            return 0, {}, b""


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def main():
    exe = build()
    data = tempfile.mkdtemp(prefix="kokoro-home-")
    port = free_port()
    base = "http://127.0.0.1:%d" % port
    logf = open(os.path.join(data, "hub.log"), "wb")
    # 必须 --tls none：--tls proxy 会让会话 cookie 带 Secure，
    # Python 的 cookiejar 在 http 上会静默丢弃，表现为"登录成功但什么都没保存"。
    proc = subprocess.Popen([exe, "serve", "--listen", "127.0.0.1:%d" % port,
                             "--data", data, "--site", "Kokoro", "--tls", "none",
                             "--domain", "vps.mjfuns.lat"],
                            stdout=logf, stderr=subprocess.STDOUT,
                            cwd=tempfile.gettempdir())
    ok, fails = [], []
    try:
        for _ in range(120):
            try:
                s = socket.create_connection(("127.0.0.1", port), 0.3)
                s.close()
                break
            except OSError:
                time.sleep(0.15)
        else:
            print("Hub 没起来")
            return 1

        logf.flush()
        txt = open(os.path.join(data, "hub.log"), "rb").read().decode("utf-8", "replace")
        m = re.search(r"已生成管理员账号: (\S+) / (\S+)", txt)
        if not m:
            print("拿不到管理员口令:\n", txt[-1500:])
            return 1
        user, pw = m.group(1), m.group(2)
        ok.append("拿到一次性管理员账号")

        adm = Client(base)
        st, _, _ = adm.post_form("/admin", {"username": user, "password": pw})
        if st not in (302, 303):
            fails.append("管理员登录失败 %s" % st)
        else:
            ok.append("管理员登录成功")

        # 1) 建安装令牌
        st, _, _ = adm.post_form("/admin/tokens", {"action": "create", "label": "smoke"})
        st, _, page = adm.get("/admin")
        page = page.decode("utf-8", "replace")
        tm = re.search(r"(it_[A-Za-z0-9]+)", page)
        if not tm:
            fails.append("后台页面上找不到安装令牌")
            return report(ok, fails)
        token = tm.group(1)
        ok.append("安装令牌已创建")

        # 2) 注册两台不同位置的节点 —— 地球要看出它们分处不同大洲
        nodes = [
            {"name": "东京 zouter", "host": "jp-tokyo", "country": "JP", "region": "日本 · 东京",
             "cores": 2, "mem": 4 << 30, "disk": 40 << 30, "os": "Debian 13"},
            {"name": "洛杉矶 dmit", "host": "us-la", "country": "US", "region": "美国 · 洛杉矶",
             "cores": 4, "mem": 8 << 30, "disk": 80 << 30, "os": "Ubuntu 24.04"},
        ]
        agents = []
        for n in nodes:
            st, _, body = Client(base).post_json("/api/v1/register", {
                "install_token": token, "hostname": n["host"], "os": n["os"],
                "kernel": "6.1.0", "arch": "x86_64", "agent_version": "0.1.0",
                "cpu_cores": n["cores"], "mem_total": n["mem"], "disk_total": n["disk"],
            })
            if st != 200:
                fails.append("注册 %s 失败 HTTP %s %s" % (n["name"], st, body[:200]))
                continue
            r = json.loads(body)
            agents.append((n, r["node_token"], r["node_id"]))
            ok.append("已注册 %s" % n["name"])

        # 3) 打上国家/地区与标签（走后台表单，和真实操作一致）
        for n, _, nid in agents:
            st, _, _ = adm.post_form("/admin/nodes", {
                "action": "rename", "id": nid, "name": n["name"],
                "country": n["country"], "region": n["region"],
                "tags": "香港, CN2, %s" % n["host"],
            })
            if st != 303:
                fails.append("设置 %s 的地区失败 HTTP %s" % (n["name"], st))
        ok.append("已写入地区与自定义标签")

        # 4) 上报指标（带 NetQ，卡片上才有延迟）
        now = int(time.time() * 1000)
        for n, tok, nid in agents:
            st, _, body = Client(base).post_json("/api/v1/report", {
                "v": 1, "ts": now, "seq": 1,
                "host": {"uptime": 11 * 3600, "procs": 120},
                "cpu": {"usage": 12.5, "load1": 0.42, "load5": 0.3, "load15": 0.2},
                "mem": {"total": n["mem"], "used": n["mem"] // 3},
                "disk": {"total": n["disk"], "used": n["disk"] // 5},
                "net": {"up": 464, "down": 988, "total_up": 1 << 30, "total_down": 3 << 30},
                "netq": {"hub_latency_ms": 42},
            }, headers={"Authorization": "Bearer " + tok})
            if st != 200:
                fails.append("上报 %s 失败 HTTP %s %s" % (n["name"], st, body[:200]))
        ok.append("已上报指标")

        # 5) 配置面板坐标（地球航线的中心）
        st, _, _ = adm.post_form("/admin/settings", {
            "comment_enabled": "1", "hub_lat": "34.05", "hub_lon": "-118.24"})
        if st != 303:
            fails.append("保存面板坐标失败 HTTP %s" % st)
        else:
            ok.append("已配置面板坐标")

        # 6) 抓首页，检查服务端吐出的东西
        st, _, raw = adm.get("/")
        html = raw.decode("utf-8", "replace")
        for key, label in [
            ('class="clocks"', "时钟条"),
            ('class="stats"', "统计卡"),
            ('class="globe"', "地球容器"),
            ('id="globe-data"', "地球数据"),
            ('/static/globe.js', "地球脚本"),
            ('class="gauges"', "资源条"),
            ('class="specs"', "规格表"),
            ('class="tags"', "自定义标签"),
        ]:
            if key in html:
                ok.append("首页含%s" % label)
            else:
                fails.append("首页缺少%s（%s）" % (label, key))
        # 网络曲线读的是 5 分钟聚合表，而聚合是后台每 5 分钟跑一次的。
        # 冒烟测试刚插完原始点就截图，必然还没聚合——所以这里接受
        # 「暂时没有历史数据」的占位，只要不是两边都没有就行。
        # 真正跑通聚合→曲线的链路由 TestHomeSparklineFromAggregated 覆盖。
        if 'class="spark"' in html:
            ok.append("首页含网络曲线")
        elif "还没有足够的历史数据" in html:
            ok.append("首页显示「暂无历史数据」占位（聚合还没跑，属预期）")
        else:
            fails.append("既没有网络曲线，也没有占位文案")

        gm = re.search(r'<script id="globe-data"[^>]*>(.*?)</script>', html, re.S)
        if gm:
            g = json.loads(gm.group(1))
            if g.get("hasHub") and len(g.get("places", [])) == 2:
                ok.append("地球数据：2 个节点 + 已配置主机")
            else:
                fails.append("地球数据不对：hasHub=%s places=%d"
                             % (g.get("hasHub"), len(g.get("places", []))))
        else:
            fails.append("解析不到地球数据")

        # 7) 真浏览器截图 —— 只有这里能证明 WebGL 真的画出来了
        shot = os.path.join(ROOT, "work", "preview", "home-real.png")
        os.makedirs(os.path.dirname(shot), exist_ok=True)
        if os.path.exists(EDGE):
            r = subprocess.run([
                EDGE, "--headless=new", "--no-sandbox", "--hide-scrollbars",
                "--enable-unsafe-swiftshader", "--window-size=1400,1400",
                "--screenshot=" + shot, base + "/"],
                capture_output=True, timeout=120)
            if os.path.exists(shot) and os.path.getsize(shot) > 20000:
                ok.append("已截图 %s（%.0f KB）" % (os.path.relpath(shot, ROOT),
                                                  os.path.getsize(shot) / 1024))
            else:
                fails.append("截图失败")
        else:
            ok.append("（本机没有 Edge，跳过截图）")

    finally:
        proc.kill()
        try:
            proc.wait(timeout=10)
        except Exception:
            pass
        shutil.rmtree(data, ignore_errors=True)

    return report(ok, fails)


def report(ok, fails):
    for line in ok:
        print("  ✓", line)
    for line in fails:
        print("  ✗", line)
    print()
    if fails:
        print("失败 %d 项，通过 %d 项" % (len(fails), len(ok)))
        return 1
    print("全部 %d 项通过" % len(ok))
    return 0


if __name__ == "__main__":
    sys.exit(main())
