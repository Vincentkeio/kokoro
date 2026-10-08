# Kokoro 竞品功能对标矩阵

> 项目：Kokoro（Go 单二进制，Hub + Agent 架构的自托管探针面板）
> 调研日期：2026-10-05
> 调研方式：直接抓取各项目官方文档 / GitHub 仓库原文，不凭记忆推测。**查不到的地方一律写「未确认」**，不做脑补。
> 覆盖竞品：哪吒监控 Nezha Monitoring V2、Komari、ServerStatus-Hotaru；单机面板参照：Dashdot、Glances、Beszel、Netdata。

## 图例

| 符号 | 含义 |
| --- | --- |
| ✅ | 官方资料明确支持 |
| ⚠️ | 部分支持 / 有条件支持 / 需额外配置 |
| ❌ | 官方资料中未见，或项目明确声明不做 |
| ❔ | **未确认**（文档没写清楚，不要当成有） |

---

## 一、功能总表

### 1. 指标采集

| 功能 | 哪吒 | Komari | ServerStatus | 说明 |
| --- | --- | --- | --- | --- |
| CPU 使用率 | ✅ | ✅ | ⚠️ | 哪吒有 `cpu` 告警类型；Komari 上报 `cpu.usage`；Hotaru README 未逐项列指标，仅明确 vnStat 月流量 |
| 内存 | ✅ | ✅ | ⚠️ | Komari 自 Agent 1.1.40 起口径与 htop 一致（cache/buffer 计入可用） |
| Swap | ✅ | ✅ | ❔ | 哪吒有 `swap` 告警类型；Komari 上报 `swap.total/used` |
| 磁盘（容量/已用） | ✅ | ✅ | ⚠️ | Komari 可用 `--include-mountpoint` / `--exclude-mountpoint` 过滤挂载点 |
| 网络速率（实时上下行） | ✅ | ✅ | ✅ | 三者都有实时速率 |
| 网络总量（累计流量） | ✅ | ✅ | ✅ | Hotaru 可选 vnStat 按月统计，否则重启清零 |
| 负载 load1/5/15 | ✅ | ✅ | ⚠️ | 哪吒有 `load1/5/15` 告警类型 |
| 进程数 | ✅ | ✅ | ❔ | 哪吒 `process_count`；Komari 上报 `process` |
| TCP / UDP 连接数 | ✅ | ✅ | ❌ | 哪吒 `tcp_conn_count`/`udp_conn_count`；Komari 上报 `connections.tcp/udp`；哪吒 Agent 可 `skip_connection_count` 关掉 |
| 温度 | ⚠️ | ❌ | ❌ | 哪吒需 Agent 配置 `temperature: true`，且有 `temperature_max` 告警；Komari 上报字段中**无**温度 |
| GPU | ⚠️ | ⚠️ | ❌ | 哪吒需 `gpu: true` 且有 `gpu` 告警类型；Komari **只有 `gpu_name` 字符串字段，无使用率/显存/功耗** |
| 开机时长 uptime | ✅ | ✅ | ✅ | Komari 上报 `uptime`（秒） |
| 系统信息（OS/内核/架构/CPU 型号） | ✅ | ✅ | ⚠️ | Komari `agent.basicInfo` 含 arch、cpu_cores、cpu_physical_cores、cpu_name、os、kernel_version、virtualization、ipv4/ipv6 |
| 磁盘 IO | ❔ | ❌ | ❌ | 三者均未确认。Beszel 明确有 Disk I/O |
| 风扇转速 / 电池 / SMART | ❌ | ❌ | ❌ | Beszel 明确支持温度、风扇、电池、S.M.A.R.T.、ZFS；Glances 可选支持 sensors/SMART/RAID/WiFi |
| Docker 容器级指标 | ❌ | ❌ | ❌ | 三家都没有；Beszel / Glances 有 |

### 2. 网络质量

| 功能 | 哪吒 | Komari | ServerStatus | 说明 |
| --- | --- | --- | --- | --- |
| 到目标的延迟（Ping/TCPing） | ✅ | ✅ | ❌ | 哪吒：ICMP Ping / TCPing / HTTP GET；Komari：Ping 任务 `ping_type` = icmp / tcp / http，value 为毫秒，`-1` 视为丢包 |
| 丢包率 | ✅ | ⚠️ | ❌ | 哪吒网络图表在「只选一个监控项时同时展示延迟和丢包率」；Komari 只能用 `-1` 推断失败 |
| 延迟抖动（jitter） | ❔ | ❌ | ❌ | 未在任何官方文档中确认 |
| 延迟历史图表 | ✅ | ❔ | ❌ | 哪吒：1 天 / 7 天 / 30 天，7/30 天需登录；Komari 有「历史图表」截图但周期未确认 |
| 多节点横向对比（同目标多线路） | ✅ | ❔ | ❌ | 哪吒「覆盖范围 + 特定服务器」可让多个 Agent 打同一目标，图表支持多选对比 |
| 三网（电信/联通/移动）探测 | ❌ | ❌ | ❌ | 三家均无原生支持 |
| 回程路由（traceroute / 去回程） | ❌ | ❌ | ❌ | 三家均无。哪吒的 `networkRoute` 只是公开备注里的**展示字段**（如 AS4837），不是实测 |
| 测速 / speedtest / iperf | ❌ | ❌ | ❌ | 三家均无；dashdot 有静态 speedtest 文件展示 |
| ICMP / TCP / HTTP 探测 | ✅ | ✅ | ❌ | 见上 |
| 从指定 Agent 发起探测 | ✅ | ✅ | ❌ | 哪吒用「覆盖范围」；Komari 下发 `agent.ping` |
| 延迟变化告警（超出阈值区间） | ✅ | ❔ | ❌ | 哪吒可设最高/最低延迟触发通知 |

