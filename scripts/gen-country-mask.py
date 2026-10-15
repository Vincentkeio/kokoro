#!/usr/bin/env python
"""把国界栅格化成「国家码 -> 格子」的紧凑表，供地球"点亮区域"使用。

## 为什么要它

首页地球要把"有节点的国家/地区"整片点亮，而不是只画一个点。要判断
"这个格子属于哪个国家"，浏览器里做多边形包含判断不可行（1.5MB 矢量
+ 每帧 24 万次测试）；所以离线栅格化成索引表，运行时只查表。

## 与陆地掩码的关系

网格参数（0.5°、lat ±85、lon ±180、720x340）与 `gen-land-mask.py`
**完全一致**，这样两边算出来的格子索引可以直接对应；输出同样是给
Go 内嵌的资源，客户端不下载这份表 —— Hub 只把"点亮国家"占用的格子
run 压缩后塞进地球 JSON（几 KB），前端按 run 把纹理染色。

## 编码

    magic(4)="KCM1" + nlon(u16) + nlat(u16) + latMax(f32) + latMin(f32)
    + step(f32) + nCodes(u16) + palette(nCodes x 2 bytes ASCII)
    + runs: 重复 [varint(count) + u8(codeIndex)]

run 按**纹理序**（与 globe.js 的 landTextureData 相同的遍历顺序：
y=0 是最南一行）展开，前端的索引因此可以直接用。

## 用法

    python scripts/gen-country-mask.py
输出：
    internal/geo/country.bin
"""
import io
import json
import os
import re
import struct
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
GEO = os.path.join(ROOT, "work", "geo")
FLAGS_EN = os.path.join(ROOT, "internal", "flags", "names_en.go")
OUT = os.path.join(ROOT, "internal", "geo", "country.bin")

LAT_MAX = 85.0
LAT_MIN = -85.0
LON_MIN = -180.0
LON_MAX = 180.0
STEP = 0.5
NLON = int(round((LON_MAX - LON_MIN) / STEP))   # 720
NLAT = int(round((LAT_MAX - LAT_MIN) / STEP))   # 340

# 与 gen-country-centroids.py 保持一致的别名表（那份是给质心用的）。
# 英文名 -> ISO 码的映射来自 internal/flags/names_en.go。
ALIASES = {
    "aland": "Åland Islands",
    "antiguaandbarb": "Antigua and Barbuda",
    "bosniaandherz": "Bosnia and Herzegovina",
    "brindianoceanter": "British Indian Ocean Territory",
    "brunei": "Brunei Darussalam",
    "capeverde": "Cabo Verde",
    "caymanis": "Cayman Islands",
    "centralafricanrep": "Central African Republic",
    "congo": "Congo",
    "curacao": "Curaçao",
    "czechrep": "Czechia",
    "cotedivoire": "Côte d'Ivoire",
    "demrepcongo": "Democratic Republic of the Congo",
    "demrepkorea": "North Korea",
    "dominicanrep": "Dominican Republic",
    "eqguinea": "Equatorial Guinea",
    "faeroeis": "Faroe Islands",
    "falklandis": "Falkland Islands",
    "frpolynesia": "French Polynesia",
    "frsantarcticlands": "French Southern Territories",
    "heardiandmcdonaldis": "Heard Island and McDonald Islands",
    "korea": "South Korea",
    "laopdr": "Laos",
    "macedonia": "North Macedonia",
    "micronesia": "Micronesia",
    "ncyprus": "Cyprus",
    "nmarianais": "Northern Mariana Islands",
    "palestine": "Palestine",
    "sgeoandsandwis": "South Georgia and the South Sandwich Islands",
    "ssudan": "South Sudan",
    "sainthelena": "Saint Helena",
    "siachenglacier": "",
    "solomonis": "Solomon Islands",
    "stpierreandmiquelon": "Saint Pierre and Miquelon",
    "stvinandgren": "Saint Vincent and the Grenadines",
    "swaziland": "Eswatini",
    "saotomeandprincipe": "Sao Tome and Principe",
    "turkey": "Türkiye",
    "turksandcaicosis": "Turks and Caicos Islands",
    "usvirginis": "United States Virgin Islands",
    "unitedstates": "United States of America",
    "wsahara": "Western Sahara",
}


