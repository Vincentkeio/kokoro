# 回程线路显示字典

卡片的 V4、V6 独立读取已采集的路径，扫描完整内容。按下表从上到下查找，首个命中项就是卡片唯一显示的线路名；不显示中转链。全部未命中时，使用第一段。仅有未知诊断且没有匹配项时显示「未知」。

这是产品的显示优先级，不代表线路质量评级。要调整优先级，直接移动 `internal/hub/route_dictionary.go` 中 `routeNamePriority` 的条目。

| 顺序 | 显示名称 | 主要匹配依据 |
| --- | --- | --- |
| 1 | CN2 GIA | 路径明确写明 CN2 GIA |
| 2 | CTG GIA | 路径明确写明 CTG GIA / CTGNet GIA |
| 3 | CN2 GT | 路径明确写明 CN2 GT |
| 4 | CMIN2 | CMIN2 / AS58807 |
| 5 | 9929 | 9929 / CUII / AS9929 |
| 6 | CN2（GT/GIA未确认） | 路径明确标记未确认的 CN2 |
| 7 | CTGNet | CTGNet / AS23764 |
| 8 | CN2 | CN2 / AS4809 |
| 9 | 163 | 163 / ChinaNet / AS4134 |
| 10 | NTT | NTT / AS2914 |
| 11 | 4837 | 4837 / China169 / AS4837 |
| 12 | CMI | CMI / AS58453 |
| 13 | CMNET | CMNET / AS9808 |
| 14 | CUG | CUG / 联通国际 / AS10099 |
| 15 | CERNET | CERNET / AS4538 |
| 16 | CSTNET | CSTNET / AS7497 |
| 17 | Softbank | SoftBank / AS17676 |
| 18 | IIJ | IIJ / AS2497 |
| 19 | PCCW | PCCW / Console Connect / AS3491 |
| 20 | Telstra | Telstra / AS4637 |
| 21 | Tata | Tata / AS6453 |
| 22 | Arelion | Arelion / Telia / AS1299 |
| 23 | GTT | GTT / AS3257 |
| 24 | Level3 | Level3 / Lumen / CenturyLink / AS3356 |
| 25 | Cogent | Cogent / AS174 |
| 26 | HE | HE / Hurricane Electric / AS6939 |
| 27 | Telxius | Telxius / Telefonica / AS12956 |
| 28 | Orange | Orange / OpenTransit / AS5511 |
| 29 | Zayo | Zayo / AboveNet / AS6461 |
| 30 | Verizon | Verizon / UUNET / AS701 |

例如 `Level3 → CMI` 显示 `CMI`，`Softbank → 电信 163（AS4134）` 显示 `163`，`NTT → 联通 4837` 显示 `NTT`。改变中转段先后顺序不会改变字典的优先级。未知运营商之间的路径则只显示第一段。

ASN 匹配有数字边界，AS1740 不会被误认为 AS174。仅 AS4809 不足以判定 GIA/GT，仅 AS23764 不足以判定 GIA；这些细分名称必须由测试报告明确给出。

名称、优先级和颜色分类维护在同一字典中：CN2 / CTG 家族为金色，9929 为紫色，CMIN2 为青色，其余已识别线路为中性灰色；未知和不可用为空值样式。颜色用于区分线路家族，不是实时质量评分，也不额外推断 GIA / GT。

## 核对来源（2026-10-17）

- [中国电信官方资料：CN2 AS4809、CTGNet AS23764](https://www.chinatelecomasiapacific.com/wp-content/uploads/2021/11/2-China-Telecom-x-GIS.pdf)
- [中国移动国际官方资料：CMI AS58453、CMIN2 AS58807](https://mainwebapi.cmi.chinamobile.com/uploads/template/20251216272c9a10af6d23e3b7c459e04e91bf4b.pdf)
- PeeringDB 网络自报资料：[ChinaNet AS4134](https://www.peeringdb.com/net?asn=4134)、[NTT AS2914](https://www.peeringdb.com/asn/2914)、[联通 AS4837](https://www.peeringdb.com/asn/4837)、[AS9929](https://www.peeringdb.com/asn/9929)、[CUG AS10099](https://www.peeringdb.com/asn/10099)、[CTGNet AS23764](https://www.peeringdb.com/asn/23764)、[SoftBank AS17676](https://www.peeringdb.com/asn/17676)、[IIJ AS2497](https://www.peeringdb.com/asn/2497)、[Lumen/Level3 AS3356](https://www.peeringdb.com/asn/3356)、[Zayo AS6461](https://www.peeringdb.com/asn/6461)、[Verizon AS701](https://www.peeringdb.com/asn/701)。
- 其余国际中转 ASN 延续现有 `scripts/kokoro-bench.sh` 的已观测中转映射，并统一展示别名；不根据目的运营商猜测路径里未出现的网络。
