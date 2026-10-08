#!/usr/bin/env python
"""把陆地轮廓栅格化成紧凑位图，供 WebGL 点阵地球使用。

## 为什么要位图而不是直接用 GeoJSON

点阵地球要的是「哪些经纬度是陆地」这个布尔判断，跑在浏览器里。
原始的 world.json + china.json 有 1.5MB 且是矢量，前端每帧去判断点是否在多边形内
完全不可行。栅格化成位图后：**244,800 个格子 = 30.6 KB**，一次解码常驻内存。

## 栅格化方式：扫描线 + 奇偶填充

按纬度逐行求所有边与该行的交点，排序后两两配对填充。
用奇偶规则能自动处理「多边形带洞」（比如里海、五大湖），
不需要额外区分外环和内环。

## 合规

底图与国界都沿用生成地图时那套：
  - world.json（全部要素，含它自己的 China）
  - china.json（DataV 国标，含台湾省、港澳，海南几何覆盖到 3.8°N 含南海诸岛）
两者取**并集**。这样台湾、南海诸岛一定是陆地——不会因为海外数据把中国画成
一整块、漏掉台湾，而在点阵上出现一个洞。

## 用法

    python scripts/gen-land-mask.py
输出：
    internal/hub/static/land.bin   给生产用（走 /static/ 静态路由）
    work/geo/out/land.b64          给预览页内联（file:// 下 fetch 会被拦）
"""
import base64
import io
import json
import os
import struct
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GEO = os.path.join(ROOT, "work", "geo")
OUT_BIN = os.path.join(ROOT, "internal", "hub", "static", "land.bin")
OUT_B64 = os.path.join(ROOT, "work", "geo", "out", "land.b64")

# 网格参数。0.5° 是分辨率与体积的折中：
# 再细一档（0.25°）点数翻四倍、体积到 120KB，视觉上已经看不出差别。
LAT_MAX = 85.0
LAT_MIN = -85.0
LON_MIN = -180.0
LON_MAX = 180.0
STEP = 0.5

NLON = int(round((LON_MAX - LON_MIN) / STEP))   # 720
NLAT = int(round((LAT_MAX - LAT_MIN) / STEP))   # 340


def rings(geom):
    t = geom.get("type")
    if t == "Polygon":
        return [geom["coordinates"]]
    if t == "MultiPolygon":
        return geom["coordinates"]
    return []


def load(name):
    with io.open(os.path.join(GEO, name), encoding="utf-8") as fp:
        return json.load(fp)


def collect_edges(name):
    """把一份数据集里的所有环拆成边列表 [(lon1,lat1,lon2,lat2), ...]。

    ⚠️ 一次只处理**一份数据集**。两份数据（world 的中国 + DataV 的各省）
    在空间上是重叠的，而扫描线用的是奇偶规则——把两份的边混在一起跑，
    重叠区会有偶数次穿越、被判成"外部"，于是在省界上留下一圈白线。
    必须各自栅格化、最后取并集。
    """
    edges = []
    data = load(name)
    for f in data["features"]:
        for poly in rings(f.get("geometry") or {}):
            for ring in poly:
                pts = [p for p in ring if isinstance(p, (list, tuple)) and len(p) >= 2]
                for i in range(len(pts)):
                    x1, y1 = float(pts[i][0]), float(pts[i][1])
                    x2, y2 = float(pts[(i + 1) % len(pts)][0]), float(pts[(i + 1) % len(pts)][1])
                    if abs(x2 - x1) > 180.0:
                        # 跨换日线：跳过这条边（由两侧各自的边覆盖），
                        # 比强行插值更稳，陆地不会因此缺角。
                        continue
                    edges.append((x1, y1, x2, y2))
    return edges


def rasterize(edges):
    """扫描线奇偶填充，返回 bytearray（每行 ceil(NLON/8) 字节）。"""
    stride = (NLON + 7) // 8
    grid = bytearray(stride * NLAT)

    # 按纬度分桶，避免每行都遍历全部边
    buckets = {}
    for (x1, y1, x2, y2) in edges:
        lo, hi = (y1, y2) if y1 <= y2 else (y2, y1)
        r0 = int((LAT_MAX - hi) / STEP)      # 上边界所在行
        r1 = int((LAT_MAX - lo) / STEP)      # 下边界所在行
        for r in range(max(0, r0), min(NLAT - 1, r1) + 1):
            buckets.setdefault(r, []).append((x1, y1, x2, y2))

    for r in range(NLAT):
        lat = LAT_MAX - (r + 0.5) * STEP     # 行中心的纬度
        xs = []
        for (x1, y1, x2, y2) in buckets.get(r, ()):
            # 半开区间：下闭上开，避免顶点正好落在扫描线上被数两次
            if (y1 <= lat < y2) or (y2 <= lat < y1):
                t = (lat - y1) / (y2 - y1)
                xs.append(x1 + t * (x2 - x1))
        if len(xs) < 2:
            continue
        xs.sort()
        base = r * stride
        for k in range(0, len(xs) - 1, 2):
            a, b = xs[k], xs[k + 1]
            c0 = int((a - LON_MIN) / STEP)
            c1 = int((b - LON_MIN) / STEP)
            if c1 < 0 or c0 >= NLON:
                continue
            c0 = max(0, c0)
            c1 = min(NLON - 1, c1)
            for c in range(c0, c1 + 1):
                grid[base + (c >> 3)] |= 1 << (7 - (c & 7))
    return grid


def main():
    if not os.path.exists(os.path.join(GEO, "world.json")):
        print("缺少 work/geo/world.json，先跑 scripts/gen-geo-svg.py")
        return 1

    # 两份数据分别栅格化再合并：见 collect_edges 上面的说明。
    grid = None
    for name in ("world.json", "china.json"):
        edges = collect_edges(name)
        g = rasterize(edges)
        print("%-12s 边数 %6d  陆地格子 %d" % (
            name, len(edges), sum(bin(b).count("1") for b in g)))
        if grid is None:
            grid = g
        else:
            for i in range(len(grid)):
                grid[i] |= g[i]

    land = sum(bin(b).count("1") for b in grid)
    print("网格 %d x %d，陆地格子 %d（%.1f%%）" % (NLON, NLAT, land, land * 100.0 / (NLON * NLAT)))

    # 文件头：magic + 版本 + 网格参数，前端据此校验，避免参数改了却忘了改前端
    head = struct.pack("<4sHHfff", b"KLD1", NLON, NLAT, LAT_MAX, LAT_MIN, STEP)
    blob = head + bytes(grid)

    os.makedirs(os.path.dirname(OUT_BIN), exist_ok=True)
    with open(OUT_BIN, "wb") as fp:
        fp.write(blob)
    os.makedirs(os.path.dirname(OUT_B64), exist_ok=True)
    with io.open(OUT_B64, "w", encoding="ascii", newline="") as fp:
        fp.write(base64.b64encode(blob).decode("ascii"))

    print("已写出 %s（%d 字节）" % (os.path.relpath(OUT_BIN, ROOT), len(blob)))
    print("已写出 %s（%d 字节 base64）" % (os.path.relpath(OUT_B64, ROOT),
                                        len(base64.b64encode(blob))))
    return 0


if __name__ == "__main__":
    sys.exit(main())
