# Kokoro 接口契约（agent ↔ hub）

> 本文件是**并行开发的法律**：所有模块必须按此实现，任何字段变更都要回来改这里并通知全组。
> 单位约定：**内存/磁盘/流量总量一律用字节（bytes），速率一律用字节每秒（B/s）**。时间用毫秒 Unix 时间戳。

---

## 1. 角色与端口

| 角色 | 默认监听 | 说明 |
|---|---|---|
| Hub | `127.0.0.1:8799`（反代模式）或 `:443`（自动 TLS 模式） | 面板 + API |
| Agent | 无监听 | **只主动外连**，不需要任何入站端口 |

---

## 2. 认证

- 每台小鸡一个 `node_token`（注册时签发，Hub 只存 hash），上报时带 `Authorization: Bearer <node_token>`。
- 注册用一次性的 `install_token`（Hub 后台生成，可设过期与可用次数）。
- 自签 CA 模式下，agent 侧 **pin CA 的 SPKI 指纹**（`sha256/base64`），指纹不符直接拒绝连接。

---

## 3. 接口

### `POST /api/v1/register` — 注册（安装时执行一次）

请求：

```json
{
  "install_token": "it_XXXXXXXX",
  "hostname": "zt6501937868",
  "os": "debian",
  "kernel": "6.12.96",
  "arch": "amd64",
  "virt": "kvm",
  "agent_version": "0.1.0",
  "cpu_model": "AMD EPYC 7763",
  "cpu_cores": 1,
  "mem_total": 1018000000,
  "disk_total": 15000000000
}
```

响应：

```json
{
  "node_id": "nd_9f2c1a",
  "node_token": "nt_XXXXXXXX",
  "interval_ms": 2000,
  "ca_fingerprint": "sha256/AbCd...",
  "hub_version": "0.1.0"
}
```

失败：`401` install_token 无效或已用尽；`403` 已被吊销。

---

### `POST /api/v1/report` — 上报指标（每 interval 一次）

请求头：`Authorization: Bearer nt_XXXXXXXX`、`Content-Type: application/json`

请求体（`Metrics`，见第 4 节）：

```json
{
  "v": 1,
  "ts": 1730000000123,
  "seq": 12345,
  "host": { "uptime": 1234567, "procs": 89, "threads": 210 },
  "cpu": { "usage": 12.34, "load1": 0.10, "load5": 0.20, "load15": 0.08, "per_core": [12.3] },
  "mem": { "total": 1018000000, "used": 320000000, "cached": 300000000, "swap_total": 0, "swap_used": 0 },
  "disk": { "total": 15000000000, "used": 2400000000 },
  "io":  { "read": 1024, "write": 2048 },
  "net": { "up": 1234, "down": 5678, "total_up": 987654321, "total_down": 1234567890,
           "ifaces": [{ "name": "eth0", "up": 1234, "down": 5678, "total_up": 987654321, "total_down": 1234567890 }] },
  "conn": { "tcp": 12, "udp": 3 },
  "temp": { "coretemp": 45.0 },
  "gpu":  [ { "name": "", "util": 0, "mem_used": 0, "mem_total": 0 } ],
  "netq": { "hub_latency_ms": 98.9, "loss": 0.0 }
}
```

响应：

```json
{
  "ok": true,
  "interval_ms": 2000,
  "server_time": 1730000000456,
  "commands": [
    { "id": "cm_1", "type": "shell", "payload": { "cmd": "uptime", "timeout_ms": 5000 } },
    { "id": "cm_2", "type": "upgrade", "payload": { "version": "0.2.0" } },
    { "id": "cm_3", "type": "reconfig", "payload": { "interval_ms": 5000 } }
  ]
}
```

- 只在有待执行任务时返回非空 `commands`，否则为空数组（省带宽）。
- agent 断线时**缓冲最近 N 条**（默认 30）并在重连后补报，Hub 按 `ts` 去重。

响应码：`401` token 无效（agent 应停止并提示重新注册）；`429` 限流（agent 退避）。

---

### `POST /api/v1/command/result` — 回传任务结果

```json
{ "id": "cm_1", "ok": true, "exit_code": 0, "stdout": "...", "stderr": "", "duration_ms": 12 }
```

---

### `GET /api/v1/dl/kokoro-agent-{os}-{arch}` — 下载 agent 制品

Hub 自带分发，安装脚本优先从这里拉，避免依赖 GitHub。`{os}` ∈ linux/darwin/windows，`{arch}` ∈ amd64/arm64/armv7。
同路径提供 `.sha256`。

