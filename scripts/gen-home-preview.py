#!/usr/bin/env python
"""生成「信息更丰富的首页」本地预览页。

为什么用脚本生成而不是手写 HTML：地图的 SVG path 有 110KB，
手写不现实；而且这个脚本本身就是**生产实现的蓝本**——
预览定稿后，同样的布局会搬进 `internal/hub/templates/home.html`，
path 数据改成 go:embed。

用法：
    python scripts/gen-home-preview.py
输出：
    work/preview/home-rich.html
"""
import io
import json
import math
import re
import os
import random
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GEO = os.path.join(ROOT, "work", "geo", "out")
OUT_DIR = os.path.join(ROOT, "work", "preview")
OUT = os.path.join(OUT_DIR, "home-rich.html")

# 与 gen-geo-svg.py 保持一致
LON_MIN, LON_MAX = -180.0, 180.0
LAT_MAX, LAT_MIN = 84.0, -58.0
VB_W = 1000.0
VB_H = VB_W * (LAT_MAX - LAT_MIN) / (LON_MAX - LON_MIN)


def project(lon, lat):
    return ((lon - LON_MIN) / (LON_MAX - LON_MIN) * VB_W,
            (LAT_MAX - lat) / (LAT_MAX - LAT_MIN) * VB_H)


def gc_segments(lat1, lon1, lat2, lon2, n=72):
    """两点之间的**大圆航线**，投影后返回若干段 path 字符串。

    为什么不用直线：地图是等距圆柱投影，直连会得到一条在球面上并不存在的
    路径。更明显的是跨太平洋——东京到洛杉矶直线画出来会横穿整个欧亚大陆，
    一眼就假。大圆航线才是真实的走法。

    为什么要分段：跨 180° 经线时投影后的 x 会从一端跳到另一端。
    必须在这里切开，否则地图上会出现一条横穿整张图的假线。
    """
    # 经度差取「近的那一边」
    dlon = lon2 - lon1
    if dlon > 180.0:
        dlon -= 360.0
    elif dlon < -180.0:
        dlon += 360.0

    p1 = (math.radians(lat1), math.radians(lon1))
    p2 = (math.radians(lat2), math.radians(lon1 + dlon))

    # 球面两点夹角
    cosd = (math.sin(p1[0]) * math.sin(p2[0]) +
            math.cos(p1[0]) * math.cos(p2[0]) * math.cos(p2[1] - p1[1]))
    cosd = max(-1.0, min(1.0, cosd))
    d = math.acos(cosd)

    raw = []
    for i in range(n + 1):
        t = i / float(n)
        if d < 1e-9:
            lat, lon = p1[0], p1[1]
        else:
            a = math.sin((1 - t) * d) / math.sin(d)
            b = math.sin(t * d) / math.sin(d)
            x = a * math.cos(p1[0]) * math.cos(p1[1]) + b * math.cos(p2[0]) * math.cos(p2[1])
            y = a * math.cos(p1[0]) * math.sin(p1[1]) + b * math.cos(p2[0]) * math.sin(p2[1])
            z = a * math.sin(p1[0]) + b * math.sin(p2[0])
            lat = math.atan2(z, math.hypot(x, y))
            lon = math.atan2(y, x)
        lon_deg = math.degrees(lon)
        while lon_deg > 180.0:
            lon_deg -= 360.0
        while lon_deg < -180.0:
            lon_deg += 360.0
        raw.append(project(lon_deg, math.degrees(lat)))

    # 投影后的 x 跳变超过半张图 = 跨了经线，切开
    segs, cur, prev = [], [], None
    for x, y in raw:
        if prev is not None and abs(x - prev) > VB_W * 0.5:
            if len(cur) >= 2:
                segs.append(cur)
            cur = []
        cur.append((x, y))
        prev = x
    if len(cur) >= 2:
        segs.append(cur)

    out = []
    for s in segs:
        d_str = "M" + " L".join("%.1f %.1f" % p for p in s)
        length = sum(math.hypot(s[i + 1][0] - s[i][0], s[i + 1][1] - s[i][1])
                     for i in range(len(s) - 1))
        out.append((d_str, length, s))
    return out


FLAG_DIR = os.path.join(ROOT, "internal", "flags", "svg")


def flag_svg(cc):
    """内联项目自带的 SVG 国旗。

    不能用 emoji 国旗（🇯🇵）：Windows 没有国旗字形，会退化成 "jp" 字母对。
    项目已经有 235 面 SVG，直接用。台湾用的是中华台北奥委会旗（合规要求）。
    """
    path = os.path.join(FLAG_DIR, cc.lower() + ".svg")
    if not os.path.exists(path):
        return ""
    with io.open(path, encoding="utf-8-sig") as fp:
        raw = fp.read()
    raw = re.sub(r"<!--.*?-->", "", raw, flags=re.S)   # 去掉来源注释
    raw = re.sub(r"<\?xml.*?\?>", "", raw, flags=re.S)
    raw = re.sub(r">\s+<", "><", raw).strip()
    # ⚠️ 项目自带的国旗是 flag-icons 的 **1:1 方形** 版本：
    # 日本旗的白色底填满整个方块，直接显示就是一个"白色方块 + 红点"，
    # 看着像带了个白底框。这里注入 preserveAspectRatio="slice"
    # （等价于 CSS 的 object-fit:cover），按真实国旗比例 3:2 裁切，
    # 溢出的部分被裁掉，剩下的才像一面旗。
    if "preserveAspectRatio" not in raw:
        raw = raw.replace("<svg ", '<svg preserveAspectRatio="xMidYMid slice" ', 1)
    return '<span class="flag" title="%s">%s</span>' % (cc.upper(), raw)


