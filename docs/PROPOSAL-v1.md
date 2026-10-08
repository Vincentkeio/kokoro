# Kokoro 探针 · 极重路线方案 v1

> 一句话定位：**每台小鸡都有一个自己的主页 —— 它既是监控面板，也是博客、评论区、交易柜台；探针之间能互相串门、互相抄皮肤。**

---

## 一、为什么不走极简

| 维度 | 极简路线（Komari / Nezha 一类） | 极重路线（Kokoro） |
|---|---|---|
| 核心问题 | 机器死没死 | 这台机器值不值、谁在用、能不能聊 |
| 内容 | 一排数字 | 数字 + Markdown 图文介绍 + 跑分 + 入手渠道 |
| 访客 | 自己看 | 陌生人也能看、能评论、能留交易意向 |
| 外观 | 一套内置 UI | 主题市场，看到别人的皮肤一键搬走 |
| 网络 | 单机自托管 | 联邦目录，能翻别人的小鸡 |
| 部署 | 一条 curl | 一条 curl + 主机远程 SSH 批量下发 |
| 数据 | 指标 | 指标 + 内容 + 社交关系 |

极简路线已经有人做得很好（Komari 十几 MB、一键脚本），正面拼"轻"没有胜算。
极重路线的护城河是**内容沉淀 + 皮肤生态 + 网络效应**：机器越多、内容越多、皮肤越多，越难被替代。

---

## 二、环境实测（2026-10-05）

| | DMIT（主机 Hub） | zouter（子机 Agent） |
|---|---|---|
| 地址 | 179.255.97.80 : 4256（密钥） | 216.23.83.149 : 4377（密码） |
| 系统 | Debian 13 x86_64 | Debian 13 x86_64 |
| 规格 | 1 核 / 1973 MB / 20G（**剩 5.4G**） | 1 核 / 967 MB / 15G（剩 12G） |
| 已占端口 | 80、443、888、887、3306、6379、2096、34347 | 4377、2096、3666、53431、35590 |
| 防火墙 | — | ufw 只限入站，**出站全开** |

链路实测：zouter → DMIT `ping` 平均 **98.9ms**，443 可达（nginx 302），GitHub 45ms。

结论与约束：

1. **agent 主动外连**是唯一正确姿态 —— 子机不需要开任何入站端口，ufw/安全组零改动。
2. DMIT 的 443 已被 Nginx 占用 → Hub **不直接抢 443**，监听 `127.0.0.1:8799`，由 Nginx 加 vhost 反代（也可后续切子域名）。
3. DMIT 磁盘只剩 5.4G → **绝不在服务器上编译**，只在本地 Windows 交叉编译 `GOOS=linux`，scp 一个二进制上去（~13MB）。装 Go 工具链要吃掉 1G+。
4. 本地目前**没有 Go / Rust / UPX**，需要先装（见第十二节）。

---

## 三、技术选型：Go

| 候选 | 结论 |
|---|---|
| **Go** | ✅ 选它 |
| Rust | ❌ 体积能更小，但 SQLite / TLS / ACME 生态与交叉编译成本高，开发速度慢 3-5 倍，后期别人写主题也更难 |

选 Go 的硬理由：

- `GOOS=linux GOARCH=amd64/arm64` 一条命令出包，**零交叉编译工具链**，本地 Windows 直接产出。
- SQLite 有成熟的**纯 Go 驱动**（`modernc.org/sqlite`，无 cgo），备份就是拷一个文件。
- `crypto/tls` + `golang.org/x/crypto/acme/autocert` 内置，ACME 自动证书不用外部依赖。
- `go:embed` 把前端塞进二进制，真正做到"一个文件扔上去就能跑"。
- `golang.org/x/crypto/ssh` 直接实现"主机远程下发装子机"。
- 生态位正确：整个探针圈子都是 Go，未来别人贡献主题/插件门槛最低。

### 关键库清单

