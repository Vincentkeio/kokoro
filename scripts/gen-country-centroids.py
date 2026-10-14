#!/usr/bin/env python
"""从世界轮廓数据生成「国家码 -> 经纬度」表，供首页地球把节点摆到正确位置。

## 为什么要自动生成

点阵地球需要每台小鸡的经纬度，而 `model.Node` 只存了 country / region / city 文本。
手敲两百多个国家的坐标既慢又容易错，而这份几何本来就在仓库里
（`work/geo/world.json`，与生成地图、陆地掩码用的是同一份）。

流程：
  1. world.json 的每个要素取出英文国名；
  2. 用 `internal/flags/names_en.go` 里的「英文名 -> ISO 码」映射换码
     （那份表本来就是自动生成的，251 条，覆盖 173 个国家）；
  3. 剩下 44 个是缩写（"Bosnia and Herz."、"Dem. Rep. Congo"…），用别名表补齐；
  4. 取**面积最大的那个多边形**算质心——不这么干的话，
     法国（含海外省）、美国（含阿拉斯加/夏威夷）的质心会飘到海里去。

## 用法

    python scripts/gen-country-centroids.py
输出：
    internal/geo/countries.go
"""
import io
import json
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
WORLD = os.path.join(ROOT, "work", "geo", "world.json")
FLAGS_EN = os.path.join(ROOT, "internal", "flags", "names_en.go")
OUT = os.path.join(ROOT, "internal", "geo", "countries.go")

# 世界数据里的缩写 -> 规范英文名。只覆盖 names_en.go 里认不出来的那些。
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


def norm(s):
    """和 names_en.go 的折叠规则保持一致：小写 + 只留字母数字。"""
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


def ring_area_centroid(ring):
    """鞋带公式：返回 (面积绝对值, 质心 lon, 质心 lat)。

    注意经度不跨 ±180 的多边形才有意义。跨换日线的（斐济、新西兰部分）
    算出来会偏，但那些国家也不是探针的常见落点，可接受。
    """
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


def centroid(feature):
    """取面积最大的多边形算质心——避免海外领地／飞地把质心拽到海里。"""
    best = (0.0, 0.0, 0.0)
    for poly in rings(feature["geometry"]):
        if not poly:
            continue
        area, lon, lat = ring_area_centroid(poly[0])
        if area > best[0]:
            best = (area, lon, lat)
    return best[1], best[2]


def main():
    en2cc = load_en_names()
    data = json.load(io.open(WORLD, encoding="utf-8"))

    out = {}
    skipped = []
    for f in data["features"]:
        name = (f.get("properties") or {}).get("name") or ""
        key = norm(name)
        if not key:
            continue
        code = en2cc.get(key)
        if not code:
            alias = ALIASES.get(key)
            if alias:
                code = en2cc.get(norm(alias))
        if not code:
            skipped.append(name)
            continue
        lon, lat = centroid(f)
        if lon == 0 and lat == 0:
            skipped.append(name + "(无几何)")
            continue
        # 一个国家可能有多条要素（比如拆分的地块），保留面积更大的那条
        out.setdefault(code, (lat, lon))

    lines = [
        "package geo",
        "",
        "// 本文件由 scripts/gen-country-centroids.py 自动生成，请勿手改。",
        "//",
        "// 数据来源：work/geo/world.json 的国家几何（与地图、陆地掩码同一份），",
        "// 国家码经 internal/flags/names_en.go 映射。质心取**面积最大的多边形**，",
        "// 否则法国、美国这类有海外领地的国家，质心会飘到海里去。",
        "//",
        "// 用途：首页点阵地球需要每台小鸡的经纬度，而 model.Node 只存文本地区，",
        "// 所以按国家码回退到一个近似位置。精确到城市要靠 cityCoords。",
        "",
        "// countryCentroids：ISO 3166-1 alpha-2 -> {纬度, 经度}。",
        "var countryCentroids = map[string][2]float64{",
    ]
    for code in sorted(out):
        lat, lon = out[code]
        lines.append('\t"%s": {%.4f, %.4f},' % (code, lat, lon))
    lines.append("}")
    lines.append("")

    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    io.open(OUT, "w", encoding="utf-8", newline="").write("\n".join(lines))

    print("已生成 %s：%d 个国家" % (os.path.relpath(OUT, ROOT), len(out)))
    if skipped:
        print("跳过 %d 个（无映射或无几何）：%s" % (len(skipped), ", ".join(skipped[:12])))
    return 0


if __name__ == "__main__":
    sys.exit(main())