# 示例节点。真实实现里经纬度要靠「国家/城市 → 坐标」查表补上，
# 因为 model.Node 目前只存 Country / City / Region，没有坐标。
NODES = [
    {"name": "东京 zouter", "cores": 2, "ram": "4G", "diskGB": 40, "os": "Debian 13", "load": 0.02, "pct": 3, "cc": "jp", "region": "日本 · 东京", "tags": "香港, CN2, 原生IP, 支持退款", "lat": 35.68, "lon": 139.69,
     "online": True, "cpu": 0.5, "mem": 36, "disk": 21, "up": "464 B/s", "down": "988 B/s",
     "uptime": "运行 11天8小时", "latency": 42, "current": True},
    {"name": "洛杉矶 dmit", "cores": 4, "ram": "8G", "diskGB": 80, "os": "Ubuntu 24.04", "load": 0.84, "pct": 46, "cc": "us", "region": "美国 · 洛杉矶", "tags": "美国, 大带宽, 高防", "lat": 34.05, "lon": -118.24,
     "online": True, "cpu": 12.4, "mem": 58, "disk": 44, "up": "1.2 MB/s", "down": "3.4 MB/s",
     "uptime": "运行 32天2小时", "latency": 168},
    {"name": "香港 hkbn", "cores": 2, "ram": "2G", "diskGB": 20, "os": "Alpine 3.20", "load": 0.11, "pct": 22, "cc": "hk", "region": "中国香港", "tags": "香港, CN2 GIA, 低延迟", "lat": 22.32, "lon": 114.17,
     "online": True, "cpu": 3.1, "mem": 22, "disk": 15, "up": "220 KB/s", "down": "1.1 MB/s",
     "uptime": "运行 88天", "latency": 64},
    {"name": "台北 hinet", "cores": 2, "ram": "4G", "diskGB": 60, "os": "Debian 12", "load": 0.35, "pct": 35, "cc": "tw", "region": "中国台湾 · 台北", "tags": "中国台湾, 家宽", "lat": 25.03, "lon": 121.57,
     "online": True, "cpu": 8.7, "mem": 41, "disk": 37, "up": "88 KB/s", "down": "540 KB/s",
     "uptime": "运行 5天19小时", "latency": 32},
    {"name": "新加坡 vultr", "cores": 1, "ram": "1G", "diskGB": 25, "os": "CentOS 9", "load": 0.0, "pct": 0, "cc": "sg", "region": "新加坡", "tags": "新加坡, 按小时计费", "lat": 1.35, "lon": 103.82,
     "online": False, "cpu": 0, "mem": 0, "disk": 61, "up": "0 B/s", "down": "0 B/s",
     "uptime": "已离线 2小时", "latency": 0},
    {"name": "法兰克福 hetzner", "cores": 8, "ram": "32G", "diskGB": 512, "os": "Debian 13", "load": 1.92, "pct": 73, "cc": "de", "region": "德国 · 法兰克福", "tags": "德国, 独服, 大盘鸡", "lat": 50.11, "lon": 8.68,
     "online": True, "cpu": 26.8, "mem": 73, "disk": 52, "up": "2.8 MB/s", "down": "6.1 MB/s",
     "uptime": "运行 61天4小时", "latency": 221},
]

# 时钟展示的时区（本地时间由浏览器提供，不写死）
CLOCKS = [
    ("本地", None),
    ("UTC", "UTC"),
    ("东京", "Asia/Tokyo"),
    ("洛杉矶", "America/Los_Angeles"),
    ("法兰克福", "Europe/Berlin"),
]


def map_layers():
    """生成地图上的三层：传输线底、流动动效、节点标记。

    传输线一律**指向 Hub**（这里是东京）。方向靠动画体现：
    虚线朝 Hub 方向滚动 = 数据从子机流向主机。
    """
    hub = None
    for n in NODES:
        if n.get("current"):
            hub = n
            break
    if hub is None:
        hub = NODES[0]

    base, flow, dots, motion = [], [], [], []
    hx, hy = project(hub["lon"], hub["lat"])
    for i, n in enumerate(NODES):
        x, y = project(n["lon"], n["lat"])
        cls = "on" if n["online"] else "off"
        cur = " cur" if n.get("current") else ""
        label = n["name"] + ("" if n["online"] else "（离线）")
        dots.append(
            '<g class="pin %s%s" transform="translate(%.1f %.1f)" data-name="%s">'
            '<circle class="halo" r="20"/><circle class="dot" r="6.2"/>'
            '<title>%s · %s</title></g>'
            % (cls, cur, x, y, label, label, n["region"]))

        # 当前节点自己不用连线
        if n.get("current"):
            continue
        # ⚠️ 参数顺序是「起点 → 终点」，必须写成 节点 → 主机。
        # 写反了不只是图中的方向反了：数据包（animateMotion 沿 path 走）
        # 会全部堆到主机那一端，虚线也会朝远离主机方向滚。
        segs = gc_segments(n["lat"], n["lon"], hub["lat"], hub["lon"])
        for j, (d, length, pts) in enumerate(segs):
            pid = "flow-%d-%d" % (i, j)
            state = "on" if n["online"] else "off"
            # 离线节点不画流动，只留一条灰线，一眼看出"这条不通"
            base.append('<path class="tline %s" id="%s" d="%s"/>'
                        % (state, pid, d))
            if n["online"]:
                # 虚线滚动：dasharray 的 2/3 是"亮段"长度，
                # keyframes 把 dashoffset 推一个周期，看起来就是往 Hub 跑。
                flow.append(
                    '<use class="tflow" href="#%s" '
                    'style="--dur:%.2fs"/>' % (pid, 1.6 + (length / 620.0) * 2.2))
                # 再叠一颗顺流而下的"数据包"，比纯虚线更有传输感。
                #
                # cx/cy 必须显式写成航段起点：animateMotion 是"相对位移"，
                # 不写的话圆点在动画开始前会停在 SVG 原点 (0,0)，
                # 在地图左上角留下一个孤立的小点（截图里能看到）。
                if j == 0:
                    sx, sy = pts[0]
                    motion.append(
                        '<circle class="packet" cx="%.1f" cy="%.1f" r="3.1">'
                        '<animateMotion dur="%.2fs" repeatCount="indefinite" '
                        'begin="%.2fs"><mpath href="#%s"/></animateMotion>'
                        '</circle>' % (sx, sy,
                                       2.6 + (length / 620.0) * 2.4,
                                       (i * 0.7) % 3.0, pid))

    # 主机的"雷达波"：三圈错开的同心圆从中心扩散出去，
    # 让"所有数据都汇到这里"这件事一眼看出来。
    radar = []
    for k in range(3):
        radar.append(
            '<circle class="ring" r="6" fill="none">'
            '<animate attributeName="r" values="6;62" dur="3.6s" '
            'begin="%.2fs" repeatCount="indefinite"/>'
            '<animate attributeName="opacity" values=".55;0" dur="3.6s" '
            'begin="%.2fs" repeatCount="indefinite"/>'
            '</circle>' % (k * 1.2, k * 1.2))
    radar = '<g class="radar" transform="translate(%.1f %.1f)">%s</g>' % (hx, hy, "".join(radar))

    return "".join(base), "".join(flow), "".join(motion), "".join(dots), radar