### 3. 服务与站点监控

| 功能 | 哪吒 | Komari | ServerStatus | 说明 |
| --- | --- | --- | --- | --- |
| HTTP / HTTPS 拨测 | ✅ | ⚠️ | ❌ | 哪吒为 HTTP GET 类型；Komari 的 http ping 存在，但「状态码校验 / 关键字匹配 / 重定向」等细节**未确认** |
| TCP 端口存活 | ✅ | ✅ | ❌ | 哪吒叫 TCPing（host:port） |
| ICMP 存活 | ✅ | ✅ | ❌ | |
| SSL 证书到期 / 变更提醒 | ✅ | ❌ | ❌ | 哪吒：目标为 `https://` 时自动监控证书，到期/变更/过期均触发通知。**Komari 的 Expire/Renew 事件是「服务器到期/续费」，不是 SSL 证书** |
| 可用性统计（在线率） | ✅（30 天） | ❔ | ❌ | 哪吒首页服务面板展示 30 天可用性 |
| 监控项对游客隐藏 | ✅ | ❔ | ❌ | |
| 自定义探测间隔 | ✅（秒） | ❔ | ❌ | |
| 监控项排序 | ✅（数值越大越前） | ❔ | ❌ | |
| 故障/恢复触发任务 | ✅ | ❌ | ❌ | 哪吒可绑定「报警时执行的任务」和「恢复后执行的任务」 |

### 4. 告警

| 功能 | 哪吒 | Komari | ServerStatus | 说明 |
| --- | --- | --- | --- | --- |
| CPU 规则 | ✅ | ❔ | ❌ | 哪吒 `cpu` |
| 内存规则 | ✅ | ❔ | ❌ | 哪吒 `memory` |
| 磁盘规则 | ✅ | ❔ | ❌ | 哪吒 `disk` |
| Swap 规则 | ✅ | ❔ | ❌ | |
| 负载规则 | ✅ | ❔ | ❌ | 哪吒 load1/5/15 |
| 流量规则（周期/月流量） | ✅ | ✅ | ❌ | 哪吒 `transfer_*_cycle` + cycle_start/interval/unit（hour/day/week/month/year）；Komari 有「流量阈值进度条 + 月重置日」与 `traffic` 事件 |
| 实时网速规则 | ✅ | ❔ | ❌ | 哪吒 net_in/out/all_speed |
| 离线规则 | ✅ | ✅ | ❌ | Hotaru README 明确：掉线 TG 通知请改用 NodeStatus |
| 进程数 / 连接数规则 | ✅ | ❔ | ❌ | 哪吒 `process_count`/`tcp_conn_count`/`udp_conn_count` |
| 温度规则 | ✅ | ❌ | ❌ | 哪吒 `temperature_max` |
| 持续时长窗口（防抖） | ✅ | ❔ | ❌ | 哪吒 `duration`（≥3，约 3 秒采样一次，窗口内 >70% 样本越界才告警；离线需全窗口离线） |
| 规则按服务器覆盖/排除 | ✅ | ❔ | ❌ | 哪吒 `cover` + `ignore` |
| 通知触发模式（总是 / 仅一次 / 恢复通知） | ✅ | ✅ | ❌ | 哪吒「总是 / 仅一次」；Komari 事件含 `Online` 与 `Offline` 两种 |
| 告警抑制（去重、静默） | ⚠️ | ❔ | ❌ | 哪吒只有「仅一次」这一种粗粒度抑制，无静默期/聚合 |
| 免打扰时段 | ❔ | ❔ | ❌ | 两家文档均未提 |
| Telegram | ✅ | ✅ | ❌ | 哪吒为 Webhook 示例；Komari 已把内置渠道迁出为**插件**（telegram / email / bark / Server酱³ / Server酱Turbo） |
| 邮件 | ⚠️ | ✅ | ❌ | 哪吒文档示例走 SendCloud 等第三方 API（**原生 SMTP 未确认**）；Komari 有 SMTP 插件（需 `node` 权限） |
| 通用 Webhook | ✅ | ✅ | ❌ | 哪吒：GET/POST + FORM/JSON + 几十个 `#SERVER.xxx#` 占位符；Komari：写 JS `sendMessage/sendEvent` 函数，可自定义任意渠道 |
| 钉钉 / 飞书 / 企业微信 | ⚠️ | ❌ | ❌ | 哪吒提供官方配置示例（本质是 Webhook）；Komari 官方未内置（GitHub issue #162 有人提需求，仍可用 JS 通知自写） |
| Bark / Server 酱 | ✅ | ✅ | ❌ | |
| Slack / Discord / Matrix / Gotify / Pushover | ⚠️ | ❔ | ❌ | 哪吒有 Slack、Matrix、wxpusher 示例；Gotify 出现在 Komari 文档示例 |
| 告警模板自定义 | ✅ | ✅ | ❌ | Komari 支持 `{{event}}`、`{{client}}`、`{{time}}`、`{{message}}`、`{{emoji}}` 模板变量 |
| 通知分组（不同规则发给不同渠道） | ✅ | ❔ | ❌ | 哪吒「通知组」 |
| 新事件类型：登录、到期、续费 | ❌ | ✅ | ❌ | Komari 事件含 `login`、`expire`、`renew`（社区文章称共 11 种事件类型，官方模板文档明确列出 Offline/Online/Alert/Renew/Expire/Test） |

### 5. 可视化