| 用途 | 选型 | 备注 |
|---|---|---|
| HTTP | `net/http`（Go 1.22+ 路由） + `chi` | 不引重框架 |
| DB | `modernc.org/sqlite` | 纯 Go，无 cgo |
| 前端 | `html/template` + HTMX(内联) + 自绘 SVG 图表 | 零 CDN 依赖，离线可用 |
| 实时 | SSE（浏览器）／HTTPS 轮询或 WS（agent） | 见第五节 |
| TLS | `crypto/tls` + `autocert` | 内置 ACME |
| 远程部署 | `golang.org/x/crypto/ssh` | Hub 直连子机 |
| Markdown | `goldmark` + `bluemonday` 消毒 | 内容安全必须 |
| 指标采集 | `gopsutil`（可裁剪） | 或自读 `/proc`，更省体积 |

### 双制品策略（为了"体积小"）

同一份代码，两个 build target：

| 制品 | 内含 | 预估体积（`-ldflags "-s -w"` 后） |
|---|---|---|
| `kokoro`（Hub） | Web 前端 + SQLite + 主题引擎 + SSH 客户端 | ~13 MB（UPX 后 ~5 MB） |
| `kokoro-agent`（子机） | 只有采集 + 上报 + TLS | ~5 MB（UPX 后 ~2 MB） |

agent 会被装到成百上千台小鸡上，它对体积和内存最敏感，单独瘦身收益最大。
默认仍提供**单一 `kokoro` 二进制跑两种模式**（`kokoro serve` / `kokoro agent`），安装脚本按角色挑对应制品下载。

---

## 四、数据模型（SQLite）

```
nodes          小鸡主表：uuid, name, slug, token_hash, owner_id,
               region, provider, tags, visibility(public/unlisted/private),
               last_seen, status, created_at
metrics_raw    原始指标：node_id, ts, cpu, mem, swap, disk, net_up, net_down,
               load, uptime, procs, tcp, udp, temp      → 保留 24h
metrics_5m     聚合指标：node_id, bucket, avg/max/min   → 保留 90 天
node_profile   博客内容：markdown, cover, specs(json: CPU/带宽/价格/到期/机房/线路),
               album, custom_fields, pv, uv
posts          可选进阶：小鸡主人的多篇文章
comments       node_id, parent_id, author, contact(仅主人可见), content,
               status(pending/approved/spam), ip_hash, ua_hash, created_at
trade_offers   node_id, from_name, contact_type(tg/email/qq/wechat), contact_value,
               price_offer, message, status(new/read/accepted/declined), created_at
themes         id, name, author, version, manifest(json), css, checksum, enabled, source_url
users / sessions / api_tokens / audit_log
```

时序数据用"按天分区表 + 定时 prune"控制体积，1C1G 的机器上带 50+ 节点没有压力。

---

## 五、通信：HTTPS 怎么落地

三种模式，自动选择：

| 模式 | 触发条件 | 做法 |
|---|---|---|
| **A. 真证书** | Hub 有域名且 80/443 可用 | 内置 ACME（Let's Encrypt）自动签发续期 |
| **B. 反代模式** | 已有 Nginx/宝塔（DMIT 就是这个情况） | Hub 只听 `127.0.0.1:8799`，Nginx 终止 TLS，Hub 读 `X-Forwarded-For` |
| **C. 自签 CA** | 纯 IP、无域名 | Hub 启动时自建 CA 并签服务器证书；agent 安装时**同时写入 CA 的 SPKI 指纹并 pin**，杜绝自签被中间人 |

认证与传输：

- 每台小鸡一个独立 **node token**（注册时由 Hub 签发，可轮换、可吊销），上报带 `Authorization: Bearer <token>`。
- 可选 **mTLS**：对高安全机器给客户端证书，Hub 校验 CN。
- agent → Hub：`POST /api/v1/report`（默认 2s 一次，可配 1-10s）；可选升级为 WebSocket 长连接（省握手、可下发命令）。
- Hub → 浏览器：**SSE** 推送实时指标，页面不刷新就跳动。
- 断线：指数退避重连 + 本地缓冲补报，机器重启后自动续传。

> 这条链路的设计要点：**子机永远只做 outbound**，任何 NAT、任何 ufw 后面都能装。

---

## 六、一键部署：最终形态

### 最短命令（Hub 后台直接生成，复制即走）

```bash
curl -fsSL https://kokoro.example.com/i/nk_XXXXXXXX | sh
```

等价显式写法：