def clock_html():
    cells = []
    for label, tz in CLOCKS:
        attr = ' data-tz="%s"' % tz if tz else ""
        cells.append(
            '<div class="clock"%s><span class="tz">%s</span>'
            '<b class="t">--:--:--</b><span class="d">----</span></div>' % (attr, label))
    return "".join(cells)



def places_json():
    """喂给地球的节点列表。真站点由 Go 模板输出同样的结构。"""
    out = []
    for n in NODES:
        out.append({"key": n["name"], "name": n["name"].split(" ")[0],
                    "sub": n["region"], "lat": n["lat"], "lon": n["lon"],
                    "online": bool(n["online"])})
    return json.dumps(out, ensure_ascii=False)


def tag_html(raw):
    """把 "a, b, c" 渲染成小标签。
    真站点由后台 parseTags() 保证格式，预览这边直接切一下。"""
    items = [t.strip() for t in re.split(r"[,，、;；\s]+", raw or "") if t.strip()]
    if not items:
        return ""
    return '<div class="tags">%s</div>' % "".join(
        '<span class="tag">%s</span>' % t for t in items)


def tag_inline(raw):
    """行内版本（表格/紧凑视图用），不带外层 div。"""
    items = [t.strip() for t in re.split(r"[,，、;；\s]+", raw or "") if t.strip()]
    return " ".join('<span class="tag">%s</span>' % t for t in items)


def spark(seed, n=44, w=260.0, h=40.0):
    """按节点名生成一条确定性的流量序列 + SVG path。

    预览不需要真实历史数据，但每次生成的形状必须一样（否则每次刷新
    卡片都在变，看着像 bug）。所以用节点名做种子，不用真随机。
    """
    rnd = random.Random(seed)
    vals, v = [], 0.34
    for _ in range(n):
        v += rnd.uniform(-0.13, 0.13)
        v = max(0.05, min(0.97, v))
        vals.append(v)
    step = w / (n - 1)
    pts = [(i * step, h - vals[i] * h) for i in range(n)]
    line = "M" + " L".join("%.1f %.1f" % pt for pt in pts)
    area = line + " L%.1f %.1f L0 %.1f Z" % (w, h, h)
    return line, area


def level(pct):
    """按占用率给条子上色档位。和真站点的 latClass 一个思路：
    颜色表达状态，不要让人去算数字。"""
    if pct >= 85:
        return "bad"
    if pct >= 65:
        return "warn"
    return "ok"


def node_cards():
    cards = []
    for n in NODES:
        cls = "on" if n["online"] else "off"
        lat = n["latency"] if n["online"] else 0
        # 延迟也画成条：以 300ms 为满格，一眼看出远近
        latp = min(100.0, lat / 300.0 * 100.0) if n["online"] else 0
        bars = ""
        for key, label in (("cpu", "CPU"), ("mem", "内存"), ("disk", "磁盘")):
            v = n[key]
            bars += ('<div class="g"><span class="gl">%s</span>'
                     '<i class="bar"><u class="lv-%s" style="width:%.1f%%"></u></i>'
                     '<em>%.1f%%</em></div>' % (label, level(v), v, v))
        load = n.get("load", 0.0)
        loadp = min(100.0, load / max(1, n.get("cores", 1)) * 100.0)
        line, area = spark(n["name"])
        cards.append(
            '<article class="card %s">'
              '<div class="card-head">'
                '<h2>%s<a href="#">%s</a></h2>'
                '<span class="status %s"><i></i>%s</span>'
              '</div>'
              '<p class="meta">%s</p>'
              '%s'
              '<div class="gauges">%s</div>'
              '<div class="net">'
                '<svg class="spark" viewBox="0 0 260 40" preserveAspectRatio="none" aria-hidden="true">'
                  '<path class="sp-area" d="%s"/><path class="sp-line" d="%s"/>'
                '</svg>'
                '<div class="net-num">'
                  '<span class="up">↑ %s</span><span class="down">↓ %s</span>'
                  '<span class="lat">延迟 <i class="bar mini"><u class="lv-%s" style="width:%.0f%%"></u></i>%s</span>'
                '</div>'
              '</div>'
              '<dl class="specs">'
                '<div><dt>规格</dt><dd>%dC / %s / %dG</dd></div>'
                '<div><dt>系统</dt><dd>%s</dd></div>'
                '<div><dt>运行</dt><dd>%s</dd></div>'
                '<div><dt>负载</dt><dd>%.2f <i class="bar mini"><u class="lv-%s" style="width:%.0f%%"></u></i></dd></div>'
              '</dl>'
              '<div class="foot"><a class="more" href="#">详情 →</a></div>'
            '</article>'
            % (cls, flag_svg(n["cc"]), n["name"],
               "on" if n["online"] else "off",
               "在线" if n["online"] else "离线",
               n["region"], tag_html(n.get("tags", "")), bars, area, line,
               n["up"], n["down"],
               level(latp), latp, ("%dms" % lat) if n["online"] else "—",
               n.get("cores", 1), n.get("ram", "-"), n.get("diskGB", 0),
               n.get("os", "-"), n["uptime"],
               load, level(loadp), loadp))
    return "".join(cards)


def node_table():
    """表格形态。和卡片形态是同一份数据，只是换个排布——
    这正是"列表形态可切换"要演示的东西。"""
    rows = []
    for n in NODES:
        cls = "on" if n["online"] else "off"
        lat = ("%dms" % n["latency"]) if n["online"] else "不可达"
        rows.append(
            '<tr class="%s"><td class="p">%s<span class="nm">%s</span></td>'
            '<td class="rg">%s</td>'
            '<td class="tg">%s</td>'
            '<td class="num">%.1f%%</td><td class="num">%.1f%%</td><td class="num">%.1f%%</td>'
            '<td class="num">%s</td><td class="num">%s</td><td class="num">%s</td>'
            '<td class="num">%s</td>'
            '<td><span class="dot %s"></span></td>'
            '<td><a class="more" href="#">详情</a></td></tr>'
            % (cls, flag_svg(n["cc"]), n["name"], n["region"],
               tag_inline(n.get("tags", "")),
               n["cpu"], n["mem"], n["disk"], n["up"], n["down"], n["uptime"], lat, cls))
    return "".join(rows)


