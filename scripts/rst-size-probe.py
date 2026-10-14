#!/usr/bin/env python
"""按 body 大小分档，探测未登录 POST 的响应可读性。

动机：Go 标准库在 handler 返回时**自己就会抽干**未读请求体，
上限是 maxPostHandlerReadBytes = 256KB（见 $GOROOT/src/net/http/server.go）。
抽不干时它会把连接标成 close-after-reply 并且不再复用。

所以「未读 body 触发 RST」这件事只在 body 大于 256KB 时才可能出现。
这个脚本按档位扫一遍，把真实边界测出来，而不是靠猜。

同时区分两种失败形态，因为它们指向完全不同的原因：
  - 状态行读到了，但正文被截断/为空  -> 服务端逻辑问题
  - 状态行都读不到（recv 返回 0 或 ECONNRESET）
    -> RST。对端主动 reset，与「正文没写」是两回事

用法：
    python scripts/rst-size-probe.py <hub_base_url>
"""
import socket
import sys
import time

# 围绕 64KB（我们的 drainBody 上限）与 256KB（stdlib 上限）取档
SIZES = [
    ("0B", 0),
    ("1KB", 1024),
    ("63KB", 63 * 1024),
    ("65KB", 65 * 1024),
    ("255KB", 255 * 1024),
    ("257KB", 257 * 1024),
    ("1MB", 1024 * 1024),
    ("3MB", 3 * 1024 * 1024),
]

PATHS = [
    ("/admin/themes/delete", "401"),   # 底层动作 -> 401
    ("/admin/comments", "303"),        # 页面型表单 -> 303 回登录页
]


def probe(host, port, path, size, want):
    """返回 (是否可读, 状态行, 备注)。"""
    body = b"id=" + b"A" * size
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(10)
    try:
        s.connect((host, port))
    except OSError as e:
        return False, "", "connect失败 %s" % e

    head = (
        "POST %s HTTP/1.1\r\nHost: %s:%d\r\n"
        "Content-Type: application/x-www-form-urlencoded\r\n"
        "Content-Length: %d\r\nConnection: close\r\n\r\n"
        % (path, host, port, len(body))
    )
    try:
        s.sendall(head.encode())
        # body 分块发，模拟真实客户端的写入节奏
        step = 64 * 1024
        for off in range(0, len(body), step):
            s.sendall(body[off:off + step])
        s.shutdown(socket.SHUT_WR)
    except OSError as e:
        return False, "", "send失败 %s" % e

    time.sleep(0.12)

    buf = b""
    while b"\r\n\r\n" not in buf:
        try:
            chunk = s.recv(8192)
        except ConnectionResetError:
            return False, "", "ECONNRESET"
        except socket.timeout:
            return False, "", "超时"
        except OSError as e:
            return False, "", "recv失败 %s" % e
        if not chunk:
            return False, "", "对端关闭且零字节响应"
        buf += chunk

    line = buf.split(b"\r\n", 1)[0].decode("latin-1").strip()
    ok = (" " + want + " ") in line
    return ok, line, ("%d 字节头部" % len(buf))


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    base = sys.argv[1].rstrip("/")
    hp = base.split("//", 1)[1]
    host, _, port_s = hp.partition(":")
    port = int(port_s or 80)

    bad = 0
    for path, want in PATHS:
        print("=== %s （期望 %s）===" % (path, want))
        for label, size in SIZES:
            ok, line, note = probe(host, port, path, size, want)
            if ok:
                print("  ok   %-7s %s" % (label, line))
            else:
                bad += 1
                print("  FAIL %-7s 读不到有效响应：%s" % (label, note))
        print()

    print("失败档位数：%d" % bad)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