```bash
curl -fsSL https://kokoro.example.com/install.sh | sh -s -- \
  --hub https://kokoro.example.com --token nk_XXXXXXXX
```

### 安装脚本要干的事（纯 POSIX sh，< 200 行）

1. 探测 OS / 架构（amd64 / arm64 / armv7）与 init（systemd / openrc / 无 init 退化 nohup）
2. 下载对应制品（优先从 **Hub 自己的 `/dl/`** 拉，避免 GitHub 抽风；支持 `GH_PROXY` 镜像环境变量）
3. 校验 sha256，失败即退出不残留
4. 写入 `/etc/kokoro/config.toml`（hub 地址、token、CA 指纹、上报间隔）
5. 注册 systemd unit、`systemctl enable --now kokoro-agent`
6. 回连 Hub 报到 —— **后台页面那台机器自动亮起来**
7. 幂等：重复执行 = 升级 / 修复；`--uninstall` 一键卸载干净

### "主机直接部署子机节点"（第二条路）

Hub 后台填：IP、端口、用户、密码或私钥 → 点"部署" → Hub 内建 SSH 客户端上去执行上面同一套流程，不用去子机上敲任何命令。
支持 CSV 批量粘贴（一次装十几台）。凭据默认**用完即弃不落盘**，可选加密保存。

---

## 七、小鸡详情页（博客化）

路由：`/n/<slug>`

页面结构：

1. **头部**：机器名、地区国旗、在线徽章、uptime、标签（如"三网直连 / 年付 12 刀"）
2. **实时图表**：CPU / 内存 / 磁盘 / 上下行流量，1h / 24h / 7d 切换（自绘 SVG，零依赖）
3. **主人写的介绍**：Markdown 正文 —— CPU 型号、线路、带宽、价格、入手渠道、跑分截图、使用心得
4. **相册 / 封面图**：本地存储或外链
5. **评论区**：树形回复、可匿名留昵称、可选审核后显示
6. **「我想交易」按钮** → 弹窗：出价 + 联系方式类型（TG / 邮箱 / QQ / 微信）+ 留言 → 进主人的收件箱 + 可选邮件 / TG 通知

后台支持在线 Markdown 编辑 + 预览、自定义字段、PV/UV 统计、分享卡片（OG 图服务端生成）。

---

## 八、主题皮肤系统

### 主题包格式（`.kokoro-theme` = 一个 tar.gz）

```
theme.json      manifest: id, name, version, author, homepage, preview,
                          tokens{--bg,--fg,--accent,...}, layout{列表样式/图表样式/是否大图头图},
                          checksum, 可选 signature
theme.css       覆盖变量 + 自定义样式
overrides/      可选模板片段（home.tmpl / node.tmpl / list.tmpl）—— 需开"高级模式"
assets/         图片、字体、背景图
```

### 一键获取的三种来源

1. **官方/社区主题源**：一个 JSON 索引，后台浏览缩略图 → 点"应用" → 3 秒换肤
2. **抄别人的**（你最想要的这条）：浏览任意 Kokoro 站点 → 点"获取此皮肤" →
   - 路线①（推荐，最稳）：下载 `.kokoro-theme` → 拖进自己后台 → 一键应用
   - 路线②（增强）：填自己的 Hub 地址 + 一次性安装令牌 → 对方直接把主题推过来安装
3. **手动上传**本地主题包

### 安全（主题本质是代码，必须设防）

- 默认**沙箱模式**：只允许 CSS 变量与类名覆盖，**不允许执行 JS**
- CSS 消毒：禁 `javascript:`、禁任意域 `@import`（走白名单）
- `overrides/` 模板覆盖需管理员显式开"高级模式"，并展示改动 diff
- 安装前校验 sha256 + 作者签名；**任何主题都能一键回滚**，且保留"上一个可用皮肤"快照

内置 4-5 套皮肤开服即有货（默认 / 赛博 / 终端绿 / 纸质 / 暗色卡片）。

---

## 九、让别人能"翻看你的小鸡"

- **目录站（先做）**：Hub 勾选"公开到目录"，向公共索引站心跳上报机器名、地区、配置摘要、在线率、主页 URL。
  **不上传 IP、不上传流量细节**。目录站只做索引和跳转，真实数据始终在各自 Hub 上。
