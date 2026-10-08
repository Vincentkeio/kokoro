#!/usr/bin/env python
"""用 CDP 打开一个页面，抓控制台异常并截图。

为什么非要用 CDP：无头浏览器的 `--screenshot` 是一次性抓拍，既看不到
JS 报错，也控制不好"等 WebGL 初始化完再拍"的时机。地球是 WebGL 渲染的，
拍早了就是一片空白——很容易误判成"功能坏了"。

⚠️ 两个坑：
  1. 连本机 CDP 端口必须绕过代理（http_proxy 会把它劫持成 502）；
  2. 等某个 id 的回包时**必须带超时**，否则事件流一直来、目标回包不来，
     `while True: recv()` 就永远卡住（第一版就是这么挂的）。

用法：
    python scripts/cdp-shot.py <URL> <输出图片> [等待毫秒]
"""
import base64
import json
import os
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request

EDGE = r"C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe"


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def wait_cdp(port, timeout=25):
    op = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    end = time.time() + timeout
    while time.time() < end:
        try:
            raw = op.open("http://127.0.0.1:%d/json/version" % port, timeout=2).read()
            return json.loads(raw)
        except Exception:
            time.sleep(0.3)
    raise SystemExit("CDP 端口没起来")


class Session:
    def __init__(self, ws, session):
        self.ws = ws
        self.session = session
        self.mid = 0
        self.events = []
        self.ws.settimeout(0.5)

    def send(self, method, params=None, wait=True, timeout=25):
        self.mid += 1
        myid = self.mid
        msg = {"id": myid, "method": method, "params": params or {}}
        if self.session:
            msg["sessionId"] = self.session
        self.ws.send(json.dumps(msg))
        if not wait:
            return None
        end = time.time() + timeout
        while time.time() < end:
            try:
                m = json.loads(self.ws.recv())
            except Exception:
                continue
            if m.get("id") == myid:
                if "error" in m:
                    raise RuntimeError("%s: %s" % (method, m["error"]))
                return m.get("result", {})
            if "method" in m:
                self.events.append(m)
        raise TimeoutError("等 %s 的回包超时" % method)

    def pump(self, seconds):
        """在指定时间内持续收事件。"""
        end = time.time() + seconds
        while time.time() < end:
            try:
                m = json.loads(self.ws.recv())
            except Exception:
                continue
            if "method" in m:
                self.events.append(m)


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        return 2
    url, out = sys.argv[1], sys.argv[2]
    wait_ms = int(sys.argv[3]) if len(sys.argv) > 3 else 6000
    # 第 4 个参数：模拟的 devicePixelRatio。
    # 本机无头默认是 1，而 boss 的 Windows 常是 1.25/1.5 ——
    # 差的就是这个，好多"高分屏才有"的 bug 在默认视口下根本复现不出来。
    dpr = float(sys.argv[4]) if len(sys.argv) > 4 else 1.0

    import websocket  # websocket-client

    port = free_port()
    profile = tempfile.mkdtemp(prefix="cdp-")
    proc = subprocess.Popen([
        EDGE, "--headless=new", "--no-sandbox", "--hide-scrollbars",
        "--enable-unsafe-swiftshader", "--disable-dev-shm-usage",
        "--remote-debugging-port=%d" % port, "--user-data-dir=" + profile,
        "--window-size=1400,1500", "about:blank",
    ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    try:
        ver = wait_cdp(port)
        ws = websocket.create_connection(ver["webSocketDebuggerUrl"],
                                         suppress_origin=True, timeout=30)
        print("· 已连上 CDP", flush=True)
        s = Session(ws, None)
        targets = s.send("Target.getTargets")
        page = [t for t in targets["targetInfos"] if t["type"] == "page"][0]
        att = s.send("Target.attachToTarget",
                     {"targetId": page["targetId"], "flatten": True})
        s.session = att["sessionId"]
        print("· 已附着到页面", flush=True)

        for meth in ("Page.enable", "Runtime.enable", "Log.enable", "Network.enable"):
            s.send(meth)
        # ⚠️ 只给 --window-size 是不够的：无头模式下它常常不生效，
        # 视口会退成 ~410x288，截图只有左上角一小块，地球也被挤成一小坨。
        # 必须再显式设一次设备度量。两者都要。
        w, h = 1400, 1500
        s.send("Emulation.setDeviceMetricsOverride",
               {"width": w, "height": h, "deviceScaleFactor": dpr, "mobile": False})
        s.send("Page.navigate", {"url": url}, wait=False)
        print("· 开始导航", flush=True)

        # 等页面彻底安静下来：先给固定等待，再按"还有没有网络请求在飞"多等一会儿
        s.pump(wait_ms / 1000.0)
        print("· 等待结束，开始自检", flush=True)

        js = """(function(){
  var g = document.getElementById('globe');
  var o = {host: !!g};
  if (g) {
    o.canvases = g.querySelectorAll('canvas').length;
    o.broken = g.classList.contains('kglobe-broken');
    o.err = g.getAttribute('data-error') || '';
    var cv = g.querySelector('canvas.kglobe-gl');
    o.gl = cv ? cv.width + 'x' + cv.height : 'none';
    o.instance = !!window.kokoroGlobe;
    if (window.kokoroGlobe) {
      o.mask = window.kokoroGlobe.mask ? 'ok' : 'missing';
      o.dots = window.kokoroGlobe.dots ? Math.round(window.kokoroGlobe.dots.length/3) : 0;
      o.places = (window.kokoroGlobe.places||[]).length;
      o.hasHub = !!(window.kokoroGlobe.hub);
    }
  }
  var d = document.getElementById('globe-data');
  o.dataLen = d ? d.textContent.length : 0;
  // 数一数页面上到底有几个地球容器 / 几块画布
  o.globeEls = document.querySelectorAll('.globe').length;
  o.globeIds = document.querySelectorAll('#globe').length;
  o.allCanvas = document.querySelectorAll('canvas').length;
  o.kglobeEls = document.querySelectorAll('.kglobe').length;
  var glc = document.querySelectorAll('canvas.kglobe-gl').length;
  var ovc = document.querySelectorAll('canvas.kglobe-ov').length;
  o.glCanvasCount = glc; o.ovCanvasCount = ovc;
  // 每块画布的位置和尺寸，重复渲染的话这里会看出来
  o.boxes = [];
  var cvs = document.querySelectorAll('canvas');
  for (var i = 0; i < cvs.length; i++) {
    var r = cvs[i].getBoundingClientRect();
    o.boxes.push(Math.round(r.left) + ',' + Math.round(r.top) + ' ' +
                 Math.round(r.width) + 'x' + Math.round(r.height) +
                 (cvs[i].style.display === 'none' ? ' hidden' : ''));
  }
  o.dpr = window.devicePixelRatio || 1;
  if (window.kokoroGlobe) {
    var gg = window.kokoroGlobe;
    o.R = +gg.R.toFixed(1);
    o.cx = +gg.cx.toFixed(1);
    o.cy = +gg.cy.toFixed(1);
    o.yaw = +gg.yaw.toFixed(4);
    o.pitch = +gg.pitch.toFixed(4);
    o.canvasCSS = gg.ovCanvas ? (gg.ovCanvas.style.width + 'x' + gg.ovCanvas.style.height) : 'n/a';
    o.canvasBacking = gg.ovCanvas ? (gg.ovCanvas.width + 'x' + gg.ovCanvas.height) : 'n/a';
    // 每个节点标记相对球心的半径 / R。>1 就是跑到球外了。
    o.markerRatios = [];
    for (var mi = 0; mi < gg.places.length; mi++) {
      var pp = gg.places[mi];
      var sp = gg._project(pp.lat, pp.lon);
      if (sp.z > 0) {
        o.markerRatios.push(+(Math.hypot(sp.x - gg.cx, sp.y - gg.cy) / gg.R).toFixed(3));
      }
    }
  }
  // 两栏是否等高（对齐）
  var band = document.querySelector('.home-band');
  if (band) {
    o.bandCards = [];
    for (var bi = 0; bi < band.children.length; bi++) {
      var br = band.children[bi].getBoundingClientRect();
      o.bandCards.push('top=' + Math.round(br.top) + ' bottom=' + Math.round(br.bottom) +
                       ' h=' + Math.round(br.height));
    }
  }

  // 滚轮缩放自测：模拟一次放大、一次缩小，看半径有没有变
  if (window.kokoroGlobe && window.kokoroGlobe._onWheel) {
    var gz = window.kokoroGlobe;
    var noop = function () {};
    o.zoomBase = +gz.R.toFixed(1);
    gz._onWheel({ deltaY: -120, preventDefault: noop });
    o.zoomIn = +gz.R.toFixed(1);
    gz._onWheel({ deltaY: 120, preventDefault: noop });
    o.zoomBack = +gz.R.toFixed(1);
  }

  o.webgl = (function(){ try { var c=document.createElement('canvas');
    return !!(c.getContext('webgl')||c.getContext('experimental-webgl')); } catch(e){ return false; } })();

  return JSON.stringify(o);
})()"""
        r = s.send("Runtime.evaluate", {"expression": js, "returnByValue": True})
        print("页面自检:", r["result"]["value"])

        shot = s.send("Page.captureScreenshot", {"format": "png"})
        with open(out, "wb") as fp:
            fp.write(base64.b64decode(shot["data"]))
        print("截图:", out, os.path.getsize(out), "字节")
    finally:
        proc.kill()
        try:
            proc.wait(timeout=10)
        except Exception:
            pass
        shutil.rmtree(profile, ignore_errors=True)

    errs, logs, fails = [], [], []
    for m in s.events:
        p = m.get("params", {})
        if m["method"] == "Runtime.exceptionThrown":
            d = p.get("exceptionDetails", {})
            errs.append((d.get("exception", {}).get("description") or d.get("text") or "")[:400])
        elif m["method"] == "Runtime.consoleAPICalled":
            txt = " ".join(str(a.get("value", a.get("description", "")))
                           for a in p.get("args", []))
            logs.append("%s: %s" % (p.get("type"), txt[:300]))
        elif m["method"] == "Log.entryAdded":
            e = p.get("entry", {})
            if e.get("level") in ("error", "warning"):
                logs.append("%s: %s" % (e.get("level"), e.get("text", "")[:300]))
        elif m["method"] == "Network.loadingFailed":
            fails.append("%s %s" % (p.get("type"), p.get("errorText")))

    for title, items in (("未捕获异常", errs),
                         ("控制台 error", [x for x in logs if x.startswith("error")]),
                         ("控制台 warn", [x for x in logs if x.startswith("warning")]),
                         ("网络失败", fails)):
        if items:
            print("\n%s:" % title)
            for x in items[:8]:
                print("  !", x)
    print("\n事件总数 %d" % len(s.events))
    return 0


if __name__ == "__main__":
    sys.exit(main())
