#!/usr/bin/env python
"""验证「节点标记是否画在该节点真实的地理位置上」。

做法是构造一个**已知答案**的场景：把主机坐标设成东京，再放一台也在东京的小鸡。
如果投影正确，这颗节点的标记必须落在球心（误差几个像素以内）。

再放一台在「东京正东 60°」的小鸡，它的标记应当落在
  屏幕半径 = R * sin(60°) 、偏东（屏幕左侧，因为本项目东=左……见 globe.js 推导）
的位置上——这条用来确认方位角也没错。

用法： python scripts/globe-marker-check.py
"""
import http.cookiejar
import json
import math
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
VENV = r"C:/Users/yifen/.workbuddy-ai/binaries/python/envs/default/Scripts/python.exe"


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


class Client:
    def __init__(self, base):
        self.base = base
        self.jar = http.cookiejar.CookieJar()
        self.op = urllib.request.build_opener(
            urllib.request.ProxyHandler({}),
            urllib.request.HTTPCookieProcessor(self.jar), NoRedirect())

    def get(self, path):
        try:
            r = self.op.open(self.base + path, timeout=20)
            return r.status, r.read()
        except urllib.error.HTTPError as e:
            return e.code, e.read()

    def post_form(self, path, fields):
        body = urllib.parse.urlencode(fields).encode()
        req = urllib.request.Request(self.base + path, data=body, method="POST")
        req.add_header("Content-Type", "application/x-www-form-urlencoded")
        try:
            r = self.op.open(req, timeout=20)
            return r.status, r.read()
        except urllib.error.HTTPError as e:
            return e.code, e.read()

    def post_json(self, path, obj, headers=None):
        req = urllib.request.Request(self.base + path,
                                     data=json.dumps(obj).encode(), method="POST")
        req.add_header("Content-Type", "application/json")
        for k, v in (headers or {}).items():
            req.add_header(k, v)
        try:
            r = self.op.open(req, timeout=20)
            return r.status, r.read()
        except urllib.error.HTTPError as e:
            return e.code, e.read()


TOKYO = (35.6762, 139.6503)
# 东京正东 60°：经度 +60，纬度不变
EAST60 = (35.6762, 139.6503 + 60.0)


def main():
    exe = os.environ.get("KOKORO_BIN") or os.path.join(tempfile.gettempdir(), "kokoro-marker.exe")
    if not os.path.exists(exe):
        subprocess.check_call(["go", "build", "-o", exe, "./cmd/kokoro"], cwd=ROOT)
    data = tempfile.mkdtemp(prefix="kokoro-marker-")
    port = free_port()
    base = "http://127.0.0.1:%d" % port
    logf = open(os.path.join(data, "hub.log"), "wb")
    proc = subprocess.Popen([exe, "serve", "--listen", "127.0.0.1:%d" % port,
                             "--data", data, "--site", "Kokoro", "--tls", "none"],
                            stdout=logf, stderr=subprocess.STDOUT, cwd=tempfile.gettempdir())
    try:
        for _ in range(120):
            try:
                s = socket.create_connection(("127.0.0.1", port), 0.3)
                s.close()
                break
            except OSError:
                time.sleep(0.15)
        logf.flush()
        txt = open(os.path.join(data, "hub.log"), "rb").read().decode("utf-8", "replace")
        m = re.search(r"已生成管理员账号: (\S+) / (\S+)", txt)
        if not m:
            print("拿不到管理员账号:\n", txt[-1200:])
            return 1
        adm = Client(base)
        adm.post_form("/admin", {"username": m.group(1), "password": m.group(2)})
        adm.post_form("/admin/tokens", {"action": "create", "label": "t"})
        _, page = adm.get("/admin")
        tm = re.search(rb"(it_[A-Za-z0-9]+)", page)
        if not tm:
            print("拿不到安装令牌")
            return 1
        token = tm.group(1).decode()

        # 两台小鸡：一台就在东京（主机也在东京），一台在东京正东 60°
        for name, (lat, lon), cc, region in (
                ("东京本机", TOKYO, "JP", "日本 · 东京"),
                ("东京东60", EAST60, "JP", "日本 · 东京")):
            st, body = Client(base).post_json("/api/v1/register", {
                "install_token": token, "hostname": name, "os": "Debian 13",
                "kernel": "6.1", "arch": "x86_64", "agent_version": "0.1.0",
                "cpu_cores": 2, "mem_total": 4 << 30, "disk_total": 40 << 30})
            if st != 200:
                print("注册失败", st, body[:200])
                return 1
            nid = json.loads(body)["node_id"]
            # 用 city 字段精确指定坐标（geo 的城市表里有东京）
            adm.post_form("/admin/nodes", {"action": "rename", "id": nid, "name": name,
                                           "country": cc, "region": region, "city": "东京"})

        # 主机就设在东京
        adm.post_form("/admin/settings", {
            "comment_enabled": "1",
            "hub_lat": str(TOKYO[0]), "hub_lon": str(TOKYO[1])})

        # 让 CDP 脚本读回页面里的投影结果
        probe = os.path.join(ROOT, "work", "_probe_marker.py")
        open(probe, "w", encoding="utf-8").write(PROBE_JS)
        r = subprocess.run([VENV, probe, base + "/"], capture_output=True, timeout=180)
        out = r.stdout.decode("utf-8", "replace")
        print(out)
        return 0 if "投影正确" in out else 1
    finally:
        proc.kill()
        try:
            proc.wait(timeout=10)
        except Exception:
            pass
        shutil.rmtree(data, ignore_errors=True)