| 功能 | 哪吒 | Komari | ServerStatus | 说明 |
| --- | --- | --- | --- | --- |
| 实时图表 | ✅ | ✅（1 秒级） | ⚠️ | 哪吒 `report_delay` 默认 3 秒；Komari `--interval` 默认 1 秒 |
| 历史曲线（1h / 24h / 7d / 30d） | ✅（实时/1d/7d/30d） | ✅（周期未确认） | ❌ | 哪吒**没有 1h 档**，且历史依赖 TSDB |
| 历史曲线（年 / 自定义区间） | ❌ | ❔ | ❌ | |
| 流量统计与月流量 | ✅ | ✅ | ✅ | Hotaru 靠 vnStat |
| 流量进度条 / 阈值 | ❌ | ✅ | ❌ | Komari 服务器编辑页可设「流量阈值」显示进度条 |
| 在线率 / 可用性 | ✅（服务 30 天） | ❔ | ❌ | |
| 地图 / 地区分布 | ✅ | ❔ | ⚠️ | 哪吒首页有地图（`window.ForceShowMap` 可默认展开）；Hotaru 只有按 `region` 显示国旗（ISO 3166-1，另含 trans/rainbow/pirate 特殊旗） |
| 排序（多字段） | ✅ | ❔ | ⚠️ | 哪吒支持名称/运行时间/系统/CPU/内存/磁盘/上下载速率/总流量，在线优先 |
| 分组展示 | ✅ | ❔ | ❌ | |
| 公开页 / 游客可见 | ✅ | ✅ | ✅ | |
| 卡片布局切换（列表 / 卡片） | ✅ | ❔ | ❌ | 哪吒 `window.ForceCardInline` |
| 账单/套餐等富信息展示 | ✅ | ❔ | ❌ | 哪吒「公开备注」JSON 可展示账单周期、金额、带宽、流量、ASN、线路 |
| 移动端适配 | ✅ | ✅ | ❌ | 哪吒有独立移动端背景图配置；Komari Mochi 主题主打移动 UI |

### 6. 节点管理

| 功能 | 哪吒 | Komari | ServerStatus | 说明 |
| --- | --- | --- | --- | --- |
| 节点上下线展示 | ✅ | ✅（三态：online_ws / online_post / offline） | ✅ | Komari 能区分「完整在线」与「POST 降级在线」，这点比哪吒细 |
| 备注 | ✅（私有备注 + 公开备注） | ❔ | ⚠️ | Hotaru 静态 config 里有 name/location/type |
| 标签（tag） | ❌ | ❔ | ❌ | 哪吒只有「分组」，没有标签 |
| 分组 | ✅ | ❔ | ❌ | |
| 隐藏 / 公开 | ✅（对游客隐藏） | ❔ | ⚠️ | Hotaru config 有 `disabled` 字段 |
| 排序 | ✅ | ❔ | ⚠️ | Hotaru 提到「Toyo 版顺序调整」问题 |
| 批量操作 | ✅（批量编辑配置、批量转移给其他用户） | ❔ | ❌ | |
| 节点详情页 | ✅（`/server/{id}`，含实时/1d/7d/30d + 网络延迟标签页） | ✅ | ❌ | |
| 节点归属 / 所有权 | ✅（每个用户有独立连接密钥，服务器归属用户） | ❔ | ❌ | |

### 7. Agent 管理

| 功能 | 哪吒 | Komari | ServerStatus | 说明 |
| --- | --- | --- | --- | --- |
| 一键安装命令（面板生成，复制即用） | ✅ | ✅ | ⚠️ | Hotaru 有 `status.sh c` 客户端脚本 |
| 版本升级 | ✅（自动更新 + 手动强制更新） | ✅ | ❌ | 哪吒可用 `disable_auto_update` / `disable_force_update` 控制 |
| 远程执行（下发命令） | ✅ | ✅ | ❌ | 哪吒：任务（计划/触发）；Komari：`agent.exec` + `agent.taskResult`（带回 exit_code） |
| 卸载 | ✅（`service uninstall` / `agent.sh uninstall`） | ✅（有 uninstall 文档） | ❌ | |
| 离线检测阈值 | ⚠️ | ✅ | ❔ | 哪吒没有独立阈值，靠告警规则的 `duration`（≈ duration×3 秒）；Komari 有明确公式 `max(3×interval, 30s)` |
| 心跳/上报间隔配置 | ✅（`report_delay`，默认 3 秒） | ✅（`--interval` 默认 1 秒；`--info-report-interval` 默认 5 分钟） | ❌ | |
| 断线重连与降级 | ✅ | ✅ | ❌ | Komari：WS 失败重试 3 次后转 HTTP POST fallback，至少 10 秒探测一次回切，POST 失败指数退避（上限 60 秒） |
| 多架构支持 | ✅（amd64 / arm；Windows、Linux、macOS、群晖 DSM7、OpenWrt 各有安装文档） | ✅（Go 项目，具体架构清单未确认） | ⚠️ | Hotaru：Linux 客户端 + Go 客户端 + psutil 客户端（可跑 Windows） |
| 无 systemd 环境支持 | ✅（OpenWrt 用 procd init.d、Windows 用 service install、群晖手动） | ❔ | ⚠️ | Hotaru 客户端是裸跑 Python 脚本 |
| 自动发现 / 批量接入 | ❌ | ✅（有 `install/agent-ad` 自动发现文档，支持 Docker/脚本/二进制） | ❌ | |
| Agent 能力开关（禁用命令执行 / NAT / 更新等） | ✅（`disable_command_execute`、`disable_force_update`、`disable_nat`、`disable_auto_update`、`skip_procs_count`、`skip_connection_count`、`gpu`、`temperature`、`use_ipv6_country_code` 等） | ❔ | ❌ | 这块哪吒明显最成熟 |
| 指定 UUID 重装继承历史 | ✅（`NZ_UUID`） | ❔ | ❌ | |
| IP 上报周期 | ✅（`ip_report_period`，默认 1800） | ❔ | ❌ | |

