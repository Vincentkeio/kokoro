#!/usr/bin/env python3
"""同步三网探测目标：从 spiritLHLS/icmp_targets 拉「可用 ICMP 地址」表。

为什么用它：我们自己维护的省级 DNS 表很多地址已经不通了（boss 2026-10-11
反馈），而这个仓库是「闭源爬虫解析 CN 全国三大运营商**可用 ICMP** 的 Ping
地址，每日更新」——正好解决"探测点不通"。

用法：
    python scripts/sync-icmp-targets.py            # 拉最新并写入 internal/netprobe/icmp_targets.json
    python scripts/sync-icmp-targets.py --check    # 只校验已有文件是否过期

写入的文件由 Go 侧用 go:embed 读入（internal/netprobe/targets.go），
所以**同步 = 覆盖这一个 JSON**，解析逻辑在 Go 里（有测试）。
"""

import argparse
import datetime as dt
import json
import sys
import urllib.request

SRC = "https://raw.githubusercontent.com/spiritLHLS/icmp_targets/main/nodes.json"
OUT = "internal/netprobe/icmp_targets.json"

# 上游用全称（河北省 / 内蒙古自治区 / 北京市…），我们全站用简称。
# 显示、设置项、分组标签都依赖这个简称，所以在这里统一映射。
SHORT = {
    "北京市": "北京", "天津市": "天津", "上海市": "上海", "重庆市": "重庆",
    "河北省": "河北", "山西省": "山西", "辽宁省": "辽宁", "吉林省": "吉林",
    "黑龙江省": "黑龙江", "江苏省": "江苏", "浙江省": "浙江", "安徽省": "安徽",
    "福建省": "福建", "江西省": "江西", "山东省": "山东", "河南省": "河南",
    "湖北省": "湖北", "湖南省": "湖南", "广东省": "广东", "海南省": "海南",
    "四川省": "四川", "贵州省": "贵州", "云南省": "云南", "陕西省": "陕西",
    "甘肃省": "甘肃", "青海省": "青海", "内蒙古自治区": "内蒙古",
    "广西壮族自治区": "广西", "西藏自治区": "西藏",
    "宁夏回族自治区": "宁夏", "新疆维吾尔自治区": "新疆",
}

# 上游的运营商代码 → 我们的显示名（跟现有 targets.go 保持一致）
ISP_NAME = {"ct": "电信", "cu": "联通", "cm": "移动"}
ISP_ORDER = ["ct", "cu", "cm"]


def fetch(use_proxy: bool = True) -> list:
    # 默认沿用环境里的代理设置（开发机常常直连不通 GitHub），
    # 服务器/CI 上直连即可 —— 用 --no-proxy 显式直连。
    if use_proxy:
        opener = urllib.request.build_opener()
    else:
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    req = urllib.request.Request(SRC, headers={"User-Agent": "kokoro-sync/1"})
    with opener.open(req, timeout=60) as r:
        return json.load(r)


def build(raw: list) -> dict:
    """按 (省, 运营商) 归并 v4 地址，按质量/延迟排序，最多留 3 个候选。"""
    groups: dict[tuple[str, str], list[dict]] = {}
    for row in raw:
        if row.get("ip_version") != "v4":
            continue
        prov = SHORT.get(str(row.get("province", "")).strip())
        isp = str(row.get("isp_code", "")).strip()
        if not prov or isp not in ISP_NAME:
            continue
        ips = [x.strip() for x in str(row.get("ips", "")).split(",") if x.strip()]
        lats = str(row.get("ips_latency_ms", "")).split(",")
        cand = []
        for i, ip in enumerate(ips):
            lat = ""
            if i < len(lats):
                lat = lats[i].strip()
            cand.append({"ip": ip, "latency_ms": lat})
        groups.setdefault((prov, isp), []).extend(cand)

    targets = []
    for (prov, isp), cands in groups.items():
        # 去重保序：同一 IP 可能在多行里出现
        seen, uniq = set(), []
        for c in cands:
            if c["ip"] in seen:
                continue
            seen.add(c["ip"])
            uniq.append(c)
        # 有延迟的排前面（上游的延迟是它自己的观测点，只当"优选"信号用）
        uniq.sort(key=lambda c: (c["latency_ms"] == "", float(c["latency_ms"] or 1e9)))
        targets.append({
            "province": prov,
            "isp": ISP_NAME[isp],
            "ips": [c["ip"] for c in uniq[:3]],
        })

    # 省份按上游出现顺序之外的稳定顺序排列：先直辖市再按拼音？这里按
    # (运营商顺序, 省份) 由 Go 侧再排，保持文件可 diff 即可。
    targets.sort(key=lambda t: (t["province"], ISP_ORDER.index(
        next(k for k, v in ISP_NAME.items() if v == t["isp"]))))
    return {
        "_meta": {
            "source": SRC,
            "project": "https://github.com/spiritLHLS/icmp_targets",
            "why": "闭源爬虫解析 CN 全国三大运营商可用 ICMP 的 Ping 地址，每日更新",
            "fetched_at": dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
            "note": "ips 按上游观测延迟优选排序；Go 侧探测时依次回退，第一个通的就用",
        },
        "targets": targets,
    }


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--check", action="store_true", help="只校验已有文件")
    ap.add_argument("--no-proxy", action="store_true", help="直连，不走环境代理")
    args = ap.parse_args()

    if args.check:
        try:
            with open(OUT, encoding="utf-8") as f:
                d = json.load(f)
        except FileNotFoundError:
            print(f"缺少 {OUT}，先跑一次同步")
            return 1
        print(f"{OUT}: {len(d.get('targets', []))} 组，抓取于 {d['_meta']['fetched_at']}")
        return 0

    data = build(fetch(use_proxy=not args.no_proxy))
    with open(OUT, "w", encoding="utf-8") as f:
        json.dump(data, f, ensure_ascii=False, indent=1, sort_keys=False)
        f.write("\n")
    print(f"写入 {OUT}: {len(data['targets'])} 组（省 × 网）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
