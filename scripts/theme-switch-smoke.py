#!/usr/bin/env python
"""访客主题切换的端到端冒烟测试。

单进程内完成「起 Hub → 打请求 → 断言 → 收工」：Windows 上后台进程
会随会话轮次被回收，分步跑必然中途断链。

覆盖：
  - 只有一套内置主题时切换器仍显示（可发现性优先）
  - 管理员导入第二套主题后切换器出现两个选项
  - 未登录访客能切主题，且站点默认主题不受影响
  - 访客切到不存在的主题返回 404
  - 恢复默认 + back 参数的开放重定向防护
  - 主题包端点可用

用法： python scripts/theme-switch-smoke.py
环境变量 KOKORO_BIN 可指定已构建好的二进制，跳过编译。
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

PEER_THEME = {
    "schemaVersion": 1,
    "id": "peer.smoke",
    "name": "冒烟对照",
    "version": "1.0.0",
    "author": "smoke",
    "tokensSchemaVersion": 2,
    "layoutSchemaVersion": 1,
    "tokens": {"--kokoro-color-primary": "#5b8cff"},
    "mode": {"default": "dark", "supportsDark": True, "allowUserSwitch": True},
    "layout": {
        "shell": {"header": {"variant": "blur"}},
        "home": {
            "list": {"mode": "table"},
            "hero": {"enabled": True, "variant": "gradient"},
        },
        "charts": {"type": "line", "grid": False},
    },
}


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
    out = os.path.join(tempfile.gettempdir(), "kokoro-smoke.exe")
    print("构建", out)
    subprocess.check_call(["go", "build", "-o", out, "./cmd/kokoro"], cwd=ROOT)
    return out


class Client:
    """一个带 cookie 的 HTTP 客户端。

    两个刻意的设置：
    1. 关掉代理 —— 沙箱里有一层动态端口的 http_proxy，不关的话
       请求本地服务会被劫持成 502假象；
    2. **不自动跟随重定向** —— 本站大量语义都在 303 上（切换主题、
       恢复默认、导入结果都靠 Location 回传 msg）。urllib 默认跟到
       200，就把这些状态码全糊掉了，测试会既看不出 401 也看不出 303。
    """

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

    def post_form(self, path, fields):
        body = urllib.parse.urlencode(fields).encode()
        req = urllib.request.Request(self.base + path, data=body, method="POST")
        req.add_header("Content-Type", "application/x-www-form-urlencoded")
        return self._open(req)

    def _open(self, req):
        try:
            resp = self.op.open(req, timeout=15)
            return resp.status, dict(resp.headers), resp.read().decode("utf-8", "replace")
        except urllib.error.HTTPError as e:
            return e.code, dict(e.headers), e.read().decode("utf-8", "replace")
        except ConnectionResetError:
            # 服务端在 401 这类分支里直接断开不写 body，属正常现象。
            return 0, {}, ""

    def cookie(self, name):
        for c in self.jar:
            if c.name == name:
                return c.value
        return None


class NoRedirect(urllib.request.HTTPRedirectHandler):
    """把 3xx 当成正常响应返回，而不是跟过去。"""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def body_attr(html, attr):
    m = re.search(re.escape(attr) + r'="([^"]*)"', html)
    return m.group(1) if m else ""


def main():
    exe = build()
    data = tempfile.mkdtemp(prefix="kokoro-smoke-")
    port = free_port()
    base = "http://127.0.0.1:%d" % port
    proc = subprocess.Popen(
        [exe, "serve", "--listen", "127.0.0.1:%d" % port, "--data", data, "--tls", "none"],
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
        errors="replace",
    )
    fails = []
    ok = []
    try:
        # 1) 等待就绪，顺带从日志里抓一次性管理员口令
        deadline = time.time() + 25
        admin = None
        ready = False
        while time.time() < deadline:
            if proc.poll() is not None:
                print("Hub 启动失败:\n", proc.stdout.read())
                return 1
            line = proc.stdout.readline()
            if not line:
                time.sleep(0.05)
                continue
            m = re.search(r"已生成管理员账号: (\S+) / (\S+)", line)
            if m:
                admin = (m.group(1), m.group(2))
            if admin:
                try:
                    st, _, _ = Client(base).get("/theme.json")
                    if st == 200:
                        ready = True
                        break
                except Exception:
                    pass
        if not ready or not admin:
            print("超时未就绪或没拿到管理员口令")
            return 1

        # 2) 站点当前主题
        c = Client(base)
        st, _, body = c.get("/theme.json")
        m = json.loads(body)
        if m["id"] != "kokoro.daylight":
            fails.append("内置主题应只剩 kokoro.daylight，实际 %s" % m["id"])
        else:
            ok.append("内置主题只剩 kokoro.daylight（%s，列表=%s）" % (
                m["name"], m["layout"]["home"]["list"]["mode"]))

        # 3) 单主题时切换器**也要出现** —— 只有一套也显示。
        #    它是访客唯一能看到"本站支持换外观"和"我正用着哪套"的地方。
        #    位置在页头右上角（data-skin-picker），不是页尾。
        st, _, home = c.get("/")
        if "data-skin-picker" not in home:
            fails.append("只有一套主题时皮肤切换器被隐藏了（应显示）")
        elif "/pick/kokoro.daylight" not in home:
            fails.append("单主题切换器里没有访客切换路径 /pick/")
        else:
            n = len(re.findall(r'class="skin-item(?: on)?"', home))
            ok.append("单主题下切换器仍显示（%d 个选项，指向 /pick/）" % n)
        # 页头右上角：必须在 <header class="top"> 之内
        head_end = home.find("</header>")
        if head_end < 0 or home.find("data-skin-picker") > head_end:
            fails.append("皮肤切换器不在页头右上角")
        else:
            ok.append("皮肤切换器位于页头右上角")

        # 4) 未登录不能导入主题
        anon = Client(base)
        st, _, _ = anon.post_form("/admin/themes/import", {"manifest": json.dumps(PEER_THEME)})
        if st != 401:
            fails.append("未登录导入主题应 401，实际 %s" % st)
        else:
            ok.append("未登录导入主题被拒（401）")

        # 5) 管理员登录并导入第二套主题
        adm = Client(base)
        st, hdr, _ = adm.post_form("/admin", {"username": admin[0], "password": admin[1]})
        if st != 303:
            fails.append("管理员登录应 303，实际 %s" % st)
        else:
            ok.append("管理员登录成功")
        st, hdr, body = adm.post_form("/admin/themes/import",
                                      {"manifest": json.dumps(PEER_THEME)})
        loc = hdr.get("Location", "")
        if st != 303 or "msg=" not in loc:
            fails.append("管理员导入主题失败，status=%s Location=%s" % (st, loc[:120]))
        else:
            from urllib.parse import parse_qs, urlparse
            msg = parse_qs(urlparse(loc).query).get("msg", [""])[0]
            if "导入失败" in msg:
                fails.append("管理员导入主题被拒：%s" % msg)
            else:
                ok.append("管理员导入社区主题：%s" % msg)

        # 6) 两套主题时，首页主题条应出现，且给出访客可用的 /pick/ 链接
        st, _, home = c.get("/")
        if "data-skin-picker" not in home:
            fails.append("两套主题时皮肤切换器未出现")
        elif "/pick/" not in home:
            fails.append("皮肤切换器没有指向访客切换路径 /pick/")
        elif "/theme/" in home:
            fails.append("皮肤切换器仍在用管理员路径 /theme/")
        else:
            chips = re.findall(r'href="(/pick/[^"]+)"', home)
            ok.append("皮肤切换器出现，访客入口 %s" % chips)
        # back 参数不能被双重转义成 %252F
        if "back=%252F" in home:
            fails.append("back 参数被双重转义（%252F）")
        else:
            ok.append("back 参数转义正确")

        # 7) 访客切换（未登录）：应 303 + 写 k_theme，且站点主题不变
        st, hdr, _ = c.get("/pick/peer.smoke?back=%2F")
        if st != 303:
            fails.append("访客切换主题应 303，实际 %s" % st)
        elif c.cookie("k_theme") != "peer.smoke":
            fails.append("k_theme cookie 未写入，实际 %r" % c.cookie("k_theme"))
        else:
            ok.append("未登录访客切换成功（k_theme=peer.smoke）")
        st, _, body = Client(base).get("/theme.json")
        if json.loads(body)["id"] != "kokoro.daylight":
            fails.append("访客切换把站点默认主题改掉了")
        else:
            ok.append("站点默认主题未被访客影响")

        # 8) 访客视角：布局真的跟着换了
        st, _, home = c.get("/")
        if body_attr(home, "data-k-list-mode") != "table":
            fails.append("访客主题未生效，列表=%s" % body_attr(home, "data-k-list-mode"))
        elif body_attr(home, "data-k-header") != "blur":
            fails.append("访客主题顶栏未生效，=%s" % body_attr(home, "data-k-header"))
        elif body_attr(home, "data-k-chart-grid") != "0":
            fails.append("grid:false 未落到 data-k-chart-grid")
        elif body_attr(home, "data-k-hero-on") != "1":
            fails.append("Hero 启用标记缺失")
        else:
            ok.append("访客视角布局全换：table / blur 顶栏 / 无网格 / 渐变 Hero")
        # 别的访客不受影响
        st, _, other = Client(base).get("/")
        if body_attr(other, "data-k-list-mode") != "card":
            fails.append("访客的主题泄漏给了其他访客")
        else:
            ok.append("其他访客不受影响")

        # 9) 切到不存在的主题 → 404
        st, _, _ = c.get("/pick/no.such.theme")
        if st != 404:
            fails.append("切到不存在的主题应 404，实际 %s" % st)
        else:
            ok.append("切到不存在主题返回 404")

        # 10) 恢复默认
        st, hdr, _ = c.get("/pick/default?back=%2F")
        if st != 303 or hdr.get("Location") != "/":
            fails.append("/pick/default 应 303→/，实际 %s %s" % (st, hdr.get("Location")))
        else:
            ok.append("/pick/default 回到站点默认")

        # 11) 开放重定向防护
        for bad in ["https://evil.example/x", "//evil.example/x", "/\\evil", "http://evil"]:
            st, hdr, _ = c.get("/pick/default?back=" + urllib.parse.quote(bad, safe=""))
            loc = hdr.get("Location", "")
            if "evil" in loc:
                fails.append("back=%s 存在开放重定向 → %s" % (bad, loc))
        ok.append("back 参数的开放重定向已挡住")

        # 12) 列表模式 cookie 仍生效
        st, _, home = c.get("/", )
        st, _, home = Client(base).get("/")
        req_client = Client(base)
        req_client.jar.set_cookie(http.cookiejar.Cookie(
            0, "k_list_mode", "compact", None, False,
            "127.0.0.1", False, False, "/", True, False, None, True, None, None, {}))
        st, _, home = req_client.get("/")
        if body_attr(home, "data-k-list-mode") != "compact":
            fails.append("列表形态 cookie 未生效")
        else:
            ok.append("列表形态 cookie 生效")

        # 13) 主题包端点
        st, hdr, raw = Client(base).get("/theme-bundle/kokoro.daylight")
        if st != 200 or raw[:2] != "PK":
            fails.append("主题包端点异常：status=%s magic=%r" % (st, raw[:2]))
        else:
            ok.append("主题包端点返回 zip（%d 字节）" % len(raw))

    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except Exception:
            proc.kill()
        shutil.rmtree(data, ignore_errors=True)

    for line in ok:
        print("  ✓", line)
    print()
    if fails:
        print("失败 %d 项：" % len(fails))
        for f in fails:
            print("  ✗", f)
        return 1
    print("全部 %d 项通过" % len(ok))
    return 0


if __name__ == "__main__":
    sys.exit(main())