### 8. 终端与运维

| 功能 | 哪吒 | Komari | ServerStatus | 说明 |
| --- | --- | --- | --- | --- |
| Web SSH / 在线终端 | ✅（Linux + Windows，`Ctrl+Shift+V` 粘贴；并发限制：单用户 20 条、单服务器 40 条 IOStream） | ✅（`/terminal` 内置独立 WS 通道 `/api/clients/terminal`） | ❌ | |
| 文件管理（浏览 / 上传 / 下载） | ✅（仅 *nix，与终端共用 IOStream 配额） | ❌（文档未见；仓库里「剪切板」功能已被移除） | ❌ | |
| Docker 容器监控 | ❌ | ❌ | ❌ | Beszel / Glances 有 |
| 进程查看（列表，不只是数量） | ❌ | ❌ | ❌ | Netdata / Glances 有 |
| systemd 服务管理 | ❌ | ❌ | ❌ | Netdata 可实时查看 systemd units/services |
| 定时任务 | ✅（计划任务 cron 六段表达式 + 触发任务，可按覆盖范围批量下发，可绑定通知） | ⚠️（插件内可用 `server.cron()`；服务端是否内置未确认） | ❌ | |
| 远程执行结果回传（输出 + 退出码） | ⚠️（文档未明确退出码） | ✅（`result` + `exit_code`） | ❌ | |
| 内网穿透 | ✅（仅明文 HTTP，一个域名映射一个 Agent 内网服务） | ❌ | ❌ | |
| DDNS | ✅（cloudflare / tencentcloud / he / webhook / dummy；IPv4+IPv6；支持 IDN；可自定义公共 DNS；有更新日志） | ❔ | ❌ | |
| IP 变更提醒 | ✅（可配置覆盖范围，默认隐藏完整 IP） | ❔ | ❌ | |

### 9. 账号与权限

| 功能 | 哪吒 | Komari | ServerStatus | 说明 |
| --- | --- | --- | --- | --- |
| 多用户 | ✅（管理员 / 普通用户两级；普通用户只能管自己名下的资源） | ❔ | ❌ | Hotaru 只有 config.json 里一对静态 username/password |
| 细粒度角色（RBAC） | ❌ | ❔ | ❌ | 哪吒只有两级，无自定义角色 |
| 访客只读（公开页） | ✅ | ✅ | ✅ | |
| 2FA | ❔ | ✅（文档有 `faq/disable2fa`，说明存在 2FA） | ❌ | 哪吒官方文档未提及 2FA |
| API Token / PAT | ✅（可设 scope、服务器白名单、过期时间，明文只显示一次，记录最后使用 IP） | ✅ | ❌ | |
| OAuth / OIDC | ✅（多个 OAuth2 提供方；可禁止密码登录；需配置回调域名） | ✅（文档有 GitHub OAuth 配置页） | ❌ | |
| 在线用户查看 / 封禁 | ✅ | ❔ | ❌ | |
| Web 应用防火墙（防爆破） | ✅（按 IP + 标识封禁，计数越多封禁越久） | ❔ | ❌ | |
| 审计日志 | ⚠️（提到「审计记录」依赖真实 IP 头，细节未确认） | ❔ | ❌ | |

### 10. 数据

| 功能 | 哪吒 | Komari | ServerStatus | 说明 |
| --- | --- | --- | --- | --- |
| 存储后端 | SQLite（配置/业务）+ 内存（实时状态）+ 可选内置 TSDB（指标与服务历史） | ❔（未确认） | 无持久化（内存态） | |
| 数据保留策略 | ❔（文档只说启用 TSDB 后存历史，未给保留期） | ❔ | ❌ | 参照：Netdata 默认 1 年可配，0.6 字节/样本 |
| 历史指标需额外开关 | ✅（不启用 TSDB 则前端锁定历史周期） | ❔ | — | |
| 导出（CSV / JSON / Prometheus 等） | ❔ | ❔ | ❌ | 参照：Glances 可导出 CSV/JSON/Influx/ES/Prometheus/Kafka 等十几种 |
| 备份与恢复 | ✅（官方文档有备份恢复页：备份 `/opt/nezha`，迁移前停 Dashboard 保证一致） | ❔ | ❌ | 参照：Beszel 支持自动备份到磁盘或 S3 |
| 迁移 | ✅ | ❔ | ❌ | |
| 压缩 / 存储优化 | ❔ | ✅（v2 协议默认 gzip；指标 zstd 压缩提交记录中有） | ❌ | |

### 11. 其他

