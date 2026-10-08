#!/usr/bin/env python
"""站长名片（用户名 / 头像 / 个人签名）端到端验证。

为什么要单开一个脚本：这条链路横跨「multipart 上传 → 落盘 → 写 settings →
首页模板渲染 → /avatar 路由回吐文件」五段，任何一段断了，
看后台表单都是"保存成功"，但首页什么都没有。所以必须整条跑通再断言。

用法： python scripts/owner-profile-smoke.py
环境变量 KOKORO_BIN 可指定已构建好的二进制。
"""
import binascii
import http.cookiejar
import os
import re
import socket
import struct
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import shutil
import zlib

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NAME = "站长阿宝"
BIO = "只卖靠谱的小鸡，跑路包赔。"


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def png_bytes(w=8, h=8):
    """造一个最小合法 PNG。不引第三方库，手写 chunk。"""
    def chunk(tag, data):
        c = tag + data
        return struct.pack(">I", len(data)) + c + struct.pack(">I", binascii.crc32(c) & 0xFFFFFFFF)
    raw = b""
    for _ in range(h):
        raw += b"\x00" + b"\xff\x80\x40\xff" * w  # 每行开头是 filter byte
    return (b"\x89PNG\r\n\x1a\n"
            + chunk(b"IHDR", struct.pack(">IIBBBBB", w, h, 8, 6, 0, 0, 0))
            + chunk(b"IDAT", zlib.compress(raw))
            + chunk(b"IEND", b""))


def build():
    env_bin = os.environ.get("KOKORO_BIN", "")
    if env_bin and os.path.exists(env_bin):
        return env_bin
    out = os.path.join(tempfile.gettempdir(), "kokoro-owner-smoke.exe")
    print("构建", out)
    subprocess.check_call(["go", "build", "-o", out, "./cmd/kokoro"], cwd=ROOT)
    return out


