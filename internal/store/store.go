// Package store 是 Kokoro Hub 的 SQLite 存储层。
//
// 设计约定：
//   - 驱动使用纯 Go 的 modernc.org/sqlite（无 cgo），通过标准库 database/sql 访问；
//   - 建表语句嵌入在 schema.sql 中，Open() 时按 PRAGMA user_version 做简单版本管理；
//   - 所有时间字段为毫秒 Unix 时间戳（int64）；
//   - 所有 ID 带前缀（nd_ 节点、cm_ 评论、th_ 主题等），由 newID 生成。
package store

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "modernc.org/sqlite"

	"github.com/Vincentkeio/kokoro/internal/model"
)

//go:embed schema.sql
var schemaSQL string

// schemaVersion 当前 schema 版本，记录在 PRAGMA user_version 中。
const schemaVersion = 10

// 5 分钟聚合窗口长度（毫秒）。
const bucket5m = int64(5 * 60 * 1000)

// ErrNotFound 查询不到记录时返回，便于调用方用 errors.Is 区分。
var ErrNotFound = errors.New("store: 记录不存在")

// Store 持有一个 SQLite 连接池。可并发使用。
type Store struct {
	db   *sql.DB
	path string

	// writeMu 串行化写入事务：SQLite 同一时刻只允许一个写者，
	// 提前排队比让连接撞上 SQLITE_BUSY 更省事。
	writeMu sync.Mutex

	insertRaw *sql.Stmt // 指标写入预编译语句
}

// MetricPoint 是历史曲线上的一点，供图表接口使用。
type MetricPoint struct {
	Ts      int64   `json:"ts"`
	CPU     float64 `json:"cpu"`
	MemUsed int64   `json:"mem_used"`
	NetUp   int64   `json:"net_up"`
	NetDown int64   `json:"net_down"`
	Load1   float64 `json:"load1"`
}