| 功能 | 哪吒 | Komari | ServerStatus | 说明 |
| --- | --- | --- | --- | --- |
| IPv6 | ✅（Agent 对接地址支持 IPv6，DDNS 支持 AAAA，`use_ipv6_country_code`） | ✅（上报 ipv6） | ❔ | |
| 反向代理支持 | ✅（Nginx/Caddy，必须同时转发 WebSocket 和 gRPC；有专项排障文档） | ✅（有 nginx 配置 FAQ） | ❌ | |
| 单域名 / 访问域名与通信域名分离 | ✅（`dashboard_host` / `install_host` 分开配） | ❔ | ❌ | |
| 自定义 CSS / JS | ✅（用户前端 + 管理前端分别注入，另有一堆 `window.*` 行为开关） | ✅（自定义头部 / 底部 HTML） | ⚠️（前端需自行改 Vue3 源码打包） | |
| 主题系统 | ✅（内置 + 社区模板，配置文件 `user_template`） | ✅（更强：ZIP 主题包 + `komari-theme.json` + 内置主题市场 + 主题动态配置项 managed/raw/redirect + 语义化 `km-` classname） | ⚠️（hotaru_theme 独立前端仓库） | Komari 的主题生态明显强于哪吒 |
| 插件系统 | ❌ | ✅（JS 插件跑在 goja 沙箱，可注册 HTTP 路由/静态资源、Hook HTTP 与 WebSocket、调用系统 RPC、注册自有 RPC、cron 定时、注入 HTML；带权限审批与插件市场） | ❌ | 这是 Komari 最大的架构优势 |
| 多语言 | ✅（语言设置影响通知与界面） | ✅（zh-CN / en，主题与插件 manifest 均支持多语言对象） | ❔ | |
| 开放 API | ✅（REST API + PAT） | ✅（`/api/public`、`/api/rpc2`、`/api/admin/*`） | ❌ | |
| MCP（给 AI 用） | ✅（`POST /mcp`，可开关） | ❔ | ❌ | 参照：Glances 4.5.1+ 也内置 MCP server |
| Webhook 出站安全限制 | ✅（拒绝私网/本机/保留地址目标，不跟随重定向） | ❔ | — | |
| 联动（流量超额自动关机等） | ⚠️（靠「通知触发任务」自己写命令实现，非内置） | ❌ | ❌ | |
| 服务器到期/续费提醒 | ❌（只有 IP 变更提醒） | ✅（`expire` / `renew` 事件） | ❌ | 这是 Komari 面向「小鸡玩家」的特色 |
| 一键部署到面板/应用商店 | ❔ | ✅（README 明确列出雨云、1Panel 应用商店一键部署） | ❌ | |
| 安装/迁移体验 | ⚠️（Dashboard + Agent 分两个仓库分别发版，版本兼容性需自行对照） | ✅（单仓库单二进制 + 一键脚本 + Docker + 应用商店） | ⚠️（需 make 编译 sergate，前端单独下载） | |

---

## 二、优先级三档划分

> 立场：我们要做的是「比它们都重的**社交化**探针」。
> 判断标准——**P0 必须有**：不做就会被判定为「不完整」；**P1 应该有**：主流有、我们没有会掉分；**P2 可以没有**：锦上添花或可以靠插件/生态补。

### 指标采集

| 功能 | 档位 | 理由 |
| --- | --- | --- |
| CPU / 内存 / swap / 磁盘 / 网络速率 / 网络总量 / 负载 | P0 | 探针的入场券，缺一项就被打成残废 |
| 开机时长 / 系统信息（OS、内核、架构、CPU 型号、虚拟化） | P0 | 公开页「晒配置」的核心信息，社交化必备 |
| 进程数 / TCP·UDP 连接数 | P0 | 哪吒 Komari 都有，是告警规则的输入 |
| 网络速率与总量（含月周期重置） | P0 | 小鸡玩家第一关心指标，直接决定要不要买 |
| GPU（使用率/显存/功耗） | P1 | 哪吒需开关、Komari 只有型号名，我们补齐就是差异点（AI 盒子/独服场景） |
| 温度 | P1 | 同上，且是「风扇/降频」排障依据 |
| 磁盘 IO | P1 | 三家都没有，做了就是降维打击；Beszel 有，说明有需求 |
| 风扇 / 电池 / SMART / ZFS | P2 | 家用 NAS、独服场景才有，一期不做 |

### 网络质量

| 功能 | 档位 | 理由 |
| --- | --- | --- |
| ICMP / TCP / HTTP 探测 + 延迟历史图 | P0 | 哪吒 Komari 都有，没有就等于没有「拨测」 |
| 从指定 Agent 发起、多节点横向对比 | P0 | 这是「多探针」相对 Uptime Kuma 的核心价值 |
| 丢包率统计 | P0 | 哪吒有，Komari 只能靠 -1 推断，我们做实打实的丢包率 |
| 延迟变化告警 | P0 | 哪吒有，属于「线路质量」刚需 |
| 延迟抖动 jitter | P1 | 三家都没确认有，做出来是卖点 |
| 三网（电信/联通/移动）探测 | P1 | 中文小众圈子的高频需求，三家全都没有 |
| 回程路由 traceroute | P1 | 同上，差异化杀伤力大（但依赖目标库与合规，需评估） |
| 测速 / speedtest / iperf | P2 | 资源消耗大、易被滥用，且可交给插件 |

### 服务与站点监控

| 功能 | 档位 | 理由 |
| --- | --- | --- |
| HTTP(S) / TCP / ICMP 三类拨测 | P0 | 三家共识的标配 |
| SSL 证书到期提醒 | P0 | 只有哪吒有，成本极低、感知极强 |
| 可用性在线率（30 天） | P0 | 公开状态页的门面 |
| 自定义探测间隔、排序、对游客隐藏 | P0 | 没有就不好用、不好看 |
| 状态码 / 关键字 / 重定向校验 | P1 | Komari 没确认有，哪吒也没明说，是补全项 |
| 故障/恢复触发任务 | P1 | 哪吒有；属于「可联动」故事的一部分 |
| DNS / WebSocket / Push 等更多拨测类型 | P2 | 属于 Uptime Kuma 的地盘，一期不抢 |

### 告警

