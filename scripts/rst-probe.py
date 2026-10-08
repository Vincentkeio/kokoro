#!/usr/bin/env python
"""用裸 socket 探测：未登录 POST 带 body 时，客户端到底读不读得到响应。

这个脚本刻意不用 urllib / http.client，而是自己拼报文、走 socket。
原因：urllib 和 Go 的 http.Client 在对端发RST 时抛的是
ConnectionResetError / EOF，两者都**不携带任何响应内容**，
所以「服务端到底写没写出401」和「报文有没有被内核丢掉」是分不开的。
只有裸 socket能把这两件事分开看。

同时输出 TCP 层细节，方便判断是 RST 还是正常 FIN。

用法：
    python scripts/rst-probe.py <hub_base_url>

退出码 0 表示所有端点都读到了预期状态码。
"""
import os
import socket
import sys
import time

CASES = [
    # (路径, body, 期望出现在状态行里的状态码)
    ("/admin/themes/import", b"manifest=" + b"A" * 2048, "401"),
    ("/admin/themes/delete", b"id=kokoro.daylight", "401"),
    ("/admin/themes/grab", b"url=https%3A%2F%2Fexample.com", "401"),
    ("/admin/comments", b"id=cm_1&action=delete", "303"),
    ("/admin/nodes", b"id=x&action=delete", "303"),
    ("/admin/settings", b"comment_enabled=1", "303"),
]


def probe(host, port, path, body, wait_ms):
    """发一个 POST，返回 (状态行或 None, 备注)。"""
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(8)
    try:
        s.connect((host, port))
    except OSError as e:
        return None, "connect失败: %s" % e

    head = (
        "POST %s HTTP/1.1\r\n"
        "Host: %s:%d\r\n"
        "Content-Type: application/x-www-form-urlencoded\r\n"
        "Content-Length: %d\r\n"
        "Connection: close\r\n"
        "\r\n" % (path, host, port, len(body))
    )
    try:
        s.sendall(head.encode() + body)
    except OSError as e:
        return None, "send失败: %s" % e

    # 半关闭：我们不再发数据，但仍然能读。
    # 这一步很关键 —— 真实浏览器/http 库也会在发完body 后
    # 立刻进入读状态，而不是傻等对端。
    try:
        s.shutdown(socket.SHUT_WR)
    except OSError:
        pass

    if wait_ms:
        time.sleep(wait_ms / 1000.0)

    buf = b""
    while b"\r\n\r\n" not in buf:
        try:
            chunk = s.recv(4096)
        except ConnectionResetError:
            return None, "读响应时被RST（ConnectionResetError）"
        except socket.timeout:
            return None, "读响应超时"
        except OSError as e:
            return None, "读响应失败: %s" % e
        if not chunk:
            if not buf:
                return None, "对端直接关闭且没有任何数据（收到 RST）"
            break
        buf += chunk

    line = buf.split(b"\r\n", 1)[0].decode("latin-1").strip()
    return line, "读到 %d 字节" % len(buf)


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return 2
    base = sys.argv[1].rstrip("/")
    hp = base.split("//", 1)[1]
    host, _, port_s = hp.partition(":")
    port = int(port_s or 80)

    # 等待两种时序：
    #   0ms  —— 立刻读。若响应已进本地缓冲区则正常。
    #   250ms —— 等对端关闭动作完成再读。若对端发了 RST，
    #            这个时序才稳定暴露出来。
    fails = 0
    for wait_ms in (0, 250):
        print("=== 读响应前等待 %dms ===" % wait_ms)
        for path, body, want in CASES:
            line, note = probe(host, port, path, body, wait_ms)
            if line is None:
                print("  FAIL %-26s 拿不到响应：%s" % (path, note))
                fails += 1
                continue
            if (" " + want + " ") not in line:
                print("  FAIL %-26s 状态行=%r，应含 %s" % (path, line, want))
                fails += 1
                continue
            print("  ok   %-26s %s（%s）" % (path, line, note))
        print()

    if fails:
        print("结果：%d 项失败 —— 存在 RST 吞响应的问题" % fails)
        return 1
    print("结果：全部通过，响应均可被客户端读到")
    return 0


if __name__ == "__main__":
    sys.exit(main())
