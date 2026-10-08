# Kokoro 加料方案 v2

> 前置：`docs/FEATURE-MATRIX.md` 已经做过一轮 11 大类、三档优先级的对标。
> 这份不重复它，只回答两个具体问题：
> **① 地球那一行左边空出来的位置放什么？② 还有哪些信息/功能能加进来？**
> 立场：极重路线 —— 信息密度越高越好，不追求简洁。

---

## 一、地球那一行左边放什么（boss 直接问的）

那一行右边是点阵地球（约 46% 宽），左边现在是「最新留言」。三个候选：

| 候选 | 放什么 | 信息密度 | 实现难度 | 评价 |
| --- | --- | --- | --- | --- |
| **A. 实时动态流** | 上线/掉线、上报、告警、留言、新文章的混合时间线 | 高 | 低 | **推荐** |
| B. 状态时间轴 | 每台小鸡一行「一天一格」的成功/失败条（Uptime Kuma 那种） | 高 | 中 | 值得，但更适合放详情页 |
| C. 延迟矩阵 | N×N 的节点两两延迟表 + 颜色深浅 | 极高 | 高 | 节点少时没意义，等机器多了再上 |

**推荐 A（实时动态流）**，理由：

1. **只有它天然是"活的"**。地球在动、这个流也在动，两块凑一起才有"监控大屏"的感觉；
   静态表格放旁边会很死。
2. **数据全都现成**：告警事件（`ListAlertEvents`）、留言（`ListRecentComments`）、
   上线/掉线（SSE 已经在推）、新文章（`node_profile`）—— 不需要新采集。
3. **能顺势把"互动"带出来**：留言进流里，访客看到别人在聊，才会跟着聊。

**具体设计**（一条动态 = 图标 + 一句话 + 相对时间）：

```
● 东京 zouter 上线了            2 分钟前
💬 阿宝 在 东京 zouter 留言：这台延迟真稳   8 分钟前
⚠ 洛杉矶 dmit CPU 连续 5 分钟 > 90%      21 分钟前
📄 站长给 东京 zouter 更新了文章        1 小时前
● 香港 hk-01 掉线了                3 小时前
```

流本身走 SSE 增量推，首屏 SSR 出最近 8 条。**不做分页**——"最新动态"给最近的就够了，
看历史去后台。

---

## 二、首页还能加什么（按性价比排序）

性价比 = 信息增益 ÷ 实现难度。**加粗的是我建议先做的。**

### 第一档：数据现成，加个区块就行

| 区块 | 展示什么 | 数据来源 |
| --- | --- | --- |
| **流量 TOP 榜** | 按今日/本月流量排序的横条榜，带占比 | `TrafficSince` 已有 |
| **延迟排行** | 按三网平均延迟排序，好/中/差上色 | `netq` 已有 |
| **最新文章** | 最近更新的 N 篇文章（标题 + 摘要 + 机器名） | `node_profile` 已有 |
| **在线率** | 每台小鸡近 7/30 天的在线百分比（99.9% 这种数字最直观） | `SetNodeOnline` 的历史，需要补一张状态表 |
| **地区分布** | 按国家/地区分组的机器数 + 旗帜 | `Node.Country` 已有 |
| **探测点状态** | 现在 103 个探测点，但首页只显示了一个数字 | `netprobe` 已有 |

### 第二档：要补一点采集或存储

| 区块 | 展示什么 | 代价 |
| --- | --- | --- |
| **状态时间轴** | 一天一格的成功/失败条，鼠标悬停看当天可用率 | 新增 `node_status_daily` 表（按天聚合，很省） |
| **硬件跑分** | CPU 型号 + Geekbench/YABS 分数、硬盘 IO | Agent 要跑一次跑分（可以做成后台点一下才跑） |
| **IP 信息** | 原生 IP / 流媒体解锁 / 风险分 | 要调第三方接口，做成后台手动刷新 |
| **到期提醒** | 每台机器的到期日 + 剩余天数（临近变红） | `node_profile.expire_at` 已有，只差展示 |
| **机房信息** | 机房名 / 测试 IP / 测速文件地址 / Looking Glass | 后台加几个字段 |

### 第三档：重，但差异化明显