# 数据源里**没有独立要素**、但探针常见落点的小地方（香港、澳门……）。
# world.json 里香港/澳门并在 China 内，台湾也在 China 内；不单独补一张表，
# 「香港的机器」就点亮不了任何东西。值取当地中心经纬度。
EXTRA = {
    "HK": (22.3193, 114.1694),
    "MO": (22.1987, 113.5439),
    "TW": (23.6978, 120.9605),
}


def norm(s):
    return re.sub(r"[^a-z0-9]", "", s.lower())


def load_en_names():
    src = io.open(FLAGS_EN, encoding="utf-8").read()
    m = re.search(r"var\s+\w+\s*=\s*map\[string\]string\{(.*?)\n\}", src, re.S)
    return dict(re.findall(r'"([^"]+)"\s*:\s*"([^"]+)"', m.group(1)))


def rings(geom):
    t = geom.get("type")
    if t == "Polygon":
        return [geom["coordinates"]]
    if t == "MultiPolygon":
        return geom["coordinates"]
    return []


def feature_edges(feature):
    edges = []
    for poly in rings(feature.get("geometry") or {}):
        for ring in poly:
            pts = [p for p in ring if isinstance(p, (list, tuple)) and len(p) >= 2]
            for i in range(len(pts)):
                x1, y1 = float(pts[i][0]), float(pts[i][1])
                x2, y2 = float(pts[(i + 1) % len(pts)][0]), float(pts[(i + 1) % len(pts)][1])
                if abs(x2 - x1) > 180.0:
                    continue  # 跨换日线，交给两侧各自的边
                edges.append((x1, y1, x2, y2))
    return edges


def fill(feature, grid, code_idx):
    """扫描线奇偶填充，把该要素覆盖的格子写成 code_idx。"""
    edges = feature_edges(feature)
    if not edges:
        return 0
    buckets = {}
    for (x1, y1, x2, y2) in edges:
        lo, hi = (y1, y2) if y1 <= y2 else (y2, y1)
        r0 = int((LAT_MAX - hi) / STEP)
        r1 = int((LAT_MAX - lo) / STEP)
        for r in range(max(0, r0), min(NLAT - 1, r1) + 1):
            buckets.setdefault(r, []).append((x1, y1, x2, y2))
    filled = 0
    for r in range(NLAT):
        lat = LAT_MAX - (r + 0.5) * STEP
        xs = []
        for (x1, y1, x2, y2) in buckets.get(r, ()):
            if (y1 <= lat < y2) or (y2 <= lat < y1):
                t = (lat - y1) / (y2 - y1)
                xs.append(x1 + t * (x2 - x1))
        if len(xs) < 2:
            continue
        xs.sort()
        texY = NLAT - 1 - r          # 纹理序：y=0 是最南一行
        base = texY * NLON
        for k in range(0, len(xs) - 1, 2):
            a, b = xs[k], xs[k + 1]
            c0 = int((a - LON_MIN) / STEP)
            c1 = int((b - LON_MIN) / STEP)
            if c1 < 0 or c0 >= NLON:
                continue
            c0 = max(0, c0)
            c1 = min(NLON - 1, c1)
            for c in range(c0, c1 + 1):
                if grid[base + c] != code_idx:
                    grid[base + c] = code_idx
                    filled += 1
    return filled


def ring_area_centroid(ring):
    if len(ring) < 3:
        return 0.0, 0.0, 0.0
    a = cx = cy = 0.0
    n = len(ring)
    for i in range(n):
        x1, y1 = ring[i][0], ring[i][1]
        x2, y2 = ring[(i + 1) % n][0], ring[(i + 1) % n][1]
        cross = x1 * y2 - x2 * y1
        a += cross
        cx += (x1 + x2) * cross
        cy += (y1 + y2) * cross
    if abs(a) < 1e-12:
        return 0.0, 0.0, 0.0
    a *= 0.5
    return abs(a), cx / (6 * a), cy / (6 * a)


def centroid_of(feature):
    """面积最大多边形的质心（和 gen-country-centroids.py 同一套算法）。"""
    best = (0.0, 0.0, 0.0)
    for poly in rings(feature.get("geometry") or {}):
        if not poly:
            continue
        area, lon, lat = ring_area_centroid(poly[0])
        if area > best[0]:
            best = (area, lon, lat)
    return best[1], best[2]