- **友邻互推（后做）**：Hub A 把 Hub B 加为友邻，首页展示邻站小鸡卡片，纯 RSS/JSON Feed 订阅，不引 ActivityPub 那么重的协议。

---

## 十、安全与合规护栏

技术侧：

- 全站 HTTPS + HSTS；Cookie `HttpOnly/Secure/SameSite`
- 每台小鸡独立 token，可随时吊销轮换
- 管理后台：密码 + TOTP + 登录失败限流 + 可选 IP 白名单
- 评论/交易表单：CSRF token、速率限制、内容长度限制、外链数量限制、可选先审后发
- 联系方式**默认只有主人可见**，公开页只显示"已收到 N 条意向"，防爬防骚扰
- `kokoro backup` 一条命令导出整个库 + 上传目录为单文件，可定时任务异地备份

内容侧（平台规则，写进 ToS 与后台提示）：

- 平台只提供**展示与意向留言**，不参与撮合、不托管资金、不做担保，交易风险由双方自负
- 禁止发布任何违法违规内容，违规内容可被下架
- 用户对自己发布的内容负责，站长可一键关闭评论/交易模块

---

## 十一、性能与体积预算

| 指标 | 目标 |
|---|---|
| Hub 二进制 | ≤ 13 MB（UPX ≤ 5 MB） |
| Agent 二进制 | ≤ 5 MB（UPX ≤ 2 MB） |
| Agent 内存 RSS | ≤ 15 MB |
| Agent CPU | < 0.5%（1 核小机） |
| 1C1G 带节点数 | ≥ 50 |
| 冷启动 | < 200ms |
| 外部依赖 | 0（不需要 MySQL / Redis / Nginx） |

---

## 十二、分期路线

| 阶段 | 目标 | 产出 |
|---|---|---|
| **M0** | 骨架跑通 | Go 双模式二进制、agent 采集上报、Hub 存 SQLite、列表页 + 实时图表、HTTPS、一键安装脚本。**DMIT 装 Hub、zouter 装 agent，端到端跑通** |
| **M1** | 内容化 | 小鸡 Markdown 主页、封面/相册、地区标签、公开/私有、自定义 slug、后台编辑器 |
| **M2** | 社交化 | 评论（审核 + 反垃圾）、「我想交易」表单、主人收件箱、通知 |
| **M3** | 皮肤化 | 主题包格式、内置皮肤、一键获取/导入/回滚、主题预览与沙箱 |
| **M4** | 网络化 | 目录站、友邻订阅、主题仓库、RSS |
| **M5** | 打磨 | 备份恢复、多用户、开放 API、移动端、文档站 |

建议节奏：M0 先花几天做出能跑的东西并**真的装到那两台机器上**，验证链路后再决定后续投入。

---

## 十三、这两台机器怎么用于测试

**DMIT（Hub）**

- Hub 监听 `127.0.0.1:8799`，Nginx 加 vhost 反代（新子域名或复用已有域名的子路径）
- 改 Nginx 前先备份 conf；443 已有证书，直接复用
- 只 scp 二进制，不在机器上编译

**zouter（Agent）**

- 跑真实一键脚本，验证：arch 探测、systemd 自启、断线重连、CA 指纹校验、跨国 HTTPS 上报延迟
- 因为是 outbound，ufw 一行都不用加

**第三个对照点**：本机 Windows / WSL 再挂一个 agent，验证异构环境安装脚本。

---

## 十四、需要 boss 拍板的决策点

1. **项目名与域名**：继续叫 Kokoro（こころ / 心探）？Hub 用哪个域名？
2. **语言**：Go（推荐）还是坚持 Rust？
3. **存储**：SQLite 单文件（推荐）还是预留 MySQL 选项？
4. **交易模块边界**：只做"意向留言"（推荐，合规最稳）还是要做报价/还价/站内消息闭环？
5. **主题自由度**：只放 CSS 变量（安全）还是允许模板覆盖（自由但需审核）？
6. **是否做公共目录站**：需要一台常驻服务器 + 域名，是现在做还是放到 M4？
7. **要不要现在装 Go 工具链**：本地没装 Go，装了就能开始写代码。