def node_compact():
    """紧凑形态：一行一个节点，只留最关键的几项。"""
    rows = []
    for n in NODES:
        cls = "on" if n["online"] else "off"
        lat = ("%dms" % n["latency"]) if n["online"] else "—"
        rows.append(
            '<a class="crow %s" href="#"><span class="dot %s"></span>'
            '<span class="p">%s%s</span>'
            '<span class="rg">%s</span>'
            '<span class="tg">%s</span>'
            '<span class="bar"><u style="width:%.0f%%"></u></span>'
            '<span class="num">CPU %.1f%%</span>'
            '<span class="num">内存 %.1f%%</span>'
            '<span class="num net">↑%s ↓%s</span>'
            '<span class="num">%s</span></a>'
            % (cls, cls, flag_svg(n["cc"]), n["name"], n["region"],
               tag_inline(n.get("tags", "")),
               n["cpu"], n["cpu"], n["mem"], n["up"], n["down"], lat))
    return "".join(rows)


def total_stats():
    online = sum(1 for n in NODES if n["online"])
    regions = len({n["region"] for n in NODES})
    return [
        ("节点总数", str(len(NODES)), "台小鸡已接入"),
        ("在线", str(online), "%.0f%% 可用" % (online * 100.0 / len(NODES))),
        ("地区", str(regions), "个地理区域"),
        ("今日流量", "1.8 TB", "↑ 420 GB · ↓ 1.4 TB"),
        ("本月流量", "36.2 TB", "剩余配额 63%"),
        ("探测点", "103", "三网覆盖 31 省"),
    ]



def css_balance(css):
    """返回 CSS 花括号的净值（0 = 配平）。

    ⚠️ 必须先把注释和字符串剥掉再数。第一版直接 count("{")/count("}")，
    结果被自己注释里那句"多出一个 }"里的 } 骗了，报了个假的不配平。
    """
    out = []
    i = 0
    n = len(css)
    while i < n:
        if css.startswith("/*", i):
            j = css.find("*/", i + 2)
            i = n if j < 0 else j + 2
            continue
        ch = css[i]
        if ch in "\"'":
            j = i + 1
            while j < n and css[j] != ch:
                if css[j] == "\\":
                    j += 1
                j += 1
            i = j + 1
            continue
        out.append(ch)
        i += 1
    return "".join(out).count("{") - "".join(out).count("}")


def build_html():
    with io.open(os.path.join(GEO, "world.path"), encoding="utf-8") as fp:
        world_path = fp.read()
    with io.open(os.path.join(GEO, "china.path"), encoding="utf-8") as fp:
        china_path = fp.read()
    # 陆地掩码内联成 base64：本地预览走 file://，fetch 会被 CORS 拦掉
    with io.open(os.path.join(GEO, "land.b64"), encoding="ascii") as fp:
        land_b64 = fp.read()

    stats = "".join(
        '<div class="stat"><span>%s</span><b>%s</b><small>%s</small></div>' % s
        for s in total_stats())

    # 用 replace 而不是 % 格式化：模板里有大量 CSS 的 100%、50% 等，
    # 走 % 格式化会把它们当成占位符直接报错。
    base, flow, motion, pins, radar = map_layers()
    out = TEMPLATE
    for token, val in (
        ("__VW__", "%.0f" % VB_W),
        ("__VH__", "%.1f" % VB_H),
        ("__WORLD__", world_path),
        ("__CHINA__", china_path),
        ("__TLINE__", base),
        ("__TFLOW__", flow),
        ("__TPACKET__", motion),
        ("__PINS__", pins),
        ("__RADAR__", radar),
        ("__LAND__", land_b64),
        ("__PLACES__", places_json()),
        ("__CLOCKS__", clock_html()),
        ("__STATS__", stats),
        ("__CARDS__", node_cards()),
        ("__ROWS__", node_table()),
        ("__COMPACT__", node_compact()),
        ("__N__", str(len(NODES))),
        ("__ONLINE__", str(sum(1 for n in NODES if n["online"]))),
    ):
        out = out.replace(token, val)
    return out


