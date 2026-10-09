-- Kokoro Hub 数据库 schema（SQLite）
--
-- 约定：
--   * 时间一律使用毫秒 Unix 时间戳（INTEGER），由 Go 侧写入 time.Now().UnixMilli()；
--   * 布尔值用 INTEGER 0/1 存储；
--   * 容量/流量单位为字节，速率单位为字节每秒，CPU/负载为浮点百分比或无量纲值；
--   * 结构化字段（tags/specs/album/channels 等）以 JSON 文本存储。
--
-- 本文件由 internal/store/store.go 通过 //go:embed 嵌入，
-- Open() 时读取 PRAGMA user_version，为 0 才整份执行（CREATE TABLE IF NOT EXISTS，可重复执行）。

-- ---------- 节点 ----------

CREATE TABLE IF NOT EXISTS nodes (
    id           TEXT    PRIMARY KEY,
    name         TEXT    NOT NULL DEFAULT '',
    slug         TEXT    NOT NULL DEFAULT '',
    token_hash   TEXT    NOT NULL DEFAULT '',
    owner_id     TEXT    NOT NULL DEFAULT '',
    region       TEXT    NOT NULL DEFAULT '',
    provider     TEXT    NOT NULL DEFAULT '',
    tags         TEXT    NOT NULL DEFAULT '[]',      -- JSON 字符串数组
    group_name   TEXT    NOT NULL DEFAULT '',        -- 列名避开 SQL 保留字 group
    visibility   TEXT    NOT NULL DEFAULT 'public',  -- public | unlisted | private
    hostname     TEXT    NOT NULL DEFAULT '',
    os           TEXT    NOT NULL DEFAULT '',
    kernel       TEXT    NOT NULL DEFAULT '',
    arch         TEXT    NOT NULL DEFAULT '',
    virt         TEXT    NOT NULL DEFAULT '',
    cpu_model    TEXT    NOT NULL DEFAULT '',
    cpu_cores    INTEGER NOT NULL DEFAULT 0,
    -- TCP 加速：拥塞控制算法（bbr/cubic…）与队列规则（fq/fq_codel…）
    tcp_cc       TEXT    NOT NULL DEFAULT '',
    tcp_qdisc    TEXT    NOT NULL DEFAULT '',
    -- 是否在 NAT 后面（公网 IP 不在自己网卡上）。0/1 布尔，**不存 IP**
    nat          INTEGER NOT NULL DEFAULT 0,
    mem_total    INTEGER NOT NULL DEFAULT 0,
    disk_total   INTEGER NOT NULL DEFAULT 0,
    agent_ver    TEXT    NOT NULL DEFAULT '',
    ip           TEXT    NOT NULL DEFAULT '',
    country      TEXT    NOT NULL DEFAULT '',
    city         TEXT    NOT NULL DEFAULT '',
    asn          INTEGER NOT NULL DEFAULT 0,
    online       INTEGER NOT NULL DEFAULT 0,
    last_seen    INTEGER NOT NULL DEFAULT 0,
    offline_warn INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL DEFAULT 0,
    sort_order   INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_token_hash ON nodes (token_hash);
CREATE UNIQUE INDEX IF NOT EXISTS idx_nodes_slug      ON nodes (slug);
CREATE INDEX        IF NOT EXISTS idx_nodes_last_seen  ON nodes (last_seen);

-- ---------- 原始指标（保留 24 小时）----------

CREATE TABLE IF NOT EXISTS metrics_raw (
    node_id      TEXT    NOT NULL,
    ts           INTEGER NOT NULL,
    seq          INTEGER NOT NULL DEFAULT 0,
    cpu          REAL    NOT NULL DEFAULT 0,
    mem_used     INTEGER NOT NULL DEFAULT 0,
    mem_total    INTEGER NOT NULL DEFAULT 0,
    mem_cached   INTEGER NOT NULL DEFAULT 0,
    swap_used    INTEGER NOT NULL DEFAULT 0,
    swap_total   INTEGER NOT NULL DEFAULT 0,
    disk_used    INTEGER NOT NULL DEFAULT 0,
    disk_total   INTEGER NOT NULL DEFAULT 0,
    net_up       INTEGER NOT NULL DEFAULT 0,   -- B/s
    net_down     INTEGER NOT NULL DEFAULT 0,   -- B/s
    total_up     INTEGER NOT NULL DEFAULT 0,   -- 累计字节
    total_down   INTEGER NOT NULL DEFAULT 0,
    load1        REAL    NOT NULL DEFAULT 0,
    load5        REAL    NOT NULL DEFAULT 0,
    load15       REAL    NOT NULL DEFAULT 0,
    procs        INTEGER NOT NULL DEFAULT 0,
    tcp          INTEGER,                      -- NULL 表示本次未采集
    udp          INTEGER,
    uptime       INTEGER NOT NULL DEFAULT 0,   -- 秒
    io_read      INTEGER,                      -- B/s，NULL 表示未采集
    io_write     INTEGER,
    hub_latency_ms REAL,                       -- 到 Hub 的往返延迟，NULL 表示未采集
    PRIMARY KEY (node_id, ts)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_metrics_raw_node_ts ON metrics_raw (node_id, ts);

-- ---------- 5 分钟聚合（保留 90 天）----------

CREATE TABLE IF NOT EXISTS metrics_5m (
    node_id       TEXT    NOT NULL,
    bucket        INTEGER NOT NULL,   -- 窗口起始时间戳（ts 向下取整到 5 分钟）
    cpu_avg       REAL    NOT NULL DEFAULT 0,
    cpu_max       REAL    NOT NULL DEFAULT 0,
    mem_used_avg  INTEGER NOT NULL DEFAULT 0,
    net_up_avg    INTEGER NOT NULL DEFAULT 0,
    net_down_avg  INTEGER NOT NULL DEFAULT 0,
    load1_avg     REAL    NOT NULL DEFAULT 0,
    net_up_max    INTEGER NOT NULL DEFAULT 0,
    net_down_max  INTEGER NOT NULL DEFAULT 0,
    total_up_max  INTEGER NOT NULL DEFAULT 0,
    total_down_max INTEGER NOT NULL DEFAULT 0,
    samples       INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, bucket)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_metrics_5m_node_bucket ON metrics_5m (node_id, bucket);

-- ---------- 节点内容页 ----------

CREATE TABLE IF NOT EXISTS node_profile (
    node_id    TEXT    PRIMARY KEY,
    cover      TEXT    NOT NULL DEFAULT '',
    summary    TEXT    NOT NULL DEFAULT '',
    content_md TEXT    NOT NULL DEFAULT '',
    album      TEXT    NOT NULL DEFAULT '[]',   -- JSON 字符串数组
    price      TEXT    NOT NULL DEFAULT '',
    expire_at  TEXT    NOT NULL DEFAULT '',
    specs      TEXT    NOT NULL DEFAULT '{}',   -- JSON 对象
    pv         INTEGER NOT NULL DEFAULT 0,
    uv         INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (node_id) REFERENCES nodes (id) ON DELETE CASCADE
);

-- ---------- 评论 ----------

CREATE TABLE IF NOT EXISTS comments (
    id         TEXT    PRIMARY KEY,
    node_id    TEXT    NOT NULL DEFAULT '',
    parent_id  TEXT    NOT NULL DEFAULT '',
    author     TEXT    NOT NULL DEFAULT '',
    contact    TEXT    NOT NULL DEFAULT '',   -- 仅主人可见
    content    TEXT    NOT NULL DEFAULT '',
    status     TEXT    NOT NULL DEFAULT 'pending',  -- pending | approved | spam
    ip_hash    TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT 0,
    pinned     INTEGER NOT NULL DEFAULT 0,    -- 1 = 站长置顶
    FOREIGN KEY (node_id) REFERENCES nodes (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_comments_node_time ON comments (node_id, created_at);

-- ---------- 点赞 / 点踩 ----------

-- target_type: node | comment；voter 是访客标识（随机 cookie 或 IP 哈希），不存明文 IP。
-- 主键即「同一访客对同一目标只能有一票」，重复点击等于改票，取消等于删行。
CREATE TABLE IF NOT EXISTS votes (
    target_type TEXT    NOT NULL DEFAULT '',
    target_id   TEXT    NOT NULL DEFAULT '',
    voter       TEXT    NOT NULL DEFAULT '',
    value       INTEGER NOT NULL DEFAULT 0,   -- 1 赞 / -1 踩
    created_at  INTEGER NOT NULL DEFAULT 0,
    updated_at  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (target_type, target_id, voter)
) WITHOUT ROWID;

CREATE INDEX IF NOT EXISTS idx_votes_target ON votes (target_type, target_id);

-- ---------- 网络质量（三网分省探测）----------

CREATE TABLE IF NOT EXISTS net_quality (
    node_id  TEXT    NOT NULL DEFAULT '',
    ts       INTEGER NOT NULL DEFAULT 0,      -- 这一轮探测的时间戳（毫秒）
    mode     TEXT    NOT NULL DEFAULT '',     -- icmp | tcp（无 raw socket 权限时降级）
    province TEXT    NOT NULL DEFAULT '',
    isp      TEXT    NOT NULL DEFAULT '',     -- 电信 | 联通 | 移动
    city     TEXT    NOT NULL DEFAULT '',
    host     TEXT    NOT NULL DEFAULT '',     -- 探测目标 IP 或域名
    ok       INTEGER NOT NULL DEFAULT 0,      -- 1 通 / 0 不通
    latency  REAL    NOT NULL DEFAULT 0,      -- 平均往返延迟（毫秒）
    jitter   REAL    NOT NULL DEFAULT 0,      -- 抖动（毫秒）
    loss     REAL    NOT NULL DEFAULT 0       -- 丢包率 0-100
);

CREATE INDEX IF NOT EXISTS idx_netq_node_time ON net_quality (node_id, ts DESC);
CREATE INDEX IF NOT EXISTS idx_netq_node_isp ON net_quality (node_id, isp);

-- ---------- 交易意向 ----------

CREATE TABLE IF NOT EXISTS trade_offers (
    id            TEXT    PRIMARY KEY,
    node_id       TEXT    NOT NULL DEFAULT '',
    from_name     TEXT    NOT NULL DEFAULT '',
    contact_type  TEXT    NOT NULL DEFAULT '',
    contact_value TEXT    NOT NULL DEFAULT '',   -- 仅主人可见
    price_offer   TEXT    NOT NULL DEFAULT '',
    message       TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT 'new', -- new | read | accepted | declined
    created_at    INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (node_id) REFERENCES nodes (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_trade_offers_node_time ON trade_offers (node_id, created_at);

-- ---------- 主题 ----------

CREATE TABLE IF NOT EXISTS themes (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL DEFAULT '',
    author     TEXT    NOT NULL DEFAULT '',
    version    TEXT    NOT NULL DEFAULT '',
    homepage   TEXT    NOT NULL DEFAULT '',
    enabled    INTEGER NOT NULL DEFAULT 0,
    source_url TEXT    NOT NULL DEFAULT '',
    checksum   TEXT    NOT NULL DEFAULT '',
    manifest   BLOB,                          -- theme.json 原文
    css        BLOB,                          -- 主题样式原文
    bundle     BLOB,                          -- 原始 .kokoro-theme 包字节
    meta       BLOB,                          -- package 元数据 JSON
    created_at INTEGER NOT NULL DEFAULT 0
);

-- ---------- 用户与会话 ----------

CREATE TABLE IF NOT EXISTS users (
    id           TEXT    PRIMARY KEY,
    name         TEXT    NOT NULL DEFAULT '',
    pass_hash    TEXT    NOT NULL DEFAULT '',
    totp_secret  TEXT    NOT NULL DEFAULT '',
    role         TEXT    NOT NULL DEFAULT 'viewer',  -- owner | admin | viewer
    created_at   INTEGER NOT NULL DEFAULT 0,
    last_login_at INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_name ON users (name);

CREATE TABLE IF NOT EXISTS sessions (
    id         TEXT    PRIMARY KEY,
    user_id    TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL DEFAULT 0,
    ip         TEXT    NOT NULL DEFAULT '',
    ua         TEXT    NOT NULL DEFAULT '',
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions (expires_at);

-- ---------- 告警 ----------

CREATE TABLE IF NOT EXISTS alert_rules (
    id        TEXT    PRIMARY KEY,
    node_id   TEXT    NOT NULL DEFAULT '',   -- 空表示全局规则
    metric    TEXT    NOT NULL DEFAULT '',   -- cpu | mem | disk | offline | load | traffic
    op        TEXT    NOT NULL DEFAULT 'gt',
    threshold REAL    NOT NULL DEFAULT 0,
    duration  INTEGER NOT NULL DEFAULT 0,    -- 持续秒数
    silenced  INTEGER NOT NULL DEFAULT 0,
    channels  TEXT    NOT NULL DEFAULT '[]', -- JSON 字符串数组
    created_at INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_alert_rules_node ON alert_rules (node_id);

CREATE TABLE IF NOT EXISTS alert_events (
    id       TEXT    PRIMARY KEY,
    node_id  TEXT    NOT NULL DEFAULT '',
    rule_id  TEXT    NOT NULL DEFAULT '',
    message  TEXT    NOT NULL DEFAULT '',
    fired_at INTEGER NOT NULL DEFAULT 0,
    resolved INTEGER NOT NULL DEFAULT 0      -- 0 表示未恢复
);

CREATE INDEX IF NOT EXISTS idx_alert_events_node_time ON alert_events (node_id, fired_at);

-- ---------- 安装令牌 ----------

CREATE TABLE IF NOT EXISTS install_tokens (
    id         TEXT    PRIMARY KEY,
    token_hash TEXT    NOT NULL DEFAULT '',
    label      TEXT    NOT NULL DEFAULT '',
    group_name TEXT    NOT NULL DEFAULT '',
    max_uses   INTEGER NOT NULL DEFAULT 1,
    used_count INTEGER NOT NULL DEFAULT 0,
    expires_at INTEGER NOT NULL DEFAULT 0,   -- 0 表示不过期
    revoked    INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT 0
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_install_tokens_hash ON install_tokens (token_hash);

-- ---------- 键值配置 ----------

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL DEFAULT ''
);

-- ---------- 审计日志 ----------

CREATE TABLE IF NOT EXISTS audit_log (
    id     TEXT    PRIMARY KEY,
    ts     INTEGER NOT NULL DEFAULT 0,
    actor  TEXT    NOT NULL DEFAULT '',
    action TEXT    NOT NULL DEFAULT '',
    target TEXT    NOT NULL DEFAULT '',
    detail TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_audit_log_ts ON audit_log (ts);

-- ---------- 事件流 ----------
--
-- 首页「实时动态流」和以后的「状态时间轴」都吃这张表。
-- 之所以单独建表而不是每次现算：上线/掉线这种**状态跃迁**必须落库，
-- nodes.online 只是个瞬时布尔值，翻过去就查不到历史了。
--
-- kind 取值：online | offline | alert | resolved | task
-- node_id 为空表示与具体节点无关的事件（例如面板自身维护公告）。

CREATE TABLE IF NOT EXISTS events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    ts      INTEGER NOT NULL DEFAULT 0,
    node_id TEXT    NOT NULL DEFAULT '',
    kind    TEXT    NOT NULL DEFAULT '',
    text    TEXT    NOT NULL DEFAULT '',
    ref     TEXT    NOT NULL DEFAULT ''   -- 关联 id（告警 id / 任务 id）
);

CREATE INDEX IF NOT EXISTS idx_events_ts      ON events (ts DESC);
CREATE INDEX IF NOT EXISTS idx_events_node_ts ON events (node_id, ts DESC);

-- ---------- 节点测试任务 ----------
--
-- 从后台下发一条测试命令，agent 拉走后执行，结果回传存这里。
-- 卡片上显示的是 summary（一句话），detail 存原始输出供后台展开看。
--
-- 为什么要有 status 而不是只看 finished_at：
--   queued  -> 还没下发
--   running -> 已下发、agent 正在跑（跑分动辄十几分钟，必须能看出"在跑"）
--   done    -> 成功回传
--   failed  -> 超时 / 退出码非 0 / agent 报错

CREATE TABLE IF NOT EXISTS node_tasks (
    id          TEXT    PRIMARY KEY,
    node_id     TEXT    NOT NULL DEFAULT '',
    kind        TEXT    NOT NULL DEFAULT '',   -- bench | ipquality | netquality | custom
    title       TEXT    NOT NULL DEFAULT '',
    cmd         TEXT    NOT NULL DEFAULT '',   -- 实际下发的 shell
    status      TEXT    NOT NULL DEFAULT 'queued',
    summary     TEXT    NOT NULL DEFAULT '',   -- 卡片上的一句话
    detail      TEXT    NOT NULL DEFAULT '',   -- 原始输出
    error       TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL DEFAULT 0,
    started_at  INTEGER NOT NULL DEFAULT 0,
    finished_at INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_tasks_node    ON node_tasks (node_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tasks_status  ON node_tasks (status, created_at);

-- 每台机器可以有多篇文章。
--
-- 为什么另起一张表而不是复用 node_profile：那张表是"一台一条"的
-- 展示位（封面/价格/规格/浏览量），文章是"一台多篇"，生命周期也不同
-- （站长会一篇篇加，不会为了加文章去改名片）。
CREATE TABLE IF NOT EXISTS node_articles (
    id         TEXT    PRIMARY KEY,
    node_id    TEXT    NOT NULL,
    title      TEXT    NOT NULL DEFAULT '',
    summary    TEXT    NOT NULL DEFAULT '',   -- 列表页只显示这个，不渲染全文
    content_md TEXT    NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (node_id) REFERENCES nodes (id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_node_articles_node
    ON node_articles (node_id, sort_order, created_at);
