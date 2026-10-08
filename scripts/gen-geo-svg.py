#!/usr/bin/env python
"""把 GeoJSON 轮廓转成紧凑的内联 SVG path，供探针首页的「全球点亮地图」使用。

## 为什么需要这个脚本

首页要放一张世界地图，但项目有硬约束：**单二进制、无 CDN、无外部服务**。
所以不能用任何地图服务/瓦片，只能把矢量轮廓嵌进二进制里。
原始 GeoJSON 加起来 1.5MB，直接塞进去太重，必须先简化。

## 合规处理（关键，别改坏）

底图用 echarts 的 world.json（源自海外数据集），**但中国的画法不能信它**——
它把中国当一条整体要素、且没有台湾省。所以：

  - 底图 = world.json **剔除 'China' 要素**后的其余陆地；
  - 中国 = 阿里 DataV 的 `100000_full.json`（含台湾省、香港、澳门，
    海南省几何覆盖到 3.8°N 含南海诸岛），叠加绘制。

两层同色渲染，视觉上是一整块；但中国的边界来自符合国标的数据源。
**不要为了"省事"把 DataV 那层去掉**，那会让中国边界退回海外数据的画法。

## 用法

    python scripts/gen-geo-svg.py

读 `work/geo/*.json`，输出 `work/geo/out/*.txt`（纯 path 数据，可直接粘进模板）。
"""
import json
import math
import os
import sys

SRC = os.path.join("work", "geo")
OUT = os.path.join("work", "geo", "out")

# 画布：等距圆柱投影。纬度裁到 84~-58，去掉南极和北极圈的空白，
# 让画面更紧凑（360° 经度 / 142° 纬度 ≈ 2.54:1）。
LON_MIN, LON_MAX = -180.0, 180.0
LAT_MAX, LAT_MIN = 84.0, -58.0
W = 1000.0
H = W * (LAT_MAX - LAT_MIN) / (LON_MAX - LON_MIN)

# 投影后坐标保留几位小数。1 位对 1000px 宽的画布已经足够（0.1px）。
PREC = 1
# 相邻点距离小于此值就丢掉（单位：投影后的像素）
MIN_STEP = 1.8
# 多边形包围盒面积小于此值就整个丢掉（去掉看不见的小岛）
MIN_AREA = 14


def project(lon, lat):
    x = (lon - LON_MIN) / (LON_MAX - LON_MIN) * W
    y = (LAT_MAX - lat) / (LAT_MAX - LAT_MIN) * H
    return x, y


def rings(geom):
    """把 Polygon / MultiPolygon 统一成 rings 列表。"""
    t = geom["type"]
    if t == "Polygon":
        return [geom["coordinates"]]
    if t == "MultiPolygon":
        return geom["coordinates"]
    return []


def ring_path(coords):
    """一个环 -> "M x y L x y ... Z"；太短或太小的直接丢（返回 None）。"""
    pts = []
    for c in coords:
        if not isinstance(c, (list, tuple)) or len(c) < 2:
            continue
        x, y = project(float(c[0]), float(c[1]))
        if pts:
            px, py = pts[-1]
            if abs(x - px) < MIN_STEP and abs(y - py) < MIN_STEP:
                continue
        pts.append((x, y))
    # 环要闭合；首尾太近时补一下，避免画出缺口
    if len(pts) < 3:
        return None
    if pts[0] != pts[-1]:
        pts.append(pts[0])
    # 包围盒面积过滤：小岛（比如几百米级）在这个尺度上根本看不见
    xs = [p[0] for p in pts]
    ys = [p[1] for p in pts]
    if (max(xs) - min(xs)) * (max(ys) - min(ys)) < MIN_AREA:
        return None
    f = ("%." + str(PREC) + "f")
    out = ["M", f % pts[0][0], f % pts[0][1]]
    for x, y in pts[1:]:
        out += ["L", f % x, f % y]
    out.append("Z")
    return " ".join(out)


def geom_to_path(geom):
    parts = []
    for poly in rings(geom):
        for ring in poly:
            p = ring_path(ring)
            if p:
                parts.append(p)
    return "".join(parts)


def load(name):
    with open(os.path.join(SRC, name), encoding="utf-8") as fp:
        return json.load(fp)


def main():
    os.makedirs(OUT, exist_ok=True)

    # ---- 1. 世界底图：剔除 China（中国改用国标数据叠加）----
    world = load("world.json")
    base_parts = []
    dropped = []
    for f in world["features"]:
        nm = (f.get("properties") or {}).get("name") or ""
        if nm == "China":
            dropped.append(nm)
            continue
        base_parts.append(geom_to_path(f["geometry"]))
    base = "".join(base_parts)

    # ---- 2. 中国：DataV 国标数据（含台湾省、港澳、南海诸岛）----
    china = load("china.json")
    china_parts = []
    for f in china["features"]:
        china_parts.append(geom_to_path(f["geometry"]))
    cn = "".join(china_parts)

    with open(os.path.join(OUT, "world.path"), "w", encoding="utf-8", newline="") as fp:
        fp.write(base)
    with open(os.path.join(OUT, "china.path"), "w", encoding="utf-8", newline="") as fp:
        fp.write(cn)

    print("画布 viewBox: 0 0 %.0f %.1f" % (W, H))
    print("底图要素数 %d（剔除 %s），path 长度 %d 字节" % (
        len(world["features"]) - len(dropped), dropped or "无", len(base)))
    print("中国要素数 %d，path 长度 %d 字节" % (len(china["features"]), len(cn)))
    print("合计 %.0f KB -> %s" % ((len(base) + len(cn)) / 1024.0, OUT))
    return 0


if __name__ == "__main__":
    sys.exit(main())