| 功能 | 档位 | 理由 |
| --- | --- | --- |
| 规则：离线 / CPU / 内存 / 磁盘 / 流量 / 负载 | P0 | 没有告警的探针没人用 |
| 持续时间窗口（防抖） | P0 | 哪吒有明确机制，没有会告警轰炸 |
| 覆盖/排除指定节点 | P0 | 多节点场景必需 |
| 恢复通知 | P0 | 只报不复会被骂 |
| 通用 Webhook + Telegram + 邮件 | P0 | 三家都支持的最低集合 |
| 钉钉 / 飞书 / 企业微信 / Bark / Server 酱 | P0 | 中文用户刚需，哪吒靠示例覆盖，我们要内置 |
| 通知模板自定义 | P0 | Komari 有、哪吒有（占位符），不做会被模板党嫌弃 |
| 通知分组（不同规则不同渠道） | P1 | 哪吒有，Komari 未确认，属于进阶 |
| Swap / 进程数 / 连接数 / 温度 / 实时网速规则 | P1 | 哪吒全有，属于「更全」的一部分 |
| 告警抑制（静默期、聚合、去重） | P1 | 三家都薄弱，是体验差异点 |
| 免打扰时段 | P1 | 三家文档都没写，低成本高感知 |
| 登录 / 到期 / 续费等非监控类事件 | P1 | Komari 有，契合「小鸡资产管理」叙事 |
| 更多渠道（Slack / Discord / Gotify / Pushover / Matrix） | P2 | 可交给 Webhook + 插件生态 |

### 可视化

| 功能 | 档位 | 理由 |
| --- | --- | --- |
| 实时图表 | P0 | 门面 |
| 历史曲线（24h / 7d / 30d） | P0 | 哪吒 Komari 都有 |
| 流量统计 + 月流量 + 月重置日 | P0 | 小鸡玩家的生命线 |
| 公开页 + 游客可见性控制 | P0 | 社交化的地基 |
| 排序 + 分组 | P0 | 节点一多没有就没法看 |
| 节点详情页 | P0 | 三家共识 |
| 历史曲线（1h / 年 / 自定义区间） | P1 | 哪吒没有 1h 和年，我们补齐就是「更重」 |
| 地图 / 地区分布 | P1 | 哪吒有、Komari 疑似没有；视觉冲击力强，社交化加分 |
| 在线率 | P1 | 哪吒有 |
| 流量进度条 / 阈值 | P1 | Komari 有、哪吒没有，成本低 |
| 账单 / 套餐 / 线路等富信息卡片（公开备注） | P1 | 哪吒有，是「晒小鸡」的核心载体，必须抄并做强 |
| 移动端适配 | P1 | 分享出去的链接大概率在手机打开 |
| 卡片 / 列表布局切换 | P2 | 审美偏好，可交给主题 |

### 节点管理

| 功能 | 档位 | 理由 |
| --- | --- | --- |
| 上下线展示（含降级态） | P0 | 基础；Komari 的三态设计值得直接抄 |
| 隐藏 / 公开 | P0 | 公开页必须有开关 |
| 备注（私有 + 公开） | P0 | 哪吒有，社交化要靠公开备注做展示 |
| 分组 | P0 | 多节点必需 |
| 排序 | P0 | 必需 |
| 批量操作（批量编辑、批量转移） | P1 | 哪吒有，节点多了才用得上 |
| 标签（tag，多对多） | P1 | 三家都没有，比分组灵活，社交化分类利器 |
| 节点归属 / 所有权 | P1 | 多用户前提下的必需项 |
| 节点详情页（含网络延迟标签页） | P0 | 哪吒有 |

### Agent 管理

| 功能 | 档位 | 理由 |
| --- | --- | --- |
| 一键安装命令（面板生成、复制即用） | P0 | 部署体验的第一印象，Komari 的强项 |
| 多架构（amd64 / arm64 / armv7 / riscv） | P0 | 覆盖路由器、盒子、树莓派，Go 的天然优势要吃满 |
| 心跳间隔可配 | P0 | 三家都可配 |
| 离线检测阈值 | P0 | Komari 有明确公式，我们要有可配阈值 |
| 断线重连 + 降级（WS→POST fallback） | P0 | Komari 的状态机设计值得照抄，直接提升「不掉线」口碑 |
| 版本升级（自动 + 强制） | P1 | 哪吒有 |
| 远程执行（含输出与退出码回传） | P1 | 两家都有，Komari 还回传 exit_code |
| 卸载 | P1 | 两家都有 |
| Agent 能力开关（禁命令执行 / 禁 NAT / 跳过连接数等） | P1 | 哪吒最成熟，安全合规必需 |
| 无 systemd 环境支持（OpenWrt / 群晖 / Windows / 容器） | P1 | 覆盖面就是竞争力 |
| 自动发现 / 批量接入 | P1 | Komari 有，哪吒没有 |
| 指定 UUID 重装继承历史 | P2 | 哪吒有，属于运维细节 |
| 挂载点过滤（include/exclude） | P1 | Komari 有，磁盘统计准确性的刚需 |

### 终端与运维

| 功能 | 档位 | 理由 |
| --- | --- | --- |
| Web SSH / 在线终端 | P0 | 两家都有，不做会被认为「不是完整探针」 |
| 定时任务（计划 + 触发） | P1 | 哪吒有，是「运维闭环」的一环 |
| 文件管理 | P1 | 哪吒有（仅 *nix），Komari 没有 |
| Docker 容器监控 | P1 | 三家都没有，Beszel/Glances 有，家用/独服场景需求真实 |
| 进程查看（列表） | P1 | 三家都没有，Netdata/Glances 有，排障刚需 |
| DDNS | P1 | 哪吒有（5 个供应商），家宽用户刚需 |
| IP 变更提醒 | P1 | 哪吒有 |
| 内网穿透 | P2 | 哪吒有但只支持明文 HTTP，安全成本高、受众窄 |
| systemd 服务管理 | P2 | 权限面太大，一期不做 |

### 账号与权限