// Open 打开（必要时创建）位于 path 的 SQLite 数据库并执行迁移。
// 父目录不存在时自动创建。
func Open(path string) (*Store, error) {
	if path == "" {
		path = "kokoro.db"
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("创建数据目录失败: %w", err)
		}
	}

	db, err := sql.Open("sqlite", buildDSN(path))
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	// SQLite 写并发有限，限制连接数避免大量 SQLITE_BUSY 重试。
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)

	s := &Store{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.prepare(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close 关闭数据库连接（会同时释放预编译语句）。
func (s *Store) Close() error {
	if s.insertRaw != nil {
		s.insertRaw.Close()
	}
	return s.db.Close()
}

// DB 暴露底层连接池，供 Hub 做复杂查询（统计页等）。
// 调用方只读使用；写入请走 Store 的方法。
func (s *Store) DB() *sql.DB { return s.db }

// Path 返回数据库文件路径。
func (s *Store) Path() string { return s.path }

// migrate 在 user_version 为 0 时执行建表，并把版本号写回。
func (s *Store) migrate() error {
	var v int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("读取 user_version 失败: %w", err)
	}
	if v > schemaVersion {
		return fmt.Errorf("数据库 schema 版本 %d 高于本程序支持的 %d，请升级 Kokoro", v, schemaVersion)
	}
	if v == 0 {
		// 全新库：整份建表语句一遍跑完。
		if _, err := s.db.Exec(schemaSQL); err != nil {
			return fmt.Errorf("执行建表语句失败: %w", err)
		}
		v = schemaVersion
	} else if v < schemaVersion {
		// 老库：按版本顺序补增量。每条迁移都写成幂等的，重复执行无害。
		for _, m := range migrations {
			if m.version <= v {
				continue
			}
			if _, err := s.db.Exec(m.sql); err != nil {
				return fmt.Errorf("迁移到 schema v%d 失败: %w", m.version, err)
			}
			v = m.version
		}
	}
	if _, err := s.db.Exec(fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		return fmt.Errorf("写入 user_version 失败: %w", err)
	}
	return nil
}

// migrations 是增量迁移脚本，按 version 升序执行。
var migrations = []struct {
	version int
	sql     string
}{
	{version: 2, sql: `CREATE TABLE IF NOT EXISTS votes (
    target_type TEXT    NOT NULL DEFAULT '',
    target_id   TEXT    NOT NULL DEFAULT '',
    voter       TEXT    NOT NULL DEFAULT '',
    value       INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL DEFAULT 0,
    updated_at  INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (target_type, target_id, voter)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS idx_votes_target ON votes (target_type, target_id);`},
	{version: 3, sql: `CREATE TABLE IF NOT EXISTS net_quality (
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
CREATE INDEX IF NOT EXISTS idx_netq_node_isp ON net_quality (node_id, isp);`},
	{version: 4, sql: `ALTER TABLE themes ADD COLUMN bundle BLOB;
ALTER TABLE themes ADD COLUMN meta BLOB;`},
	{version: 5, sql: `CREATE TABLE IF NOT EXISTS events (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    ts      INTEGER NOT NULL DEFAULT 0,
    node_id TEXT    NOT NULL DEFAULT '',
    kind    TEXT    NOT NULL DEFAULT '',
    text    TEXT    NOT NULL DEFAULT '',
    ref     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_events_ts      ON events (ts DESC);
CREATE INDEX IF NOT EXISTS idx_events_node_ts ON events (node_id, ts DESC);`},
	{version: 6, sql: `CREATE TABLE IF NOT EXISTS node_tasks (
    id          TEXT    PRIMARY KEY,
    node_id     TEXT    NOT NULL DEFAULT '',
    kind        TEXT    NOT NULL DEFAULT '',
    title       TEXT    NOT NULL DEFAULT '',
    cmd         TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL DEFAULT 'queued',
    summary     TEXT    NOT NULL DEFAULT '',
    detail      TEXT    NOT NULL DEFAULT '',
    error       TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL DEFAULT 0,
    started_at  INTEGER NOT NULL DEFAULT 0,
    finished_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_tasks_node   ON node_tasks (node_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON node_tasks (status, created_at);`},
	{version: 7, sql: `ALTER TABLE nodes ADD COLUMN tcp_cc    TEXT    NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN tcp_qdisc TEXT    NOT NULL DEFAULT '';
ALTER TABLE nodes ADD COLUMN nat       INTEGER NOT NULL DEFAULT 0;`},
	{version: 8, sql: `CREATE TABLE IF NOT EXISTS node_articles (
    id         TEXT    PRIMARY KEY,
    node_id    TEXT    NOT NULL,
    title      TEXT    NOT NULL DEFAULT '',
    summary    TEXT    NOT NULL DEFAULT '',
    content_md TEXT    NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_node_articles_node
    ON node_articles (node_id, sort_order, created_at);
-- 把老的"一台一篇"搬过来，不能丢
INSERT INTO node_articles (id, node_id, title, summary, content_md, created_at, updated_at)
SELECT 'art_' || node_id, node_id, '', summary, content_md, updated_at, updated_at
FROM node_profile WHERE TRIM(content_md) <> '';`},
	{version: 9, sql: `CREATE TABLE IF NOT EXISTS node_votes (
    node_id    TEXT    NOT NULL,
    voter      TEXT    NOT NULL,
    value      INTEGER NOT NULL,
    created_at INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (node_id, voter)
);
CREATE INDEX IF NOT EXISTS idx_node_votes_node ON node_votes (node_id);`},
	// v9 里我建了张 node_votes，想给卡片做赞踩 —— 结果发现站内早就有
	// 通用的 votes 表（含 target_type/target_id，连评论的赞踩都在用）。
	// 这张多余的清掉，免得以后有人看着两张表不知道该写哪张。
	{version: 10, sql: `DROP TABLE IF EXISTS node_votes;`},
}

// prepare 预编译高频写入语句。
func (s *Store) prepare() error {
	st, err := s.db.Prepare(`INSERT INTO metrics_raw
(node_id, ts, seq, cpu, mem_used, mem_total, mem_cached, swap_used, swap_total,
 disk_used, disk_total, net_up, net_down, total_up, total_down,
 load1, load5, load15, procs, tcp, udp, uptime, io_read, io_write, hub_latency_ms)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT (node_id, ts) DO NOTHING`)
	if err != nil {
		return fmt.Errorf("预编译指标写入语句失败: %w", err)
	}
	s.insertRaw = st
	return nil
}

// ==================== 节点 ====================

const nodeCols = `id, name, slug, token_hash, owner_id, region, provider, tags, group_name,
visibility, hostname, os, kernel, arch, virt, cpu_model, cpu_cores, mem_total, disk_total,
tcp_cc, tcp_qdisc, nat,
agent_ver, ip, country, city, asn, online, last_seen, offline_warn, created_at, sort_order`

// scanner 同时适配 *sql.Row 与 *sql.Rows。
type scanner interface{ Scan(dest ...any) error }

func scanNode(sc scanner) (*model.Node, error) {
	var (
		n           model.Node
		tags        string
		visibility  string
		online      int
		offlineWarn int
		nat         int
	)
	err := sc.Scan(
		&n.ID, &n.Name, &n.Slug, &n.TokenHash, &n.OwnerID, &n.Region, &n.Provider, &tags,
		&n.Group, &visibility, &n.Hostname, &n.OS, &n.Kernel, &n.Arch, &n.Virt, &n.CPUModel,
		&n.CPUCores, &n.MemTotal, &n.DiskTotal, &n.TCPCC, &n.TCPQdisc, &nat,
		&n.AgentVer, &n.IP, &n.Country, &n.City,
		&n.ASN, &online, &n.LastSeen, &offlineWarn, &n.CreatedAt, &n.SortOrder,
	)
	if err != nil {
		return nil, err
	}
	n.Visibility = model.Visibility(visibility)
	n.Online = online != 0
	n.OfflineWarn = offlineWarn != 0
	n.NAT = nat != 0
	_ = decodeJSON(tags, &n.Tags)
	return &n, nil
}

// CreateNode 写入新节点。ID、slug、created_at 为空时自动生成。
func (s *Store) CreateNode(n *model.Node) error {
	if n == nil {
		return errors.New("node 为空")
	}
	if n.ID == "" {
		n.ID = newID("nd_")
	}
	if n.CreatedAt == 0 {
		n.CreatedAt = nowMS()
	}
	if n.Visibility == "" {
		n.Visibility = model.VisibilityPublic
	}
	if n.Slug == "" {
		n.Slug = slugify(n.Name)
	}
	slug, err := s.uniqueSlug(n.Slug, n.ID)
	if err != nil {
		return err
	}
	n.Slug = slug
	tags, err := encodeJSON(n.Tags)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO nodes (`+nodeCols+`) VALUES (`+placeholders(nodeCols)+`)`,
		n.ID, n.Name, n.Slug, n.TokenHash, n.OwnerID, n.Region, n.Provider, tags, n.Group,
		string(n.Visibility), n.Hostname, n.OS, n.Kernel, n.Arch, n.Virt, n.CPUModel,
		n.CPUCores, n.MemTotal, n.DiskTotal, n.TCPCC, n.TCPQdisc, boolInt(n.NAT),
		n.AgentVer, n.IP, n.Country, n.City,
		n.ASN, boolInt(n.Online), n.LastSeen, boolInt(n.OfflineWarn), n.CreatedAt, n.SortOrder,
	)
	if err != nil {
		return fmt.Errorf("创建节点失败: %w", err)
	}
	return nil
}

// GetNode 按 ID 取节点。
func (s *Store) GetNode(id string) (*model.Node, error) {
	row := s.db.QueryRow("SELECT "+nodeCols+" FROM nodes WHERE id = ?", id)
	n, err := scanNode(row)
	return wrapNoRows(n, err)
}

// GetNodeBySlug 按 slug 取节点。
func (s *Store) GetNodeBySlug(slug string) (*model.Node, error) {
	row := s.db.QueryRow("SELECT "+nodeCols+" FROM nodes WHERE slug = ?", slug)
	n, err := scanNode(row)
	return wrapNoRows(n, err)
}

// GetNodeByTokenHash 按节点令牌摘要取节点（agent 上报鉴权用）。
func (s *Store) GetNodeByTokenHash(h string) (*model.Node, error) {
	row := s.db.QueryRow("SELECT "+nodeCols+" FROM nodes WHERE token_hash = ?", h)
	n, err := scanNode(row)
	return wrapNoRows(n, err)
}

// ListNodes 列出节点；includePrivate 为 false 时只返回 public 节点。
// 排序按 sort_order、名称。
func (s *Store) ListNodes(includePrivate bool) ([]model.Node, error) {
	q := "SELECT " + nodeCols + " FROM nodes"
	if !includePrivate {
		q += " WHERE visibility = 'public'"
	}
	q += " ORDER BY sort_order ASC, name ASC, id ASC"
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// UpdateNode 更新节点已有字段。ID 必须存在。
// 注意：TokenHash 为空时不覆盖原值，避免表单回写把令牌清空。
// SearchNodes 按关键词搜节点：名称、slug、地区、机房、分组、主机名、系统、
// 国家、城市、CPU 型号、标签都参与匹配。多个关键词用空格分隔，之间是「与」的关系。
// includePrivate 为 false 时只返回公开节点（不索引/私有都不出现）。
// 结果按「在线优先 → sort_order → 名称」排序。
func (s *Store) SearchNodes(q string, includePrivate bool) ([]model.Node, error) {
	words := strings.Fields(q)
	where := ""
	args := make([]any, 0, len(words)*13)
	if !includePrivate {
		where += " WHERE visibility = 'public'"
	}
	for _, w := range words {
		if where == "" {
			where += " WHERE "
		} else {
			where += " AND "
		}
		like := "%" + escapeLike(w) + "%"
		where += `(name LIKE ? ESCAPE '\' OR slug LIKE ? ESCAPE '\' OR region LIKE ? ESCAPE '\'
 OR provider LIKE ? ESCAPE '\' OR group_name LIKE ? ESCAPE '\' OR hostname LIKE ? ESCAPE '\'
 OR os LIKE ? ESCAPE '\' OR arch LIKE ? ESCAPE '\' OR country LIKE ? ESCAPE '\' OR city LIKE ? ESCAPE '\'
 OR tags LIKE ? ESCAPE '\' OR cpu_model LIKE ? ESCAPE '\')`
		// 上面条件里有 12 个 ?，数量必须一致
		for i := 0; i < 12; i++ {
			args = append(args, like)
		}
	}
	rows, err := s.db.Query("SELECT "+nodeCols+" FROM nodes"+where+
		" ORDER BY (online = 0), sort_order, name", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// escapeLike 转义 LIKE 通配符，避免用户输入的 % 把整表捞出来。
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func (s *Store) UpdateNode(n *model.Node) error {
	if n == nil || n.ID == "" {
		return errors.New("node.ID 为空")
	}
	slug, err := s.uniqueSlug(n.Slug, n.ID)
	if err != nil {
		return err
	}
	tags, err := encodeJSON(n.Tags)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE nodes SET
name=?, slug=?, owner_id=?, region=?, provider=?, tags=?, group_name=?, visibility=?,
hostname=?, os=?, kernel=?, arch=?, virt=?, cpu_model=?, cpu_cores=?, mem_total=?, disk_total=?,
tcp_cc=?, tcp_qdisc=?, nat=?,
agent_ver=?, ip=?, country=?, city=?, asn=?, online=?, last_seen=?, offline_warn=?, sort_order=?,
token_hash=CASE WHEN ?<>'' THEN ? ELSE token_hash END
WHERE id=?`,
		n.Name, slug, n.OwnerID, n.Region, n.Provider, tags, n.Group, string(n.Visibility),
		n.Hostname, n.OS, n.Kernel, n.Arch, n.Virt, n.CPUModel, n.CPUCores, n.MemTotal, n.DiskTotal,
		n.TCPCC, n.TCPQdisc, boolInt(n.NAT),
		n.AgentVer, n.IP, n.Country, n.City, n.ASN, boolInt(n.Online), n.LastSeen,
		boolInt(n.OfflineWarn), n.SortOrder, n.TokenHash, n.TokenHash, n.ID,
	)
	if err != nil {
		return fmt.Errorf("更新节点失败: %w", err)
	}
	return nil
}

// DeleteNode 删除节点，其指标、内容、评论等通过外键级联删除。
func (s *Store) DeleteNode(id string) error {
	if _, err := s.db.Exec("DELETE FROM nodes WHERE id = ?", id); err != nil {
		return fmt.Errorf("删除节点失败: %w", err)
	}
	return nil
}

// TouchNode 更新节点心跳：最近上报时间与在线状态，ip 非空时一并更新。
func (s *Store) TouchNode(id string, ts int64, ip string) error {
	if ts == 0 {
		ts = nowMS()
	}
	var err error
	if ip != "" {
		_, err = s.db.Exec("UPDATE nodes SET last_seen=?, online=1, ip=? WHERE id=?", ts, ip, id)
	} else {
		_, err = s.db.Exec("UPDATE nodes SET last_seen=?, online=1 WHERE id=?", ts, id)
	}
	if err != nil {
		return fmt.Errorf("更新心跳失败: %w", err)
	}
	return nil
}

// SetNodeOnline 批量或单个设置节点在线状态（离线检测用）。
// SetNodeOnline 更新在线状态，并在**状态发生跃迁**时记一条事件。
//
// 只在跃迁时写：上报很频繁，每次都写会把事件表刷爆，动态流也没法看。
// 事件写失败不影响状态更新——动态流是锦上添花，不该连累主流程。
func (s *Store) SetNodeOnline(id string, online bool) error {
	var prev int
	err := s.db.QueryRow("SELECT online FROM nodes WHERE id=?", id).Scan(&prev)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if _, err := s.db.Exec("UPDATE nodes SET online=? WHERE id=?", boolInt(online), id); err != nil {
		return err
	}
	if err == sql.ErrNoRows || (prev == 1) == online {
		return nil // 没变，不记
	}
	var name string
	_ = s.db.QueryRow("SELECT name FROM nodes WHERE id=?", id).Scan(&name)
	if name == "" {
		name = id
	}
	kind, text := "offline", name+" 掉线了"
	if online {
		kind, text = "online", name+" 上线了"
	}
	_, _ = s.db.Exec(`INSERT INTO events (ts, node_id, kind, text, ref) VALUES (?,?,?,?,?)`,
		nowMS(), id, kind, text, "")
	return nil
}

// ==================== 指标 ====================

// rawCols 是指标明细列（不含 node_id）；rawColsWithID 用于需要 node_id 的查询。
const rawCols = `ts, seq, cpu, mem_used, mem_total, mem_cached, swap_used, swap_total,
disk_used, disk_total, net_up, net_down, total_up, total_down,
load1, load5, load15, procs, tcp, udp, uptime, io_read, io_write, hub_latency_ms`

const rawColsWithID = `node_id, ` + rawCols

// scanMetricsWithNode 扫描以 node_id 开头的一行指标；nodeID 传 nil 时丢弃该列。
func scanMetricsWithNode(sc scanner, nodeID *string) (*model.Metrics, error) {
	if nodeID == nil {
		var dump string
		nodeID = &dump
	}
	var (
		m       model.Metrics
		tcp     sql.NullInt64
		udp     sql.NullInt64
		ioRead  sql.NullInt64
		ioWrite sql.NullInt64
		lat     sql.NullFloat64
	)
	err := sc.Scan(
		nodeID,
		&m.Ts, &m.Seq, &m.CPU.Usage, &m.Mem.Used, &m.Mem.Total, &m.Mem.Cached,
		&m.Mem.SwapUsed, &m.Mem.SwapTotal, &m.Disk.Used, &m.Disk.Total,
		&m.Net.Up, &m.Net.Down, &m.Net.TotalUp, &m.Net.TotalDown,
		&m.CPU.Load1, &m.CPU.Load5, &m.CPU.Load15, &m.Host.Procs,
		&tcp, &udp, &m.Host.Uptime, &ioRead, &ioWrite, &lat,
	)
	if err != nil {
		return nil, err
	}
	m.V = model.ProtocolVersion
	// 采集失败的字段用 NULL 区分「没采到」和「真的是 0」。
	if tcp.Valid || udp.Valid {
		m.Conn = &model.ConnStat{TCP: int(tcp.Int64), UDP: int(udp.Int64)}
	}
	if ioRead.Valid || ioWrite.Valid {
		m.IO = &model.IOStat{Read: ioRead.Int64, Write: ioWrite.Int64}
	}
	if lat.Valid {
		m.NetQ = &model.NetQStat{HubLatencyMS: lat.Float64}
	}
	return &m, nil
}

func metricsArgs(nodeID string, m *model.Metrics) []any {
	var tcp, udp, ioRead, ioWrite any
	if m.Conn != nil {
		tcp, udp = m.Conn.TCP, m.Conn.UDP
	}
	if m.IO != nil {
		ioRead, ioWrite = m.IO.Read, m.IO.Write
	}
	var lat any
	if m.NetQ != nil {
		lat = m.NetQ.HubLatencyMS
	}
	return []any{
		nodeID, m.Ts, m.Seq, m.CPU.Usage, m.Mem.Used, m.Mem.Total, m.Mem.Cached,
		m.Mem.SwapUsed, m.Mem.SwapTotal, m.Disk.Used, m.Disk.Total,
		m.Net.Up, m.Net.Down, m.Net.TotalUp, m.Net.TotalDown,
		m.CPU.Load1, m.CPU.Load5, m.CPU.Load15, m.Host.Procs, tcp, udp, m.Host.Uptime,
		ioRead, ioWrite, lat,
	}
}

// InsertMetrics 写入一条指标。重复的 (node_id, ts) 会被忽略（agent 补报去重）。
func (s *Store) InsertMetrics(nodeID string, m *model.Metrics) error {
	if nodeID == "" {
		return errors.New("node_id 为空")
	}
	if m == nil {
		return errors.New("metrics 为空")
	}
	return s.InsertMetricsBatch(nodeID, []*model.Metrics{m})
}

// InsertMetricsBatch 在一个事务里批量写入多条指标（agent 断线补报场景）。
func (s *Store) InsertMetricsBatch(nodeID string, ms []*model.Metrics) error {
	if len(ms) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt := tx.Stmt(s.insertRaw)
	for _, m := range ms {
		if m == nil {
			continue
		}
		if _, err := stmt.Exec(metricsArgs(nodeID, m)...); err != nil {
			stmt.Close()
			tx.Rollback()
			return fmt.Errorf("写入指标失败: %w", err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交指标事务失败: %w", err)
	}
	return nil
}

// LatestMetrics 返回该节点最近一条原始指标；无数据返回 (nil, nil)。
func (s *Store) LatestMetrics(nodeID string) (*model.Metrics, error) {
	row := s.db.QueryRow("SELECT "+rawColsWithID+" FROM metrics_raw WHERE node_id = ? ORDER BY ts DESC LIMIT 1", nodeID)
	m, err := scanMetricsWithNode(row, nil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return m, err
}

// LatestSnapshot 返回全部节点的最新一条指标，键为 node_id，供 SSE 全量推送。
func (s *Store) LatestSnapshot() (map[string]*model.Metrics, error) {
	q := `SELECT r.node_id, ` + rawCols + ` FROM metrics_raw r
JOIN (SELECT node_id, MAX(ts) AS mts FROM metrics_raw GROUP BY node_id) x
  ON r.node_id = x.node_id AND r.ts = x.mts`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]*model.Metrics)
	for rows.Next() {
		var id string
		m, err := scanMetricsWithNode(rows, &id)
		if err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
}

// History 返回节点在 [from, to] 区间内的历史曲线，最多 maxPoints 个点。
// 跨度超过 6 小时自动改用 metrics_5m 聚合表，否则读 metrics_raw。
func (s *Store) History(nodeID string, from, to int64, maxPoints int) ([]MetricPoint, error) {
	if to <= 0 {
		to = nowMS()
	}
	if from <= 0 {
		from = to - 6*60*60*1000
	}
	if maxPoints <= 0 {
		maxPoints = 500
	}
	span := to - from
	if span <= 0 {
		span = 1
	}
	step := span / int64(maxPoints)
	if step < 1 {
		step = 1
	}

	var q string
	if span > 6*60*60*1000 {
		// 5 分钟粒度：按 step 再降采样一次
		q = `SELECT MIN(bucket), AVG(cpu_avg), AVG(mem_used_avg), AVG(net_up_avg), AVG(net_down_avg), AVG(load1_avg)
FROM metrics_5m WHERE node_id = ? AND bucket >= ? AND bucket <= ? GROUP BY bucket / ? ORDER BY 1`
	} else {
		q = `SELECT MIN(ts), AVG(cpu), AVG(mem_used), AVG(net_up), AVG(net_down), AVG(load1)
FROM metrics_raw WHERE node_id = ? AND ts >= ? AND ts <= ? GROUP BY ts / ? ORDER BY 1`
	}
	rows, err := s.db.Query(q, nodeID, from, to, step)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MetricPoint
	for rows.Next() {
		var (
			ts                                  int64
			cpu, memUsed, netUp, netDown, load1 float64
		)
		if err := rows.Scan(&ts, &cpu, &memUsed, &netUp, &netDown, &load1); err != nil {
			return nil, err
		}
		out = append(out, MetricPoint{
			Ts:      ts,
			CPU:     cpu,
			MemUsed: int64(math.Round(memUsed)),
			NetUp:   int64(math.Round(netUp)),
			NetDown: int64(math.Round(netDown)),
			Load1:   load1,
		})
	}
	return out, rows.Err()
}

// Aggregate5m 把已闭合的 5 分钟窗口从 metrics_raw 聚合进 metrics_5m。
// 通过 settings 里的 agg_cursor 记录进度，只处理新数据。
func (s *Store) Aggregate5m() error {
	now := nowMS()
	winStart := now - now%bucket5m

	cursor := int64(0)
	if v, err := s.GetSetting("agg_cursor"); err == nil && v != "" {
		cursor, _ = strconv.ParseInt(v, 10, 64)
	}
	if cursor == 0 {
		// 首次运行：从现有最早数据开始，避免把历史全表扫一遍却又落在游标之前。
		var min sql.NullInt64
		if err := s.db.QueryRow("SELECT MIN(ts) FROM metrics_raw").Scan(&min); err == nil && min.Valid {
			cursor = min.Int64
		} else {
			cursor = winStart
		}
	}
	if winStart <= cursor {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO metrics_5m
(node_id, bucket, cpu_avg, cpu_max, mem_used_avg, net_up_avg, net_down_avg, load1_avg,
 net_up_max, net_down_max, total_up_max, total_down_max, samples)
SELECT node_id, (ts / ?) * ?,
       AVG(cpu), MAX(cpu), AVG(mem_used), AVG(net_up), AVG(net_down), AVG(load1),
       MAX(net_up), MAX(net_down), MAX(total_up), MAX(total_down), COUNT(*)
FROM metrics_raw WHERE ts >= ? AND ts < ?
GROUP BY node_id, (ts / ?) * ?
ON CONFLICT (node_id, bucket) DO UPDATE SET
  cpu_avg = excluded.cpu_avg,
  cpu_max = MAX(metrics_5m.cpu_max, excluded.cpu_max),
  mem_used_avg = excluded.mem_used_avg,
  net_up_avg = excluded.net_up_avg,
  net_down_avg = excluded.net_down_avg,
  load1_avg = excluded.load1_avg,
  net_up_max = MAX(metrics_5m.net_up_max, excluded.net_up_max),
  net_down_max = MAX(metrics_5m.net_down_max, excluded.net_down_max),
  total_up_max = MAX(metrics_5m.total_up_max, excluded.total_up_max),
  total_down_max = MAX(metrics_5m.total_down_max, excluded.total_down_max),
  samples = excluded.samples`,
		bucket5m, bucket5m, cursor, winStart, bucket5m, bucket5m)
	if err != nil {
		return fmt.Errorf("聚合 5 分钟指标失败: %w", err)
	}
	return s.SetSetting("agg_cursor", strconv.FormatInt(winStart, 10))
}

// Prune 按保留时长删除过期指标与过期会话。
// 删除行数超过阈值时才做一次空间回收，避免频繁 IO。
func (s *Store) Prune(retentionRaw, retention5m time.Duration) error {
	now := nowMS()
	var deleted int64

	if retentionRaw > 0 {
		res, err := s.db.Exec("DELETE FROM metrics_raw WHERE ts < ?", now-int64(retentionRaw/time.Millisecond))
		if err != nil {
			return fmt.Errorf("清理原始指标失败: %w", err)
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	if retention5m > 0 {
		res, err := s.db.Exec("DELETE FROM metrics_5m WHERE bucket < ?", now-int64(retention5m/time.Millisecond))
		if err != nil {
			return fmt.Errorf("清理聚合指标失败: %w", err)
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	// 顺手清理过期会话。
	if _, err := s.db.Exec("DELETE FROM sessions WHERE expires_at > 0 AND expires_at < ?", now); err != nil {
		return fmt.Errorf("清理会话失败: %w", err)
	}

	switch {
	case deleted >= 200000:
		_, _ = s.db.Exec("VACUUM")
	case deleted >= 10000:
		// auto_vacuum 未开启时该语句是空操作，忽略错误即可。
		_, _ = s.db.Exec("PRAGMA incremental_vacuum")
	}
	return nil
}

// ==================== 内容 ====================

// GetProfile 取节点内容页；不存在返回 (nil, nil)。
func (s *Store) GetProfile(nodeID string) (*model.NodeProfile, error) {
	var (
		p     model.NodeProfile
		album string
		specs string
	)
	err := s.db.QueryRow(`SELECT node_id, cover, summary, content_md, album, price, expire_at,
specs, pv, uv, updated_at FROM node_profile WHERE node_id = ?`, nodeID).
		Scan(&p.NodeID, &p.Cover, &p.Summary, &p.ContentMD, &album, &p.Price, &p.ExpireAt,
			&specs, &p.PV, &p.UV, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = decodeJSON(album, &p.Album)
	_ = decodeJSON(specs, &p.Specs)
	return &p, nil
}

// SaveProfile 新建或更新节点内容页。
func (s *Store) SaveProfile(p *model.NodeProfile) error {
	if p == nil || p.NodeID == "" {
		return errors.New("profile.NodeID 为空")
	}
	if p.UpdatedAt == 0 {
		p.UpdatedAt = nowMS()
	}
	album, err := encodeJSON(p.Album)
	if err != nil {
		return err
	}
	specs, err := encodeJSON(p.Specs)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO node_profile
(node_id, cover, summary, content_md, album, price, expire_at, specs, pv, uv, updated_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT (node_id) DO UPDATE SET
  cover = excluded.cover, summary = excluded.summary, content_md = excluded.content_md,
  album = excluded.album, price = excluded.price, expire_at = excluded.expire_at,
  specs = excluded.specs, pv = excluded.pv, uv = excluded.uv, updated_at = excluded.updated_at`,
		p.NodeID, p.Cover, p.Summary, p.ContentMD, album, p.Price, p.ExpireAt, specs,
		p.PV, p.UV, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("保存节点内容失败: %w", err)
	}
	return nil
}

// ListComments 列出评论。nodeID 为空表示全部节点；
// onlyApproved 为 true 时只返回已审核通过的。
func (s *Store) ListComments(nodeID string, onlyApproved bool) ([]model.Comment, error) {
	q := `SELECT id, node_id, parent_id, author, contact, content, status, ip_hash, created_at
FROM comments`
	var args []any
	var conds []string
	if nodeID != "" {
		conds = append(conds, "node_id = ?")
		args = append(args, nodeID)
	}
	if onlyApproved {
		conds = append(conds, "status = 'approved'")
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY created_at ASC, id ASC"

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Comment
	for rows.Next() {
		var (
			c      model.Comment
			status string
		)
		if err := rows.Scan(&c.ID, &c.NodeID, &c.ParentID, &c.Author, &c.Contact, &c.Content,
			&status, &c.IPHash, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Status = model.CommentStatus(status)
		out = append(out, c)
	}
	return out, rows.Err()
}

// AddComment 新增评论。ID、created_at 自动生成，状态缺省 pending。
func (s *Store) AddComment(c *model.Comment) error {
	if c == nil {
		return errors.New("comment 为空")
	}
	if c.ID == "" {
		c.ID = newID("cm_")
	}
	if c.CreatedAt == 0 {
		c.CreatedAt = nowMS()
	}
	if c.Status == "" {
		c.Status = model.CommentPending
	}
	_, err := s.db.Exec(`INSERT INTO comments
(id, node_id, parent_id, author, contact, content, status, ip_hash, created_at)
VALUES (?,?,?,?,?,?,?,?,?)`,
		c.ID, c.NodeID, c.ParentID, c.Author, c.Contact, c.Content, string(c.Status),
		c.IPHash, c.CreatedAt)
	if err != nil {
		return fmt.Errorf("新增评论失败: %w", err)
	}
	return nil
}

// ModerateComment 修改评论状态（approved / spam / pending）。
func (s *Store) ModerateComment(id string, status model.CommentStatus) error {
	_, err := s.db.Exec("UPDATE comments SET status = ? WHERE id = ?", string(status), id)
	if err != nil {
		return fmt.Errorf("审核评论失败: %w", err)
	}
	return nil
}

// DeleteComment 删除单条评论（连带它的票）。
func (s *Store) DeleteComment(id string) error {
	if _, err := s.db.Exec("DELETE FROM votes WHERE target_type = 'comment' AND target_id = ?", id); err != nil {
		return fmt.Errorf("清理评论票数失败: %w", err)
	}
	if _, err := s.db.Exec("DELETE FROM comments WHERE id = ?", id); err != nil {
		return fmt.Errorf("删除评论失败: %w", err)
	}
	return nil
}

// ListRecentComments 按时间倒序列出评论供后台审核。limit<=0 时用 50。
// status 为空表示不限状态。
func (s *Store) ListRecentComments(limit int, status model.CommentStatus) ([]model.Comment, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, node_id, parent_id, author, contact, content, status, ip_hash, created_at
FROM comments`
	var args []any
	if status != "" {
		q += " WHERE status = ?"
		args = append(args, string(status))
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Comment
	for rows.Next() {
		var (
			c  model.Comment
			st string
		)
		if err := rows.Scan(&c.ID, &c.NodeID, &c.ParentID, &c.Author, &c.Contact, &c.Content,
			&st, &c.IPHash, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Status = model.CommentStatus(st)
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetVote 投一票。val 为 1 赞成、-1 反对、0 撤票。
// 同一访客对同一目标只会保留最后一票（主键冲突即改票）。
func (s *Store) SetVote(targetType, targetID, voter string, val int) error {
	if targetType == "" || targetID == "" || voter == "" {
		return errors.New("投票参数不完整")
	}
	ts := nowMS()
	if val == 0 {
		_, err := s.db.Exec(
			"DELETE FROM votes WHERE target_type = ? AND target_id = ? AND voter = ?",
			targetType, targetID, voter)
		if err != nil {
			return fmt.Errorf("撤票失败: %w", err)
		}
		return nil
	}
	if val > 0 {
		val = 1
	} else {
		val = -1
	}
	_, err := s.db.Exec(`INSERT INTO votes (target_type, target_id, voter, value, created_at, updated_at)
VALUES (?,?,?,?,?,?)
ON CONFLICT (target_type, target_id, voter) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		targetType, targetID, voter, val, ts, ts)
	if err != nil {
		return fmt.Errorf("投票失败: %w", err)
	}
	return nil
}

// VoteCounts 批量取投票聚合。ids 为空时返回空 map。
// 返回的 map 只包含在 votes 表里有过记录的目标；没票的目标调用方按 0 处理。
func (s *Store) VoteCounts(targetType string, ids []string, voter string) (map[string]model.VoteCount, error) {
	out := make(map[string]model.VoteCount, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	ph := make([]string, 0, len(ids))
	// 占位符按 SQL 里出现的先后绑定：先 SELECT 里的 voter，再 WHERE 里的 target_type 与 id 列表。
	args := make([]any, 0, len(ids)+2)
	args = append(args, voter, targetType)
	for _, id := range ids {
		ph = append(ph, "?")
		args = append(args, id)
	}
	q := `SELECT target_id,
  SUM(CASE WHEN value > 0 THEN 1 ELSE 0 END),
  SUM(CASE WHEN value < 0 THEN 1 ELSE 0 END),
  COALESCE(SUM(CASE WHEN voter = ? THEN value ELSE 0 END), 0)
FROM votes WHERE target_type = ? AND target_id IN (` + strings.Join(ph, ",") + `)
GROUP BY target_id`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("统计票数失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id  string
			cnt model.VoteCount
		)
		if err := rows.Scan(&id, &cnt.Up, &cnt.Down, &cnt.Mine); err != nil {
			return nil, err
		}
		out[id] = cnt
	}
	return out, rows.Err()
}

// ListAlertEvents 列出最近的告警事件（新的在前）。nodeID 为空表示全部节点。
func (s *Store) ListAlertEvents(nodeID string, limit int) ([]model.AlertEvent, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT id, node_id, rule_id, message, fired_at, resolved FROM alert_events`
	var args []any
	if nodeID != "" {
		q += " WHERE node_id = ?"
		args = append(args, nodeID)
	}
	q += " ORDER BY fired_at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AlertEvent
	for rows.Next() {
		var e model.AlertEvent
		if err := rows.Scan(&e.ID, &e.NodeID, &e.RuleID, &e.Message,
			&e.FiredAt, &e.Resolved); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListTradeOffers 列出交易意向（新的在前）。nodeID 为空表示全部节点。
func (s *Store) ListTradeOffers(nodeID string) ([]model.TradeOffer, error) {
	q := `SELECT id, node_id, from_name, contact_type, contact_value, price_offer, message,
status, created_at FROM trade_offers`
	var args []any
	if nodeID != "" {
		q += " WHERE node_id = ?"
		args = append(args, nodeID)
	}
	q += " ORDER BY created_at DESC, id DESC"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.TradeOffer
	for rows.Next() {
		var t model.TradeOffer
		if err := rows.Scan(&t.ID, &t.NodeID, &t.FromName, &t.ContactType, &t.ContactValue,
			&t.PriceOffer, &t.Message, &t.Status, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddTradeOffer 新增交易意向。ID、created_at 自动生成，状态缺省 new。
func (s *Store) AddTradeOffer(t *model.TradeOffer) error {
	if t == nil {
		return errors.New("trade offer 为空")
	}
	if t.ID == "" {
		t.ID = newID("tr_")
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = nowMS()
	}
	if t.Status == "" {
		t.Status = "new"
	}
	_, err := s.db.Exec(`INSERT INTO trade_offers
(id, node_id, from_name, contact_type, contact_value, price_offer, message, status, created_at)
VALUES (?,?,?,?,?,?,?,?,?)`,
		t.ID, t.NodeID, t.FromName, t.ContactType, t.ContactValue, t.PriceOffer,
		t.Message, t.Status, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("新增交易意向失败: %w", err)
	}
	return nil
}

// UpdateTradeStatus 更新交易意向状态（read / accepted / declined）。
func (s *Store) UpdateTradeStatus(id, status string) error {
	_, err := s.db.Exec("UPDATE trade_offers SET status = ? WHERE id = ?", status, id)
	if err != nil {
		return fmt.Errorf("更新交易意向状态失败: %w", err)
	}
	return nil
}

// ==================== 主题 ====================

// ListThemes 列出全部主题，启用的排在最前。
func (s *Store) ListThemes() ([]model.Theme, error) {
	rows, err := s.db.Query(`SELECT id, name, author, version, homepage, enabled, source_url,
checksum, manifest, css, bundle, meta, created_at FROM themes ORDER BY enabled DESC, name ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Theme
	for rows.Next() {
		t, err := scanTheme(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

func scanTheme(sc scanner) (*model.Theme, error) {
	var (
		t       model.Theme
		enabled int
	)
	err := sc.Scan(&t.ID, &t.Name, &t.Author, &t.Version, &t.Homepage, &enabled,
		&t.SourceURL, &t.Checksum, &t.Manifest, &t.CSS, &t.Bundle, &t.Meta, &t.CreatedAt)
	if err != nil {
		return nil, err
	}
	t.Enabled = enabled != 0
	return &t, nil
}

// SaveTheme 新建或更新主题（manifest/css 以 BLOB 存储）。
func (s *Store) SaveTheme(t *model.Theme) error {
	if t == nil {
		return errors.New("theme 为空")
	}
	if t.ID == "" {
		t.ID = newID("th_")
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = nowMS()
	}
	_, err := s.db.Exec(`INSERT INTO themes
(id, name, author, version, homepage, enabled, source_url, checksum, manifest, css, bundle, meta, created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT (id) DO UPDATE SET
  name = excluded.name, author = excluded.author, version = excluded.version,
  homepage = excluded.homepage, enabled = excluded.enabled, source_url = excluded.source_url,
  checksum = excluded.checksum, manifest = excluded.manifest, css = excluded.css,
  bundle = excluded.bundle, meta = excluded.meta`,
		t.ID, t.Name, t.Author, t.Version, t.Homepage, boolInt(t.Enabled), t.SourceURL,
		t.Checksum, t.Manifest, t.CSS, t.Bundle, t.Meta, t.CreatedAt)
	if err != nil {
		return fmt.Errorf("保存主题失败: %w", err)
	}
	return nil
}

// DeleteTheme 删除一份主题存档。
func (s *Store) DeleteTheme(id string) error {
	if id == "" {
		return errors.New("theme id 为空")
	}
	if _, err := s.db.Exec(`DELETE FROM themes WHERE id = ?`, id); err != nil {
		return fmt.Errorf("删除主题失败: %w", err)
	}
	return nil
}

// EnableTheme 启用指定主题，同时停用其他主题（同一时刻只启用一个）。
func (s *Store) EnableTheme(id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE themes SET enabled = 0 WHERE enabled <> 0"); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec("UPDATE themes SET enabled = 1 WHERE id = ?", id); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// ==================== 告警 ====================

// ListAlertRules 列出全部告警规则（全局规则排在前面）。
func (s *Store) ListAlertRules() ([]model.AlertRule, error) {
	rows, err := s.db.Query(`SELECT id, node_id, metric, op, threshold, duration, silenced,
channels FROM alert_rules ORDER BY node_id ASC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.AlertRule
	for rows.Next() {
		var (
			r        model.AlertRule
			channels string
			silenced int
		)
		if err := rows.Scan(&r.ID, &r.NodeID, &r.Metric, &r.Op, &r.Threshold, &r.Duration,
			&silenced, &channels); err != nil {
			return nil, err
		}
		r.Silenced = silenced != 0
		_ = decodeJSON(channels, &r.Channels)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveAlertRule 新建或更新告警规则。
func (s *Store) SaveAlertRule(r *model.AlertRule) error {
	if r == nil {
		return errors.New("alert rule 为空")
	}
	if r.ID == "" {
		r.ID = newID("ar_")
	}
	if r.Op == "" {
		r.Op = "gt"
	}
	channels, err := encodeJSON(r.Channels)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO alert_rules
(id, node_id, metric, op, threshold, duration, silenced, channels, created_at)
VALUES (?,?,?,?,?,?,?,?,?)
ON CONFLICT (id) DO UPDATE SET
  node_id = excluded.node_id, metric = excluded.metric, op = excluded.op,
  threshold = excluded.threshold, duration = excluded.duration,
  silenced = excluded.silenced, channels = excluded.channels`,
		r.ID, r.NodeID, r.Metric, r.Op, r.Threshold, r.Duration, boolInt(r.Silenced),
		channels, nowMS())
	if err != nil {
		return fmt.Errorf("保存告警规则失败: %w", err)
	}
	return nil
}

// AddAlertEvent 记录一次告警触发。ID、fired_at 自动生成。
func (s *Store) AddAlertEvent(e *model.AlertEvent) error {
	if e == nil {
		return errors.New("alert event 为空")
	}
	if e.ID == "" {
		e.ID = newID("ae_")
	}
	if e.FiredAt == 0 {
		e.FiredAt = nowMS()
	}
	_, err := s.db.Exec(`INSERT INTO alert_events (id, node_id, rule_id, message, fired_at, resolved)
VALUES (?,?,?,?,?,?)`, e.ID, e.NodeID, e.RuleID, e.Message, e.FiredAt, e.Resolved)
	if err != nil {
		return fmt.Errorf("记录告警事件失败: %w", err)
	}
	return nil
}

// ==================== 用户 ====================

// GetUserByName 按用户名取用户；不存在返回 (nil, nil)。
func (s *Store) GetUserByName(name string) (*model.User, error) {
	u := &model.User{}
	err := s.db.QueryRow(`SELECT id, name, pass_hash, totp_secret, role, created_at, last_login_at
FROM users WHERE name = ?`, name).
		Scan(&u.ID, &u.Name, &u.PassHash, &u.TOTPSecret, &u.Role, &u.CreatedAt, &u.LastLoginAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return u, err
}

// CreateUser 新建用户。ID、created_at 自动生成，角色缺省 viewer。
func (s *Store) CreateUser(u *model.User) error {
	if u == nil || u.Name == "" {
		return errors.New("用户名不能为空")
	}
	if u.ID == "" {
		u.ID = newID("us_")
	}
	if u.CreatedAt == 0 {
		u.CreatedAt = nowMS()
	}
	if u.Role == "" {
		u.Role = "viewer"
	}
	_, err := s.db.Exec(`INSERT INTO users (id, name, pass_hash, totp_secret, role, created_at, last_login_at)
VALUES (?,?,?,?,?,?,?)`, u.ID, u.Name, u.PassHash, u.TOTPSecret, u.Role, u.CreatedAt, u.LastLoginAt)
	if err != nil {
		return fmt.Errorf("创建用户失败: %w", err)
	}
	return nil
}

// ==================== 配置与审计 ====================

// GetSetting 读取配置项；不存在返回空字符串与 nil 错误。
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

// SetSetting 写入（或覆盖）配置项。
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	return nil
}

// AddAudit 追加一条审计日志，ts 自动填充为当前时间。
func (s *Store) AddAudit(actor, action, target, detail string) error {
	_, err := s.db.Exec(`INSERT INTO audit_log (id, ts, actor, action, target, detail)
VALUES (?,?,?,?,?,?)`, newID("au_"), nowMS(), actor, action, target, detail)
	if err != nil {
		return fmt.Errorf("写入审计日志失败: %w", err)
	}
	return nil
}

// ==================== 内部工具 ====================

// newID 生成带前缀的随机 ID：prefix + 16 位十六进制。
// ListSettings 返回指定前缀的全部键值，用于枚举安装令牌一类的派生数据。
// 前缀里的 LIKE 通配符会被转义，避免误匹配。
func (s *Store) ListSettings(prefix string) (map[string]string, error) {
	esc := strings.ReplaceAll(prefix, `\`, `\\`)
	esc = strings.ReplaceAll(esc, `%`, `\%`)
	esc = strings.ReplaceAll(esc, `_`, `\_`)

	rows, err := s.db.Query(`SELECT key, value FROM settings WHERE key LIKE ? ESCAPE '\'`, esc+`%`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

func newID(prefix string) string {
	return prefix + randHex(8)
}

// randHex 返回 n 字节随机数对应的十六进制字符串（长度 2n）。
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// 随机数不可用属于致命错误，继续运行会产生可预测的 ID，直接中止更安全。
		panic("store: crypto/rand 不可用: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// placeholders 按列清单生成同样数量的 ? 占位符，避免手写时数量写错。
func placeholders(colList string) string {
	n := strings.Count(colList, ",") + 1
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func nowMS() int64 { return time.Now().UnixMilli() }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func encodeJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func decodeJSON(s string, v any) error {
	if s == "" || s == "null" {
		return nil
	}
	return json.Unmarshal([]byte(s), v)
}

// wrapNoRows 把 sql.ErrNoRows 统一为 ErrNotFound。
func wrapNoRows[T any](v *T, err error) (*T, error) {
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return v, err
}

// uniqueSlug 保证 slug 唯一：为空时退回节点 ID，冲突时追加随机后缀。
func (s *Store) uniqueSlug(slug, selfID string) (string, error) {
	if slug == "" {
		slug = selfID
	}
	if slug == "" {
		slug = "node"
	}
	for i := 0; i < 10; i++ {
		var id string
		err := s.db.QueryRow("SELECT id FROM nodes WHERE slug = ? AND id <> ?", slug, selfID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return slug, nil
		}
		if err != nil {
			return "", err
		}
		slug = slug + "-" + randHex(2)
	}
	return "", fmt.Errorf("无法为节点生成唯一 slug: %s", slug)
}

// slugify 把节点名转成 URL 友好的 slug：保留字母、数字，其余字符统一折叠成连字符。
// 中文等 Unicode 字母会被保留（浏览器会对路径做百分号编码）。
func slugify(name string) string {
	var sb strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			sb.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && sb.Len() > 0 {
				sb.WriteRune('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(sb.String(), "-")
}

// buildDSN 组装连接串：file: URI + 常用 PRAGMA。
// WAL 模式下读写不互相阻塞，busy_timeout 让并发写入排队而不是立刻失败。
func buildDSN(path string) string {
	const params = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"

	p := filepath.ToSlash(path)
	// URI 中 % ? # 有歧义，按 SQLite 的百分号编码规则转义。
	p = strings.ReplaceAll(p, "%", "%25")
	p = strings.ReplaceAll(p, "?", "%3f")
	p = strings.ReplaceAll(p, "#", "%23")

	if filepath.IsAbs(path) {
		// 绝对路径写成 file:///abs/path（Windows 盘符路径则是 file:///D:/path）。
		if !strings.HasPrefix(p, "/") {
			p = "/" + p
		}
		return "file://" + p + params
	}
	return "file:" + p + params
}

// ---- 首页用的批量查询 ----
//
// 首页要同时显示所有节点的网络曲线和全网流量汇总。逐个节点调 History()
// 会变成 N 次查询，节点一多就明显拖慢首屏。所以这里统一做**一次查询拿全部**。

// NetPoint 是 5 分钟聚合窗口里的平均上下行速率（字节/秒）。
type NetPoint struct {
	Bucket int64
	Up     float64
	Down   float64
}

// RecentNetSeries 一次取回**所有节点**最近若干窗口的速率序列，按 node_id 分组。
//
// 只读 metrics_5m：原始表粒度太细，首页只要 1 小时的走势，5 分钟点够用且查询便宜。
func (s *Store) RecentNetSeries(since int64) (map[string][]NetPoint, error) {
	if since <= 0 {
		since = nowMS() - 60*60*1000
	}
	rows, err := s.db.Query(
		`SELECT node_id, bucket, net_up_avg, net_down_avg
FROM metrics_5m WHERE bucket >= ? ORDER BY node_id, bucket`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string][]NetPoint)
	for rows.Next() {
		var id string
		var p NetPoint
		if err := rows.Scan(&id, &p.Bucket, &p.Up, &p.Down); err != nil {
			return nil, err
		}
		out[id] = append(out[id], p)
	}
	return out, rows.Err()
}

// TrafficSince 汇总从 since 起的上下行字节数。
//
// 算法：把每个 5 分钟窗口的平均速率 × 窗口秒数累加。
// 这是**估算**不是精确值——它假设窗口内速率均匀，而且窗口边界会有最多 5 分钟的误差。
// 对首页那个「今日流量」的展示足够了；要精确计量得另存累计差值。
func (s *Store) TrafficSince(since int64) (up, down int64, err error) {
	if since <= 0 {
		return 0, 0, nil
	}
	var upAvg, downAvg float64
	var n int64
	// SUM 在没有任何窗口时返回 NULL，用 COALESCE 兜住
	err = s.db.QueryRow(
		`SELECT COALESCE(SUM(net_up_avg), 0), COALESCE(SUM(net_down_avg), 0), COUNT(*)
FROM metrics_5m WHERE bucket >= ?`, since).Scan(&upAvg, &downAvg, &n)
	if err != nil {
		return 0, 0, err
	}
	const bucketSec = 300.0 // 5 分钟窗口
	return int64(upAvg * bucketSec), int64(downAvg * bucketSec), nil
}