TEMPLATE = u"""<!doctype html>
<html lang="zh-CN" data-k-mode="light">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Kokoro 首页改版预览</title>
<style>
/* 这份预览刻意直接照抄 kokoro.daylight 的变量命名与取值，
   定稿后搬进模板时不用改颜色，只改结构。 */
:root {
  --bg:#ffffff; --bg-soft:#f6f8fa; --panel:#ffffff; --sunken:#f6f8fa;
  --fg:#1f2328; --fg-strong:#1f2328; --muted:#656d76; --faint:#8b949e;
  --line:#d0d7de; --accent:#2f6feb; --accent-fg:#ffffff;
  --ok:#1a7f37; --warn:#9a6700; --bad:#cf222e;
  --radius:10px; --radius-sm:8px; --radius-full:999px;
  --mono:ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,monospace;
  --s1:4px; --s2:8px; --s3:12px; --s4:16px; --s5:24px;
  --gap:16px; --head-h:56px;
  /* 地球点阵的昼夜色。写进 :root 里，不要另起一个块——
     之前就是在 :root 之外又写了一段，多出一个 }，把后面的 CSS 全废掉了。 */
  --globe-day:#c9d4e2; --globe-night:#39435a; --globe-rim:rgba(47,111,235,.18);
}
html[data-k-mode="dark"] {
  --globe-day:#5d6b82; --globe-night:#242c3a; --globe-rim:rgba(59,130,246,.26);
  --bg:#0d1117; --bg-soft:#161b22; --panel:#161b22; --sunken:#1c2230;
  --fg:#e6edf3; --fg-strong:#f0f6fc; --muted:#8b949e; --faint:#6e7681;
  --line:#30363d; --accent:#3b82f6; --accent-fg:#ffffff;
  --ok:#3fb950; --warn:#d29922; --bad:#f85149;
}
* { box-sizing:border-box; }
body {
  margin:0; background:var(--bg); color:var(--fg);
  font:14px/1.6 system-ui,"PingFang SC","Microsoft YaHei",sans-serif;
}
.wrap { max-width:1280px; margin:0 auto; padding:0 20px; }
a { color:var(--accent); }

/* ---- 顶栏 ---- */
.top { border-bottom:1px solid var(--line); background:var(--bg-soft);
       position:sticky; top:0; z-index:10; }
.top .wrap { display:flex; align-items:center; gap:var(--s4); min-height:var(--head-h); }
.brand { font-weight:700; font-size:16px; color:var(--fg-strong); text-decoration:none; }
.search { display:flex; gap:6px; flex:1; max-width:420px; }
.search input { flex:1; padding:6px 10px; border:1px solid var(--line);
  border-radius:var(--radius-sm); background:var(--panel); color:var(--fg); }
.top-tools { display:flex; align-items:center; gap:var(--s2); margin-left:auto; }
.btn { border:1px solid var(--line); background:var(--panel); color:var(--fg);
  border-radius:var(--radius-sm); padding:4px 10px; cursor:pointer;
  font-size:13px; text-decoration:none; }
.btn:hover { border-color:var(--accent); color:var(--accent); }
.skin { border:1px solid var(--line); background:var(--sunken); border-radius:var(--radius-full);
  padding:3px 10px; font-size:13px; color:var(--muted); cursor:pointer; }

/* ---- 预览提示条 ---- */
.banner { background:#fff8c5; border-bottom:1px solid #d4a72c;
  color:#4d2d00; font-size:12.5px; padding:6px 0; }
html[data-k-mode="dark"] .banner { background:#3a2d00; border-color:#9e6a03; color:#f0d38a; }

/* ---- Hero + 时钟 ---- */
.hero { display:flex; align-items:flex-end; justify-content:space-between;
  gap:var(--s5); flex-wrap:wrap; padding:28px 0 20px; }
.hero h1 { margin:0; font-size:28px; font-weight:700; color:var(--fg-strong);
  letter-spacing:-.6px; }
.hero p { margin:7px 0 0; color:var(--muted); font-size:13.5px; }

.clocks { display:flex; gap:2px; flex-wrap:wrap; border:1px solid var(--line);
  border-radius:var(--radius); overflow:hidden; background:var(--line); }
.clock { flex:1 1 140px; background:var(--panel); padding:10px 14px; }
/* 第一格是本地时间，给它一点主色，其余保持安静 */
.clock:first-child { background:color-mix(in srgb, var(--accent) 5%, var(--panel)); }
.clock:first-child .tz { color:var(--accent); }
.clock .tz { display:block; font-size:11px; color:var(--faint); letter-spacing:.4px; }
.clock b { font-family:var(--mono); font-size:17px; color:var(--fg-strong); letter-spacing:.5px; }
.clock .d { display:block; font-size:11px; color:var(--muted); font-family:var(--mono); }

/* ---- 统计卡 ---- */
.stats { display:grid; grid-template-columns:repeat(auto-fit,minmax(150px,1fr));
  gap:var(--gap); margin:18px 0; }
.stat { position:relative; background:var(--panel); border:1px solid var(--line);
  border-radius:var(--radius); padding:13px 15px 12px; overflow:hidden; }
/* 顶部一条主色细条：让一组数字看起来像"一块仪表"，而不是六个白盒子 */
.stat::before { content:''; position:absolute; left:0; right:0; top:0; height:2px;
  background:linear-gradient(90deg, var(--accent), color-mix(in srgb, var(--accent) 12%, transparent)); }
.stat span { font-size:12px; color:var(--faint); }
.stat b { display:block; font-size:23px; font-family:var(--mono); letter-spacing:-.5px;
  color:var(--fg-strong); margin:4px 0 2px; }
.stat small { font-size:11.5px; color:var(--muted); }

/* ---- 地图 ---- */
.map-card { background:var(--panel); border:1px solid var(--line);
  border-radius:var(--radius); padding:14px 16px 10px; margin-bottom:18px; }
.map-head { display:flex; align-items:baseline; justify-content:space-between;
  gap:12px; flex-wrap:wrap; margin-bottom:6px; }
.map-head h2 { margin:0; font-size:14px; font-weight:600; color:var(--fg-strong); }
.map-head .sub { font-size:12px; color:var(--muted); }
.map-legend { display:flex; gap:14px; font-size:11.5px; color:var(--muted); }
.map-legend i { display:inline-block; width:8px; height:8px; border-radius:50%;
  margin-right:5px; vertical-align:middle; }
.map-legend .i-on { background:var(--ok); }
.map-legend .i-cur { background:var(--accent); }
.map-legend .i-off { background:var(--faint); }
.globe { position:relative; width:100%; height:min(64vh, 560px);
  max-width:900px; margin:8px auto 0; border-radius:var(--radius-sm); overflow:hidden;
  background:radial-gradient(70% 70% at 50% 45%,
    color-mix(in srgb, var(--accent) 7%, transparent), transparent 72%);
  touch-action:none; }
.globe canvas { position:absolute; inset:0; display:block; }
.globe-ov { cursor:grab; }
.globe-ov.grabbing { cursor:grabbing; }
.globe-tip { position:absolute; transform:translate(-50%, -100%);
  pointer-events:none; background:var(--panel); color:var(--fg);
  border:1px solid var(--line); border-radius:var(--radius-sm);
  padding:3px 8px; font-size:12px; line-height:1.5; white-space:nowrap;
  box-shadow:0 6px 18px -8px rgba(0,0,0,.3); z-index:3; }
.globe-tip b { display:block; }
.globe-tip span { color:var(--muted); font-size:11px; }
.globe-broken::after { content:'地球加载失败'; color:var(--muted);
  position:absolute; inset:0; display:flex; align-items:center; justify-content:center; }
.hint-drag { color:var(--faint); }
.mapbox { position:relative; border-radius:var(--radius-sm);
  background:radial-gradient(120% 80% at 50% 42%,
    color-mix(in srgb, var(--accent) 6%, transparent), transparent 72%); }
.mapbox svg { display:block; width:100%; height:auto; }
.land { fill:var(--sunken); stroke:var(--line); stroke-width:.6; }
.grat { stroke:var(--line); stroke-width:.4; stroke-dasharray:2 4; opacity:.7; }
.pin .dot { fill:var(--ok); filter:url(#glow); }
.pin .halo { fill:var(--ok); opacity:.18; }
/* 离线点不发光、光晕收小——否则几个灰点叠在一起会糊成一坨 */
.pin.off .dot, .pin.off .halo { fill:var(--faint); }
.pin.off .dot { filter:none; }
.pin.off .halo { r:13; opacity:.10; }
.pin.cur .dot { fill:var(--accent); }
.pin.cur .halo { fill:var(--accent); opacity:.22; }
/* 在线点做呼吸动画，让"点亮"这件事看得见 */
.pin.on .halo { animation:pulse 2.4s ease-out infinite; }
@keyframes pulse {
  0%   { transform:scale(.55); opacity:.34; }
  70%  { transform:scale(1.5);  opacity:0; }
  100% { transform:scale(1.5);  opacity:0; }
}
.pin:hover .dot { stroke:var(--fg); stroke-width:1.2; }

/* ---- 数据传输线：各节点 → 主机 ---- */
/* 底层是一条很淡的线，让"这条链路存在"看得见 */
.tline { fill:none; stroke:var(--line); stroke-width:1.1; }
.tline.off { stroke-dasharray:3 5; opacity:.55; }
/* 上层用虚线滚动表示「数据在流动」。
   dashoffset 往负方向推 = 沿 path 起点→终点方向前进，
   而 path 是「子机 → 主机」画的，所以看起来就是数据往主机跑。
   周期必须等于 dasharray 之和（7+11=18），否则会有跳格感。 */
/* 彗星拖尾：一段亮、一段空。周期 = 26+26 = 52，keyframes 必须推满一个周期，
   否则循环处会"跳一下"。 */
.tflow {
  fill:none; stroke:var(--accent); stroke-width:2; stroke-linecap:round;
  stroke-dasharray:26 26; opacity:.95;
  filter:url(#glow);
  animation:flow var(--dur, 2.6s) linear infinite;
}
@keyframes flow { to { stroke-dashoffset:-52; } }
/* 再叠一颗顺流而下的"数据包"，让传输感更具体 */
.packet { fill:var(--accent); opacity:.95; filter:url(#glow); }
/* 主机雷达波 */
.radar .ring { stroke:var(--accent); stroke-width:1.2; }
.pin.off ~ .flows .packet { display:none; }
@media (prefers-reduced-motion: reduce) {
  .tflow { animation:none; }
  .packet { display:none; }
  .pin.on .halo { animation:none; }
  .radar { display:none; }
}

/* ---- 列表形态切换 ---- */
.modes { display:flex; gap:0; border:1px solid var(--line);
  border-radius:var(--radius-sm); overflow:hidden; }
.mode-btn { border:0; background:var(--panel); color:var(--muted);
  font-size:12.5px; padding:4px 10px; cursor:pointer; }
.mode-btn + .mode-btn { border-left:1px solid var(--line); }
.mode-btn.on { background:var(--accent); color:var(--accent-fg); }

/* 三种形态只显示其一 */
body[data-k-list-mode="card"]    .view-table,
body[data-k-list-mode="card"]    .view-compact { display:none; }
body[data-k-list-mode="table"]   .view-card,
body[data-k-list-mode="table"]   .view-compact { display:none; }
body[data-k-list-mode="compact"] .view-card,
body[data-k-list-mode="compact"] .view-table { display:none; }

/* 表格形态 */
.tbl { width:100%; border-collapse:collapse; font-size:12.5px;
  background:var(--panel); border:1px solid var(--line); border-radius:var(--radius); }
.tbl th { text-align:left; font-weight:600; color:var(--muted); font-size:11.5px;
  padding:8px 10px; border-bottom:1px solid var(--line); background:var(--sunken); }
.tbl td { padding:7px 10px; border-bottom:1px solid var(--line); }
.tbl tbody tr:last-child td { border-bottom:0; }
.tbl tr.off { opacity:.55; }
.tbl .num { font-family:var(--mono); white-space:nowrap; }
.tbl .rg { color:var(--muted); }
.tbl .nm { margin-left:2px; }

/* 紧凑形态 */
.crow { display:flex; align-items:center; gap:10px; padding:6px 12px;
  border-bottom:1px solid var(--line); background:var(--panel);
  color:var(--fg); text-decoration:none; font-size:12.5px; }
.crow:first-child { border-radius:var(--radius) var(--radius) 0 0; }
.crow:last-child { border-bottom:0; border-radius:0 0 var(--radius) var(--radius); }
.view-compact { border:1px solid var(--line); border-radius:var(--radius); overflow:hidden; }
.crow:hover { background:var(--sunken); }
.crow.off { opacity:.55; }
.crow .p { width:170px; font-weight:500; }
.crow .rg { width:150px; color:var(--muted); }
.crow .num { font-family:var(--mono); white-space:nowrap; }
.crow .net { width:170px; }
.crow .bar { flex:1; height:5px; background:var(--sunken);
  border-radius:99px; overflow:hidden; min-width:60px; }
.crow .bar u { display:block; height:100%; background:var(--accent); }

/* ---- 站长名片 ---- */
.owner { display:flex; gap:14px; align-items:center; background:var(--panel);
  border:1px solid var(--line); border-radius:var(--radius);
  padding:14px 16px; margin-bottom:18px; }
.owner .avatar { width:52px; height:52px; border-radius:50%; object-fit:cover;
  border:1px solid var(--line); background:var(--sunken); flex:none;
  display:flex; align-items:center; justify-content:center;
  font-size:20px; color:var(--faint); }
.owner-txt b { font-size:16px; color:var(--fg-strong); }
.owner .bio { margin:3px 0 0; color:var(--muted); }

/* ---- 节点卡：偏"仪表盘"，信息尽量用条/图表达而不是堆数字 ---- */
.grid { display:grid; grid-template-columns:repeat(auto-fill,minmax(340px,1fr));
  gap:var(--gap); padding-bottom:28px; }
.card { background:var(--panel); border:1px solid var(--line);
  border-radius:var(--radius); padding:16px 18px 12px;
  display:flex; flex-direction:column; gap:10px;
  transition:border-color .18s ease, box-shadow .18s ease, transform .18s ease; }
.card:hover { border-color:color-mix(in srgb, var(--accent) 55%, var(--line));
  box-shadow:0 6px 20px -8px color-mix(in srgb, var(--accent) 40%, transparent);
  transform:translateY(-2px); }
.card.off { opacity:.6; }
.card.off:hover { transform:none; box-shadow:none; }

.card-head { display:flex; align-items:center; justify-content:space-between; gap:8px; }
.card-head h2 { margin:0; font-size:15px; font-weight:600; }
.card-head h2 a { color:var(--fg-strong); text-decoration:none; }
.card-head h2 a:hover { color:var(--accent); }
.card-head .flag { margin-right:6px; }
.status { display:inline-flex; align-items:center; gap:5px; flex:none;
  font-size:11.5px; color:var(--muted); }
.status i { width:7px; height:7px; border-radius:50%; background:var(--faint); }
.status.on { color:var(--ok); }
.status.on i { background:var(--ok); box-shadow:0 0 0 3px color-mix(in srgb, var(--ok) 18%, transparent); }
.dot { width:8px; height:8px; border-radius:50%; flex:none; }
.dot.on { background:var(--ok); } .dot.off { background:var(--faint); }
/* 国旗按真实比例 3:2 显示。不要加描边/白底——boss 明确要求
   "只要显示国旗就行"。日本旗这类白底旗在白卡片上会只剩中间的红色图案，
   那是国旗本身的样子，不是显示错了。 */
.flag { display:inline-flex; align-items:center; vertical-align:-2px;
  margin-right:5px; border-radius:2px; overflow:hidden; flex:none;
  width:1.5em; height:1em; }
.flag svg { display:block; width:100%; height:100%; }
.meta { margin:0; color:var(--muted); font-size:12.5px; }
.tags { display:flex; flex-wrap:wrap; gap:4px; }
.tag { display:inline-block; font-size:11px; line-height:1.6;
  padding:0 7px; border-radius:var(--radius-full);
  color:var(--muted); background:var(--sunken); border:1px solid var(--line); }
.tbl .tg { white-space:nowrap; }
.crow .tg { width:190px; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }

/* 资源条 */
.gauges { display:flex; flex-direction:column; gap:6px; }
.gauges .g { display:flex; align-items:center; gap:9px; font-size:12px; color:var(--muted); }
.gauges .g .gl { width:32px; flex:none; }
.gauges .g em { font-style:normal; font-family:var(--mono); width:46px;
  text-align:right; color:var(--fg); }
.bar { flex:1; height:6px; background:var(--sunken); border-radius:99px;
  overflow:hidden; display:block; }
.bar u { display:block; height:100%; border-radius:99px; background:var(--accent);
  transition:width .3s ease; }
.bar.mini { height:5px; width:74px; flex:none; }
/* 占用率配色：颜色表达状态，别让人去算数字 */
.lv-ok   { background:var(--ok); }
.lv-warn { background:var(--warn); }
.lv-bad  { background:var(--bad); }

/* 网络：一张小面积图 + 数字。
   ⚠️ 必须限定在 .card 内——紧凑视图里也用了 class="net"，
   不收作用域的话那条 border-top 会画到紧凑行上去。 */
.card .net { border-top:1px solid var(--line); padding-top:9px; }
.card .spark { display:block; width:100%; height:40px; }
.card .sp-area { fill:color-mix(in srgb, var(--accent) 16%, transparent); stroke:none; }
.card .sp-line { fill:none; stroke:var(--accent); stroke-width:1.6;
  stroke-linejoin:round; vector-effect:non-scaling-stroke; }
.card.off .sp-line { stroke:var(--faint); }
.card.off .card .sp-area { fill:color-mix(in srgb, var(--faint) 16%, transparent); }
.card .net-num { display:flex; align-items:center; gap:12px; margin-top:5px;
  font-size:11.5px; color:var(--muted); font-family:var(--mono); flex-wrap:wrap; }
.card .net-num .up { color:var(--ok); }
.card .net-num .down { color:var(--accent); }
.card .net-num .lat { margin-left:auto; display:inline-flex; align-items:center; gap:6px; font-family:inherit; }

/* 规格表：两列，标签弱、值强 */
.specs { display:grid; grid-template-columns:1fr 1fr; gap:4px 14px; margin:0;
  border-top:1px solid var(--line); padding-top:9px; font-size:11.5px; }
.specs > div { display:flex; align-items:center; gap:8px; min-width:0; }
.specs dt { color:var(--faint); flex:none; }
.specs dd { margin:0; color:var(--fg); font-family:var(--mono);
  white-space:nowrap; overflow:hidden; text-overflow:ellipsis;
  display:flex; align-items:center; gap:6px; }
.foot { display:flex; align-items:center; margin-top:2px; font-size:12px; }
.foot .more { margin-left:auto; text-decoration:none; }
.foot-page { border-top:1px solid var(--line); color:var(--faint);
  font-size:12px; padding:14px 0 22px; }
</style>
</head>
<body data-k-list-mode="card">

<div class="banner"><div class="wrap">
  <b>本地预览</b> · 这是首页改版的静态样稿，数据为<b>示例</b>（其中「东京 zouter」是线上真实节点）。
  定稿后我再搬进 Go 模板。
</div></div>

<header class="top"><div class="wrap">
  <a class="brand" href="#">Kokoro</a>
  <form class="search" onsubmit="return false">
    <input type="search" placeholder="搜小鸡：名字 / 地区 / 机房 / 标签…">
    <button class="btn" type="submit">搜索</button>
  </form>
  <div class="top-tools">
    <div class="modes" role="group" aria-label="列表显示方式">
      <button class="mode-btn on" data-set-list="card" type="button" title="卡片视图">卡片</button>
      <button class="mode-btn" data-set-list="table" type="button" title="表格视图">表格</button>
      <button class="mode-btn" data-set-list="compact" type="button" title="紧凑视图">紧凑</button>
    </div>
    <button class="btn" id="mode">☾</button>
    <button class="skin">◐ 晨白</button>
    <a class="btn" href="#">仪表盘</a>
    <a class="btn" href="#">后台</a>
  </div>
</div></header>

<main class="wrap">

  <section class="hero">
    <div>
      <h1>Kokoro</h1>
      <p>每台小鸡都有一颗心 · 实时监控 · 全省三网探测</p>
    </div>
  </section>

  <section class="clocks">__CLOCKS__</section>

  <section class="stats">__STATS__</section>

  <section class="map-card">
    <div class="map-head">
      <h2>全球节点</h2>
      <div class="map-legend">
        <span><i class="i-on"></i>在线</span>
        <span><i class="i-cur"></i>主机</span>
        <span><i class="i-off"></i>离线</span>
        <span class="hint-drag">拖动旋转</span>
      </div>
    </div>
    <div class="globe" id="globe" aria-label="全球节点与数据流向"></div>
    </section>

  <section class="owner">
    <div class="avatar">🌪</div>
    <div class="owner-txt">
      <b>站长阿宝</b>
      <p class="bio">只卖靠谱的小鸡，跑路包赔。有问题随时留言。</p>
    </div>
  </section>

  <!-- 三种列表形态同时渲染，靠 body[data-k-list-mode] 显示其一。
       真实站点也是这套机制（纯 CSS 切换 + cookie 记忆），所以这里能直接演示。 -->
  <section class="view view-card">
    <div class="grid">__CARDS__</div>
  </section>

  <section class="view view-table">
    <table class="tbl">
      <thead><tr>
        <th>节点</th><th>地区</th><th>标签</th><th>CPU</th><th>内存</th><th>磁盘</th>
        <th>上行</th><th>下行</th><th>运行</th><th>延迟</th><th>状态</th><th></th>
      </tr></thead>
      <tbody>__ROWS__</tbody>
    </table>
  </section>

  <section class="view view-compact">
    __COMPACT__
  </section>

</main>

<footer class="wrap foot-page"><span>Kokoro · 2026 · 皮肤《晨白》</span></footer>

<!-- 地球渲染器。生产里走 /static/globe.js，预览这里用相对路径直接引源文件 -->
<script src="../../internal/hub/static/globe.js"></script>
<script>
/* 时钟：本地时间交给浏览器算，其他时区用 Intl 格式化。
   生产实现同样只输出时区名，具体时间在前端算，避免服务端时区问题。 */
(function () {
  'use strict';
  function pad(n) { return n < 10 ? '0' + n : '' + n; }
  function tick() {
    var cells = document.querySelectorAll('.clock');
    for (var i = 0; i < cells.length; i++) {
      var tz = cells[i].getAttribute('data-tz');
      var now = new Date();
      var hh, mm, ss, dateStr;
      if (!tz) {
        hh = now.getHours(); mm = now.getMinutes(); ss = now.getSeconds();
        dateStr = now.getFullYear() + '-' + pad(now.getMonth() + 1) + '-' + pad(now.getDate());
      } else {
        var p = new Intl.DateTimeFormat('en-GB', {
          timeZone: tz, hour: '2-digit', minute: '2-digit', second: '2-digit',
          hour12: false, year: 'numeric', month: '2-digit', day: '2-digit'
        }).formatToParts(now).reduce(function (o, x) { o[x.type] = x.value; return o; }, {});
        hh = +p.hour; mm = +p.minute; ss = +p.second;
        dateStr = p.year + '-' + p.month + '-' + p.day;
      }
      cells[i].querySelector('.t').textContent = pad(hh) + ':' + pad(mm) + ':' + pad(ss);
      cells[i].querySelector('.d').textContent = dateStr;
    }
  }
  tick();
  setInterval(tick, 1000);

  document.getElementById('mode').addEventListener('click', function () {
    var h = document.documentElement;
    var next = h.getAttribute('data-k-mode') === 'dark' ? 'light' : 'dark';
    h.setAttribute('data-k-mode', next);
    this.textContent = next === 'dark' ? '☀' : '☾';
  });

  /* ---- 点阵地球 ---- */
  (function () {
    var host = document.getElementById('globe');
    if (!host || !window.KokoroGlobe) return;
    var PLACES = __PLACES__;
    var HUB = { name: '东京', lat: 35.68, lon: 139.69 };
    function cssVar(el, name, fb) {
      var v = getComputedStyle(el).getPropertyValue(name).trim();
      return v || fb;
    }
    var g = new KokoroGlobe(host, {
      landInline: "__LAND__",
      hub: HUB,
      places: PLACES,
      colors: function () {
        var el = document.documentElement;
        return {
          day:   cssVar(el, '--globe-day', '#c9d4e2'),
          night: cssVar(el, '--globe-night', '#39435a'),
          arc:   cssVar(el, '--accent', '#2f6feb'),
          hub:   cssVar(el, '--accent', '#2f6feb'),
          ok:    cssVar(el, '--ok', '#1a7f37'),
          off:   cssVar(el, '--faint', '#8b949e'),
          rim:   cssVar(el, '--globe-rim', 'rgba(47,111,235,.20)')
        };
      }
    });
    // 演示脉冲：每条在线航线轮流亮一下，让人看到"数据在流"
    var i = 0;
    setInterval(function () {
      var on = PLACES.filter(function (p) { return p.online; });
      if (!on.length) return;
      g.pulse(on[i % on.length].key);
      i++;
    }, 1800);
  })();

  /* 列表形态：改 body 上的属性，纯 CSS 切换三种排布。
     真实站点还会写 k_list_mode cookie 记住选择，这里只演示切换。 */
  var btns = document.querySelectorAll('[data-set-list]');
  for (var i = 0; i < btns.length; i++) {
    btns[i].addEventListener('click', function (ev) {
      var mode = ev.currentTarget.getAttribute('data-set-list');
      document.body.setAttribute('data-k-list-mode', mode);
      for (var j = 0; j < btns.length; j++) {
        btns[j].classList.toggle('on',
          btns[j].getAttribute('data-set-list') === mode);
      }
    });
  }
})();
</script>
</body>
</html>
"""


def main():
    if not os.path.exists(os.path.join(GEO, "world.path")):
        print("缺少地理数据，先跑 python scripts/gen-geo-svg.py")
        return 1
    os.makedirs(OUT_DIR, exist_ok=True)
    html = build_html()

    # 生成前自检：CSS 花括号必须配平。
    # 少一个/多一个会让浏览器把后面整段样式丢掉，而页面"看着还行"，
    # 极难发现——实测因此让暗色主题整块失效过。
    css = html.split("<style>", 1)[1].split("</style>", 1)[0]
    bal = css_balance(css)
    if bal != 0:
        print("CSS 花括号不配平（净值 %+d）：浏览器会把后面整段样式丢掉" % bal)
        return 1

    with io.open(OUT, "w", encoding="utf-8", newline="") as fp:
        fp.write(html)
    print("已生成 %s（%.0f KB）" % (OUT, len(html.encode("utf-8")) / 1024.0))
    return 0


if __name__ == "__main__":
    sys.exit(main())