class Client:
    def __init__(self, base):
        self.base = base
        self.jar = http.cookiejar.CookieJar()
        # 关代理：沙箱里那层动态端口代理会把本地请求劫持成 502 假象。
        # 不跟随重定向：本站语义大量在 303 上，跟过去就把状态码糊掉了。
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

    def post_multipart(self, path, fields, files):
        """files: [(字段名, 文件名, bytes)]"""
        boundary = "----kokoroSmokeBoundary7d9a"
        parts = []
        for k, v in fields.items():
            parts.append(
                ("--%s\r\nContent-Disposition: form-data; name=\"%s\"\r\n\r\n%s\r\n"
                 % (boundary, k, v)).encode("utf-8"))
        for name, filename, data in files:
            parts.append(
                ("--%s\r\nContent-Disposition: form-data; name=\"%s\"; filename=\"%s\"\r\n"
                 "Content-Type: application/octet-stream\r\n\r\n" % (boundary, name, filename)).encode("utf-8"))
            parts.append(data)
            parts.append(b"\r\n")
        parts.append(("--%s--\r\n" % boundary).encode("utf-8"))
        body = b"".join(parts)
        req = urllib.request.Request(self.base + path, data=body, method="POST")
        req.add_header("Content-Type", "multipart/form-data; boundary=" + boundary)
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
    data = tempfile.mkdtemp(prefix="kokoro-owner-")
    port = free_port()
    base = "http://127.0.0.1:%d" % port
    logf = open(os.path.join(data, "hub.log"), "wb")
    # --tls none 是必须的：--tls proxy（默认）会让会话 cookie 带上 Secure，
    # 而 Python 的 cookiejar 在 http:// 上会**静默丢弃** Secure cookie，
    # 于是后续请求全部未登录、后台一律 303 跳走——
    # 症状是"保存成功但什么都没存"，而不是一个明显的 401，极难排查。
    proc = subprocess.Popen([exe, "serve", "--listen", "127.0.0.1:%d" % port,
                             "--data", data, "--site", "Kokoro", "--tls", "none"],
                            stdout=logf, stderr=subprocess.STDOUT,
                            cwd=tempfile.gettempdir())
    ok, fails = [], []
    try:
        for _ in range(100):
            try:
                s = socket.create_connection(("127.0.0.1", port), 0.3)
                s.close()
                break
            except OSError:
                time.sleep(0.15)
        else:
            print("Hub 没起来"); return 1

        # 从日志里取一次性管理员口令
        logf.flush()
        txt = open(os.path.join(data, "hub.log"), "rb").read().decode("utf-8", "replace")
        m = re.search(r"已生成管理员账号: (\S+) / (\S+)", txt)
        if not m:
            print("没拿到管理员口令：\n", txt[-2000:]); return 1
        user, pw = m.group(1), m.group(2)
        ok.append("拿到一次性管理员账号")

        c = Client(base)
        # 1) 未登录时首页不该出现站长名片
        st, _, home = c.get("/")
        if "owner" in home.decode("utf-8", "replace") and NAME in home.decode("utf-8", "replace"):
            fails.append("未设置资料时首页就出现了站长名片")
        else:
            ok.append("未设置资料时首页不显示站长名片（避免空名片）")

        # 2) 上传头像 + 昵称 + 签名
        # 注意登录端点是 /admin 本身（GET 出表单、POST 校验），不是 /admin/login。
        st, _, _ = c.post_form("/admin", {"username": user, "password": pw})
        if st not in (302, 303):
            fails.append("管理员登录失败，状态 %s" % st)
            print("\n".join(fail_line(f) for f in fails)); return 1
        ok.append("管理员登录成功")

        st, _, body = c.post_multipart(
            "/admin/profile",
            {"owner_name": NAME, "owner_bio": BIO},
            [("avatar", "me.png", png_bytes())],
        )
        if st != 303:
            fails.append("保存站长资料状态 %s，应为 303（%s）" % (st, body[:200]))
        else:
            ok.append("站长资料保存成功（303）")

        # 3) 文件真的落盘了
        files = [f for f in os.listdir(data) if f.startswith("avatar.")]
        if not files:
            fails.append("数据目录里没有头像文件：%s" % os.listdir(data))
        else:
            sz = os.path.getsize(os.path.join(data, files[0]))
            ok.append("头像已落盘 %s（%d 字节）" % (files[0], sz))

        # 4) /avatar 能取回图片
        st, hdrs, raw = c.get("/avatar")
        if st != 200:
            fails.append("/avatar 返回 %s" % st)
        elif not hdrs.get("Content-Type", "").startswith("image/"):
            fails.append("/avatar Content-Type = %s" % hdrs.get("Content-Type"))
        elif raw[:8] != b"\x89PNG\r\n\x1a\n":
            fails.append("/avatar 吐出来的不是 PNG")
        else:
            ok.append("/avatar 正常回吐图片（%s，%d 字节）" % (hdrs.get("Content-Type"), len(raw)))

        # 5) 首页要出现昵称、签名、头像
        st, _, raw = c.get("/")
        home = raw.decode("utf-8", "replace")
        if NAME not in home:
            fails.append("首页没出现用户名 %s" % NAME)
        else:
            ok.append("首页显示用户名")
        if BIO not in home:
            fails.append("首页没出现个人签名")
        else:
            ok.append("首页显示个人签名")
        if 'class="avatar"' not in home:
            fails.append("首页没渲染头像 <img class=\"avatar\">")
        else:
            ok.append("首页显示头像")

        # 6) 头像带版本参数（否则换了头像浏览器还用旧图）
        if re.search(r'/avatar\?v=\d+', home):
            ok.append("头像 URL 带版本参数（换图不会被缓存挡住）")
        else:
            fails.append("头像 URL 没带 ?v= 版本参数")

        # 7) 未登录访客也能看到站长名片
        anon = Client(base)
        st, _, raw = anon.get("/")
        anon_home = raw.decode("utf-8", "replace")
        if NAME in anon_home and BIO in anon_home:
            ok.append("未登录访客也能看到站长名片")
        else:
            fails.append("访客看不到站长名片")

        # 8) 清除头像
        st, _, _ = c.post_multipart("/admin/profile",
                                    {"owner_name": NAME, "owner_bio": BIO, "avatar_clear": "1"}, [])
        st2, _, raw = c.get("/avatar")
        if st2 == 200:
            fails.append("清除头像后 /avatar 仍返回 200")
        else:
            ok.append("清除头像生效（/avatar 已 %s）" % st2)

    finally:
        proc.kill()
        try:
            proc.wait(timeout=10)
        except Exception:
            pass
        shutil.rmtree(data, ignore_errors=True)

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


def fail_line(f):
    return "  ✗ " + f


if __name__ == "__main__":
    sys.exit(main())