| 功能 | 档位 | 理由 |
| --- | --- | --- |
| 多用户 | P0 | 社交化 = 多人 + 公开 |
| 访客只读 / 公开页 | P0 | 社交化的入口 |
| API Token / PAT（带 scope 与过期） | P0 | 两家都有，生态与自动化的入口 |
| OAuth | P1 | 两家都有，降低注册门槛 |
| 2FA | P1 | Komari 有，哪吒疑似没有；高权限面板的安全底线 |
| 细粒度角色 RBAC | P1 | 两家都没有，做社交化（多人协作/分享）迟早要 |
| 在线用户查看 / 封禁 | P2 | 哪吒有，面向公开站的防护 |
| Web 应用防火墙（防爆破） | P2 | 公开站才需要，二期 |
| 审计日志 | P1 | 多用户 + 远程执行必须有留痕 |

### 数据

| 功能 | 档位 | 理由 |
| --- | --- | --- |
| 单文件/内嵌存储（零外部依赖） | P0 | Go 单二进制的立身之本 |
| 时序历史（可选启用） | P0 | 哪吒的 TSDB 模式证明了可行性 |
| 数据保留策略（可配天数） | P1 | 三家都没说清楚，我们写清楚就是优势 |
| 备份与恢复（含文档） | P1 | 哪吒有文档，自托管用户必问 |
| 迁移 | P1 | 换机是常态 |
| 导出（CSV / JSON / Prometheus） | P1 | Glances 的生态位，做开放数据的姿态 |
| 存储压缩 | P2 | 优化项，二期 |

### 其他

| 功能 | 档位 | 理由 |
| --- | --- | --- |
| IPv6 | P0 | 两家都支持，小鸡圈 IPv6-only 机器很多 |
| 反向代理支持（WS + gRPC 转发文档） | P0 | 哪吒有专门排障文档，说明这是高频坑 |
| 开放 API | P0 | 主题/插件/生态的前提 |
| 自定义 CSS / JS | P0 | 两家都有，个性化第一步 |
| 主题系统 | P0 | Komari 靠主题生态起飞，社交化必须有 |
| 插件系统 | P1 | Komari 的护城河；但投入大，建议二期做，先留接口 |
| 多语言（中/英） | P1 | 两家都有 |
| 一键部署（Docker / 脚本 / 应用商店） | P1 | Komari 的雨云 + 1Panel 入口是它扩散的关键 |
| 服务器到期 / 续费提醒 | P1 | Komari 有，契合「资产管理」叙事，哪吒没有 |
| MCP（AI 接入） | P2 | 哪吒有、Glances 有，趋势项但非刚需 |
| 联动（流量超额自动关机等） | P2 | 哪吒靠任务间接实现，受众小、风险高 |
| Webhook 出站 SSRF 防护 | P1 | 哪吒有，安全底线 |

---

## 三、竞品的明显短板（我们的差异化点）

以下均基于本次实际读到的官方文档/仓库，不是主观臆测。

1. **主题/外观生态整体偏弱，且没有「内容化」。**
   哪吒的主题是「模板 + 自定义代码片段 + 一堆 `window.*` 全局变量」，定制靠改 JS 全局变量（`CustomLogo`、`ForceShowServices`…），本质是打补丁；Komari 的主题系统明显更强（ZIP 包 + manifest + 主题市场 + 动态配置表单 + 语义化 classname），但**两家都完全没有「内容」**：没有节点主页、没有评测/评论、没有榜单、没有可分享的节点卡片。我们的「社交化」在这块是真空地带。

2. **所有竞品都只做「机器视角」，不做「人的视角」。**
   三家的数据模型都是 machine-centric（节点 → 指标 → 告警）。Komari 是唯一往前走了一步的（有 `expire`/`renew`/`login` 事件，有 region、账单式展示），但也仅此而已。没人做「这台机器是谁的、主人想说什么、别人怎么看」。

3. **网络质量维度集体空白：三网、回程、抖动、测速全没有。**
   哪吒最强也只是「多 Agent 打同一目标 + 延迟/丢包图」，且 `networkRoute` 只是公开备注里的**静态展示字段**（如 AS4837），不是实测。中文用户最关心的三网延迟、回程路由、抖动，三家一个都没做。

4. **指标深度不够，GPU/温度/磁盘 IO/容器/进程全是短板。**
   哪吒 GPU 与温度需要手动开 Agent 开关；Komari 只有 `gpu_name` 一个字符串、**根本没有使用率**；三家的 Docker、进程列表、磁盘 IO、SMART 全部缺失。而 Beszel（容器/SMART/ZFS/风扇/电池）、Netdata（800+ 采集器、systemd units、进程、连接）证明这块是有真实需求的。

5. **告警体验停留在「能动」层面。**
   哪吒只有「总是 / 仅一次」两档，没有静默期、没有聚合、没有免打扰时段；通知渠道本质是「通用 Webhook + 官方示例」，邮件还要靠第三方 API（文档示例是 SendCloud）；Komari 更激进——**把内置通知渠道整个迁出成了插件**（telegram/email/bark/Server酱），等于把刚需功能甩给生态，且仓库自述「暂无持续维护计划」。国内常用的钉钉/飞书/企业微信在 Komari 侧至今是 issue 状态（#162）。

6. **Komari 的文档与可观测性材料严重不足。**
   官方文档站只有「安装 / 开发者（agent、api、theme、plugin、managed-config）/ FAQ」，**没有告警规则页、没有节点管理页、没有指标说明页**；存储后端、数据保留、备份迁移、多用户、角色、DDNS 等一概查不到。这意味着它的功能再强，用户也「用不起来」，同时说明这些能力本身大概率不成熟。