| 区块 | 说明 |
| --- | --- |
| 路由追踪展示 | 三网回程线路（CN2 GIA / 9929 / 4837）的路径图，买家最看这个 |
| 交易/转让区 | 项目里**已经有 `trade_offers` 表和 `AddTradeOffer`/`UpdateTradeStatus`**，只是没做成界面 |
| 历史故障记录 | 每次掉线的起止时间，做成"故障公告"列表 |
| 库存/开售 | 有货几台、下一批开售倒计时 |

---

## 三、单机详情页还能加什么

现在详情页有：实时指标、4 张曲线、三网分省表、文章。建议补：

1. **状态时间轴**（30 天可用率）—— 买家判断"稳不稳"最快的办法
2. **流量日历** —— 一个月每天用了多少，热力图形式
3. **IP / 线路信息卡** —— 测试 IP、测速文件、Looking Glass、回程线路类型
4. **同机房其他机器** —— 横向对比，也顺手做了内链
5. **留言区升级**：现在是平铺列表，建议支持**楼中楼**（`Comment.ParentID` 字段已经在了，只是没用）
6. **文章列表** —— 详见第五节（要改成一篇 → 多篇）

---

## 四、后台还能加什么

| 功能 | 说明 |
| --- | --- |
| **批量下发命令** | 路由 `/api/v1/command/result` 已经在了，但**没有下发入口**——补一个后台页面就能用 |
| **告警规则界面** | `SaveAlertRule` / `ListAlertRules` 已有，确认后台是否已暴露 |
| **通知渠道** | 项目里有 `internal/notify`，确认支持哪些（Telegram / Bark / Webhook） |
| **审计日志** | `AddAudit` 已经在写，但没有查看界面 |
| **数据导出** | 导出某台机器的历史指标为 CSV |
| **用户/权限** | `CreateUser` / `GetUserByName` 已有，说明多用户是预留了的 |

---

## 五、去掉「一台小鸡一篇文章」的限制（boss 已提）

现状：`node_profile.content_md` 是**一列**，所以物理上只能一篇。

**改法**：新建 `node_posts` 表，`content_md` 迁过去当第一篇，保留字段做兼容期回退。

```sql
CREATE TABLE node_posts (
  id         TEXT PRIMARY KEY,
  node_id    TEXT NOT NULL,
  title      TEXT NOT NULL DEFAULT '',
  slug       TEXT NOT NULL,          -- /n/<node>/p/<slug>
  content_md TEXT NOT NULL DEFAULT '',
  summary    TEXT NOT NULL DEFAULT '',
  published  INTEGER NOT NULL DEFAULT 1,
  pinned     INTEGER NOT NULL DEFAULT 0,
  views      INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX idx_node_posts_node ON node_posts(node_id, pinned DESC, created_at DESC);
```

配套：后台从「一篇编辑器」改成**列表 + 新建/编辑/删除**；
详情页显示文章列表；首页「最新文章」直接查这张表。

⚠️ 迁移要点：`node_profile.content_md` 非空的行，迁成一篇 title 为空的文章；
**迁移后不要删原列**，先留着做回滚兜底，等稳定一个版本再清。

---

## 六、建议的实施顺序

**第 1 批（一次上线，都是现成数据）**
1. 地球左侧「实时动态流」（含留言）
2. 卡片上加点赞踩 + 留言入口（`SetVote`/`AddComment` 已有）
3. 卡片显示文章数 + 最新一条留言
4. 首页「流量 TOP 榜」+「最新文章」

**第 2 批（要动表）**
5. `node_posts` 多文章（第五节）
6. `node_status_daily` 状态时间轴 + 在线率
7. 详情页：流量日历、IP/线路信息卡

**第 3 批（重，但差异化）**
8. 交易/转让区（表已存在，纯做界面）
9. 路由追踪展示
10. 后台：批量命令下发、审计日志查看

---

## 附：三路调研的要点来源

- **监控类**：哪吒监控 Nezha、cppla/ServerStatus、Komari、Uptime Kuma、Beszel、Netdata、Glances、Prometheus+Grafana 的功能对标（详见 `docs/FEATURE-MATRIX.md`）
- **售卖/展示类**：搬瓦工 / DMIT / RackNerd 等商家官网的商品页字段；主机测评、NodeSeek、HostLoc 等社区的测评模板字段
- **可视化实现**：全部限定在**零依赖原生 SVG / Canvas / CSS** 内——项目是 `go:embed` 单二进制，没有构建链，不能引 React/Vue/ECharts/Chart.js。点阵地球已经是手写 WebGL 的先例，状态时间轴、热力图、TOP 榜都可以用 CSS Grid + SVG 手写。