---

### `GET /i/<install_token>` — 一键安装脚本（给人 curl 用）

返回一段 shell（Content-Type: `text/plain`），内容等价于 `scripts/install.sh`，自动带上 hub 地址与 token。

---

### `GET /api/v1/stream` — 浏览器实时推送（SSE）

`text/event-stream`，事件：`metrics`（整体快照）、`node_online`、`node_offline`、`alert`。
Hub 每 interval 推一次全量快照（节点数 <200 时全量最简单可靠）。

---

## 4. `Metrics` 字段定义（唯一真源）

| 路径 | 类型 | 单位 | 必填 | 说明 |
|---|---|---|---|---|
| `v` | int | — | ✅ | 协议版本，当前 1 |
| `ts` | int64 | ms | ✅ | agent 本地时间戳 |
| `seq` | int64 | — | ✅ | 单调递增序号，用于去重与补报排序 |
| `host.uptime` | int64 | 秒 | ✅ | |
| `host.procs` | int | 个 | ✅ | 进程数 |
| `host.threads` | int | 个 | ⬜ | |
| `cpu.usage` | float | % | ✅ | 0-100，单核机器 100 即满载 |
| `cpu.load1/5/15` | float | — | ✅ | |
| `cpu.per_core` | []float | % | ⬜ | |
| `mem.total/used/cached` | int64 | B | ✅ | |
| `mem.swap_total/swap_used` | int64 | B | ✅ | |
| `disk.total/used` | int64 | B | ✅ | 所有挂载点合计 |
| `io.read/write` | int64 | B/s | ⬜ | |
| `net.up/down` | int64 | B/s | ✅ | 瞬时速率 |
| `net.total_up/total_down` | int64 | B | ✅ | 累计总量，用于月流量 |
| `net.ifaces[]` | array | — | ⬜ | 每网卡明细 |
| `conn.tcp/udp` | int | 个 | ⬜ | |
| `temp.*` | float | ℃ | ⬜ | key 为传感器名 |
| `gpu[]` | array | — | ⬜ | |
| `netq.hub_latency_ms` | float | ms | ⬜ | 到 Hub 的往返延迟 |
| `netq.loss` | float | % | ⬜ | |

采集失败的字段**省略**而不是填 0（Hub 侧区分"没采到"和"真的是 0"）。

---

## 5. 数据库 schema（SQLite）

见 `internal/store/schema.sql`。核心表：

`nodes` / `metrics_raw`（保留 24h）/ `metrics_5m`（保留 90 天）/ `node_profile` /
`comments` / `trade_offers` / `themes` / `users` / `sessions` / `audit_log` / `alerts`

命名规则：所有 ID 带前缀（`nd_` 节点、`nt_` 节点令牌、`it_` 安装令牌、`cm_` 命令、`th_` 主题）。

---

## 6. Go 包边界（并行开发按此切分，禁止越界写别人的文件）

| 包 | 负责 | 主要文件 |
|---|---|---|
| `internal/model` |  structs 与 DTO，**不含业务逻辑** | `model.go` |
| `internal/collector` | 纯采集，输入无、输出 `*model.Metrics` | `collector.go` |
| `internal/agent` | 注册/上报/重连/命令执行 | `agent.go` |
| `internal/store` | SQLite 读写、迁移、清理 | `store.go` `schema.sql` |
| `internal/hub` | HTTP 路由、页面渲染、SSE | `api.go` `web.go` `sse.go` |
| `internal/theme` | 主题包解析/校验/应用/回滚 | `theme.go` |
| `internal/monitor` | 拨测与告警规则引擎 | `monitor.go` |
| `internal/notify` | 通知渠道分发 | `notify.go` |
| `internal/sshdeploy` | 远程下发安装 | `sshdeploy.go` |
| `cmd/kokoro` | 命令行入口 `serve` / `agent` / `theme` / `backup` | `main.go` |

跨包只允许通过 `internal/model` 的类型与显式接口通信。

---

## 7. 错误码约定

| HTTP | 含义 | agent 行为 |
|---|---|---|
| 401 | token 无效/被吊销 | 停止上报，写日志提示重新注册 |
| 403 | 节点被禁用 | 同上 |
| 429 | 限流 | 指数退避，最长 60s |
| 5xx | Hub 故障 | 退避重连，本地缓冲 |

---

## 8. 版本兼容

- Hub 与 agent 各有 `version`；Hub 只接受**主版本号相同**的 agent。
- 协议 `v` 字段升级时，Hub 需同时兼容旧值至少 2 个大版本。