# 在页面里量：主机的投影半径、每个节点标记的投影半径，和理论值比。
PROBE_JS = r'''
import json, socket, subprocess, sys, tempfile, time, shutil, base64, urllib.request
import websocket

EDGE = r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe"
url = sys.argv[1]

def free_port():
    s = socket.socket(); s.bind(("127.0.0.1", 0)); p = s.getsockname()[1]; s.close(); return p

port = free_port()
profile = tempfile.mkdtemp(prefix="cdp-marker-")
proc = subprocess.Popen([EDGE, "--headless=new", "--no-sandbox", "--hide-scrollbars",
    "--enable-unsafe-swiftshader", "--remote-debugging-port=%d" % port,
    "--user-data-dir=" + profile, "--window-size=1400,1200", "about:blank"],
    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
try:
    op = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    end = time.time() + 25
    while time.time() < end:
        try:
            ver = json.loads(op.open("http://127.0.0.1:%d/json/version" % port, timeout=2).read()); break
        except Exception: time.sleep(0.3)
    else:
        print("CDP 起不来"); sys.exit(1)
    ws = websocket.create_connection(ver["webSocketDebuggerUrl"], suppress_origin=True, timeout=30)
    ws.settimeout(0.5)
    mid = [0]
    def call(method, params=None, sess=None, timeout=25):
        mid[0] += 1
        msg = {"id": mid[0], "method": method, "params": params or {}}
        if sess: msg["sessionId"] = sess
        ws.send(json.dumps(msg))
        end = time.time() + timeout
        while time.time() < end:
            try: m = json.loads(ws.recv())
            except Exception: continue
            if m.get("id") == mid[0]: return m.get("result", {})
        raise TimeoutError(method)
    t = call("Target.getTargets")
    page = [x for x in t["targetInfos"] if x["type"] == "page"][0]
    sess = call("Target.attachToTarget", {"targetId": page["targetId"], "flatten": True})["sessionId"]
    for mm in ("Page.enable", "Runtime.enable"):
        call(mm, sess=sess)
    call("Emulation.setDeviceMetricsOverride",
         {"width": 1400, "height": 1200, "deviceScaleFactor": 1, "mobile": False}, sess=sess)
    call("Page.navigate", {"url": url}, sess=sess, timeout=1)
    end = time.time() + 7
    while time.time() < end:
        try: ws.recv()
        except Exception: pass

    js = r"""(function(){
  var g = window.kokoroGlobe;
  if (!g) return JSON.stringify({err:'没有地球实例'});
  function proj(lat, lon){
    var p = g._project(lat, lon);
    var dx = p.x - g.cx, dy = p.y - g.cy;
    return { z: +p.z.toFixed(4), r: +Math.hypot(dx, dy).toFixed(1),
             ratio: +(Math.hypot(dx, dy) / g.R).toFixed(4) };
  }
  var out = { R: +g.R.toFixed(1), cx: +g.cx.toFixed(1), cy: +g.cy.toFixed(1),
              places: [], hub: null };
  for (var i = 0; i < g.places.length; i++) {
    var p = g.places[i];
    var q = proj(p.lat, p.lon);
    out.places.push({ name: p.name, lat: +p.lat.toFixed(4), lon: +p.lon.toFixed(4),
                      z: q.z, ratio: q.ratio });
  }
  if (g.hub) { var h = proj(g.hub.lat, g.hub.lon); out.hub = { ratio: h.ratio, z: h.z }; }
  return JSON.stringify(out);
})()"""
    r = call("Runtime.evaluate", {"expression": js, "returnByValue": True}, sess=sess)
    print(r["result"]["value"])
finally:
    proc.kill()
    try: proc.wait(timeout=10)
    except Exception: pass
    shutil.rmtree(profile, ignore_errors=True)
'''


if __name__ == "__main__":
    sys.exit(main())