def nearest_cell(lon, lat):
    """把经纬度吸附到同一套网格的格子（纹理序索引）。

    香港、新加坡这类小地方在 0.5° 网格里可能一个格心都盖不到 ——
    它们恰恰是探针常见落点，不兜底就等于"有节点却不点亮"。
    """
    col = int((lon - LON_MIN) / STEP)
    row = int((LAT_MAX - lat) / STEP)
    col = max(0, min(NLON - 1, col))
    row = max(0, min(NLAT - 1, row))
    return (NLAT - 1 - row) * NLON + col


def varint(n):
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        if n:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)


def main():
    world = os.path.join(GEO, "world.json")
    if not os.path.exists(world):
        print("缺少 work/geo/world.json，先跑 scripts/gen-geo-svg.py")
        return 1

    en2cc = load_en_names()
    data = json.load(io.open(world, encoding="utf-8"))

    # 先定调色板：把能映射到 ISO 码的要素收集起来（排序保证输出稳定）。
    mapping = []          # [(code, feature)]
    skipped = []
    codes = set()
    for f in data["features"]:
        name = (f.get("properties") or {}).get("name") or ""
        key = norm(name)
        code = en2cc.get(key) if key else None
        if not code and key:
            alias = ALIASES.get(key)
            if alias:
                code = en2cc.get(norm(alias))
        if not code:
            skipped.append(name)
            continue
        mapping.append((code, f))
        codes.add(code)

    palette = sorted(codes)
    idx_of = {c: i + 1 for i, c in enumerate(palette)}
    if len(palette) > 255:
        print("国家数超过 255，调色板放不下")
        return 1

    grid = bytearray(NLON * NLAT)
    tiny = []
    for code, f in mapping:
        if fill(f, grid, idx_of[code]) == 0:
            tiny.append((code, f))

    # 小地方兜底：占不到任何格心的，吸附到最近的那一格。
    # 放在所有正常要素之后跑，免得被大国的填充盖掉。
    for code, f in tiny:
        lon, lat = centroid_of(f)
        if lon == 0 and lat == 0:
            continue
        grid[nearest_cell(lon, lat)] = idx_of[code]
    if tiny:
        print("小地方按最近格心兜底：%d 个" % len(tiny))

    # 源数据里没有独立要素的地方（见 EXTRA），按给定坐标钉到最近格心。
    for code, (lat, lon) in EXTRA.items():
        if code not in idx_of:
            palette.append(code)
            idx_of[code] = len(palette)
        grid[nearest_cell(lon, lat)] = idx_of[code]

    # RLE（纹理序）
    runs = []
    prev = grid[0] if grid else 0
    count = 0
    for v in grid:
        if v == prev:
            count += 1
            continue
        runs.append((count, prev))
        prev = v
        count = 1
    if count:
        runs.append((count, prev))

    body = bytearray()
    for c, v in runs:
        body += varint(c)
        body.append(v)

    head = struct.pack("<4sHHfffH", b"KCM1", NLON, NLAT, LAT_MAX, LAT_MIN, STEP, len(palette))
    pal = b"".join(c.encode("ascii") for c in palette)
    blob = head + pal + bytes(body)

    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    with open(OUT, "wb") as fp:
        fp.write(blob)

    land = sum(1 for v in grid if v)
    print("国家 %d 个，格子 %d x %d，有归属的格子 %d（%.1f%%）"
          % (len(palette), NLON, NLAT, land, land * 100.0 / (NLON * NLAT)))
    print("run 数 %d，输出 %s（%d 字节）" % (len(runs), os.path.relpath(OUT, ROOT), len(blob)))
    if skipped:
        # 控制台可能是 GBK，国家名里有非 ASCII 字符时 print 会抛 UnicodeEncodeError；
        # 这份输出只是给人看的，不该因为它让生成任务非零退出。
        safe = ", ".join(skipped[:10]).encode("ascii", "backslashreplace").decode("ascii")
        print("跳过 %d 个无映射要素：%s" % (len(skipped), safe))
    return 0


if __name__ == "__main__":
    sys.exit(main())