7. **ServerStatus 系已明确放弃演进。**
   Hotaru README 直接写「将会停留在轻量级的 ServerStatus，不会再添加新的功能」，并把低 IO、WebSocket、Docker、Web 管理客户端、掉线 TG 通知等需求全部指向 NodeStatus。它的价值只剩「极简 + 国旗 + vnStat 月流量」，是一个**下限参照物**，不构成威胁。

8. **部署与升级的摩擦仍在。**
   哪吒 Dashboard 与 Agent 是**两个独立仓库分别发版**，需要对照版本兼容性表；反向代理必须同时正确处理 WebSocket 和 gRPC（官方文档为此专门写了排障页）；不启用 TSDB 历史曲线直接被前端锁掉。Komari 是单仓库单二进制 + 一键脚本 + 应用商店，部署体验明显更好——这也说明「装得上、跑得起来」本身就是竞争力，我们要把一键脚本和多架构覆盖做到极致。

9. **可扩展性的天花板。**
   哪吒**没有插件系统**，只有主题和用户自定义代码；ServerStatus 要改前端得自己打包 Vue3。Komari 是唯一有真插件系统的（goja 沙箱 + 路由/Hook/RPC/cron + 权限审批 + 插件市场），这也是它最值得我们警惕和抄的点。

---

## 四、单机面板维度：值得抄的点（Dashdot / Glances / Beszel / Netdata）

| 来源 | 值得抄的点 | 怎么用到 Kokoro |
| --- | --- | --- |
| **Dashdot** | 玻璃拟态（glassmorphism）现代化仪表盘、明暗双主题、Docker 一行起（`docker run -p 80:3001 -v /:/mnt/host:ro --privileged`）、AMD64/ARM 双镜像、可展示静态 speedtest 结果、可关闭后台采集 | 视觉标准要拉到 dashdot 这一档；「首页颜值」是社交化分享的第一转化点；speedtest 结果静态展示是低成本高感知功能 |
| **Glances** | 插件化架构（CPU/内存/网络/磁盘/容器/传感器/SMART/RAID/WiFi/GPU 全是可插拔 extras）、RESTful + XML-RPC + Python API 三套接口、`-w` 一键 Web 模式、服务器自动发现（`--browser`）、十几种导出目标（CSV/JSON/Influx/ES/Prometheus/Kafka/…）、action script 联动、内置 MCP server | 「导出」与「API 优先」是生态位；告警联动（action script）比单纯通知更进一步，可作为我们「联动」功能的参照 |
| **Beszel** | Hub + Agent 架构与 Kokoro 完全一致（参考价值最高）；Docker/Podman 容器级 CPU/内存/网络历史；**S.M.A.R.T. 磁盘健康 + 故障通知**；ZFS 池与数据集；GPU（NVIDIA/AMD/Intel）使用率与功耗；风扇转速、电池电量；多用户（各管各系统，管理员可共享）；OAuth/OIDC（可关密码登录）；**自动备份到磁盘或 S3**；基于 PocketBase、开箱即用 | 容器监控、SMART、ZFS、风扇/电池是三家探针都没有的空白；「自动备份到 S3」是自托管用户的痛点；OAuth + 关密码登录是安全卖点 |
| **Netdata** | 1 秒粒度采集、采集到可视化 1 秒延迟；**零配置自动发现**（800+ 采集器）；每个指标训练 18 个 k-means 模型做无监督异常检测（误报率极低）；400+ 预置告警模板 + 20+ 通知平台；三层时序存储（秒/分/时）0.6 字节/样本、默认保留 1 年；Anomaly Advisor 做根因排序与爆炸半径；边缘存数据（合规友好）；可实时查看 processes / network connections / systemd units | 「免配置的默认告警模板 + 异常检测」是拉开体验差距的方向；三层分分辨率存储是历史曲线性能的标准答案；保留期可配（默认 1 年）正好补齐三家都没说清的短板 |

---

## 五、本次「未确认」清单（不要当真，需二次核实的点）

| 项目 | 未确认项 |
| --- | --- |
| 哪吒 | 2FA、告警静默期/免打扰、数据保留策略、导出能力、磁盘 IO、1h/年历史档、标签系统、原生 SMTP 邮件、一键部署到应用商店 |
| Komari | 告警规则的具体类型与字段、通知渠道完整清单（现为插件）、历史图表周期、在线率、地图、备注/标签/分组/隐藏/批量操作、存储后端与保留策略、备份恢复与迁移、多用户与角色、DDNS、无 systemd 支持、具体架构支持清单、内置定时任务、文件管理、服务端 MCP |
| ServerStatus-Hotaru | 完整指标清单（README 未逐项列出）、IPv6、多语言、数据持久化 |
| 通用 | 三网探测、回程路由、抖动、iperf/测速——**四家探针全部没有**，需自行确认是否作为差异化立项 |

---

## 附：主要资料来源

- 哪吒监控 V2 官方文档：https://nezha.wiki/ （overview、architecture、servers、services、notifications、tasks、agent、ddns、nat、settings、user、profile、comparison）
- 哪吒 Dashboard / Agent 仓库：https://github.com/nezhahq/nezha 、https://github.com/nezhahq/agent
- Komari 仓库与中文 README：https://github.com/komari-monitor/komari
- Komari 文档站：https://www.komari.wiki/ 、https://komari-document.pages.dev/ （dev/agent、dev/theme、dev/plugin、faq/faq、faq/notification-template）
- Komari 通知插件仓库：https://github.com/Akizon77/komari-notification-plugins
- ServerStatus-Hotaru README：https://github.com/cokemine/ServerStatus-Hotaru
- Beszel README：https://github.com/henrygd/beszel
- Dashdot README：https://github.com/MauriceNino/dashdot
- Glances README：https://github.com/nicolargo/glances
- Netdata 官方概览：https://learn.netdata.cloud/docs/welcome-to-netdata/
