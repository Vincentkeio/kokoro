// Package model 定义 Kokoro 的全部数据结构。
//
// 这里是并行开发的唯一真源：所有包只允许通过本包的类型通信。
// 单位约定：内存/磁盘/流量总量用字节，速率用字节每秒，时间用毫秒 Unix 时间戳。
package model

// 协议版本。agent 与 hub 主版本号必须一致。
const ProtocolVersion = 1

// ---- 指标上报 ----

// Metrics 是一次上报的完整快照。采集失败的字段省略（指针或 omitempty），
// 以便 Hub 区分「没采到」和「真的是 0」。
type Metrics struct {
	V    int      `json:"v"`
	Ts   int64    `json:"ts"`
	Seq  int64    `json:"seq"`
	Host HostStat `json:"host"`
	CPU  CPUStat  `json:"cpu"`
	Mem  MemStat  `json:"mem"`
	Disk DiskStat `json:"disk"`

	IO    *IOStat    `json:"io,omitempty"`
	Net   NetStat    `json:"net"`
	Conn  *ConnStat  `json:"conn,omitempty"`
	Temp  *TempStat  `json:"temp,omitempty"`
	GPU   []GPUStat  `json:"gpu,omitempty"`
	NetQ  *NetQStat  `json:"netq,omitempty"`
	Parts []PartStat `json:"parts,omitempty"`

	// Facts 是**启动时上报一次**的静态信息（虚拟化/CPU/加速…）。
	//
	// 为什么不塞进注册流程：agent 已经注册过的机器不会重新注册，
	// 新加的字段就永远填不上。放进第一次上报里，老机器升级后也能补齐。
	Facts *HostFacts `json:"facts,omitempty"`
}

// HostFacts 是不随时间变的机器信息。
//
// 注册时（RegisterRequest）和启动后第一次上报（Metrics.Facts）
// 都带这份数据，Hub 收到就更新节点 —— 幂等，重复收到没关系。
type HostFacts struct {
	Virt     string `json:"virt,omitempty"`
	CPUModel string `json:"cpu_model,omitempty"`
	CPUCores int    `json:"cpu_cores,omitempty"`
	TCPCC    string `json:"tcp_cc,omitempty"`
	TCPQdisc string `json:"tcp_qdisc,omitempty"`
	// Country / Region 是 agent **从自己所在的网络**探出来的位置。
	//
	// 为什么不由 Hub 按 IP 反查：IP 归属库对 VPS 不准。实测这 5 台里
	// 就有两台会被判错 —— zouter 的 IP 是美国段但机器在东京，
	// ByteVirt 的 IP 也是美国段但机器在新加坡。
	// agent 问 Cloudflare 的 cdn-cgi/trace，那玩意是按真实网络路径判的。
	//
	// 只在**探到值**时才带 —— 探不到（没网/被墙）就留空，Hub 侧不动原值。
	Country string `json:"country,omitempty"`
	Region  string `json:"region,omitempty"`
	// LocalIPs 只用于判 NAT，Hub 侧不存、不显示。
	LocalIPs []string `json:"local_ips,omitempty"`
}

type HostStat struct {
	Uptime  int64 `json:"uptime"`
	Procs   int   `json:"procs"`
	Threads int   `json:"threads,omitempty"`
}

type CPUStat struct {
	Usage   float64   `json:"usage"`
	Load1   float64   `json:"load1"`
	Load5   float64   `json:"load5"`
	Load15  float64   `json:"load15"`
	PerCore []float64 `json:"per_core,omitempty"`
}

type MemStat struct {
	Total     int64 `json:"total"`
	Used      int64 `json:"used"`
	Cached    int64 `json:"cached,omitempty"`
	SwapTotal int64 `json:"swap_total"`
	SwapUsed  int64 `json:"swap_used"`
}

type DiskStat struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
}

type PartStat struct {
	Mount string `json:"mount"`
	Total int64  `json:"total"`
	Used  int64  `json:"used"`
}

type IOStat struct {
	Read  int64 `json:"read"`
	Write int64 `json:"write"`
}

type NetStat struct {
	Up        int64       `json:"up"`
	Down      int64       `json:"down"`
	TotalUp   int64       `json:"total_up"`
	TotalDown int64       `json:"total_down"`
	Ifaces    []IfaceStat `json:"ifaces,omitempty"`
}

type IfaceStat struct {
	Name      string `json:"name"`
	Up        int64  `json:"up"`
	Down      int64  `json:"down"`
	TotalUp   int64  `json:"total_up"`
	TotalDown int64  `json:"total_down"`
}

type ConnStat struct {
	TCP int `json:"tcp"`
	UDP int `json:"udp"`
}

// TempStat 传感器名 -> 摄氏度。
type TempStat struct {
	Values map[string]float64 `json:"values"`
}

type GPUStat struct {
	Name     string  `json:"name"`
	Util     float64 `json:"util"`
	MemUsed  int64   `json:"mem_used"`
	MemTotal int64   `json:"mem_total"`
}

// NetQStat 到 Hub 的网络质量。
type NetQStat struct {
	HubLatencyMS float64 `json:"hub_latency_ms"`
	Loss         float64 `json:"loss,omitempty"`
}

// ---- 注册 ----

// Article 是某台机器的一篇文章。
//
// 一台可以有多篇（站长会一篇篇加：上手体验、测评、续费记…）——
// 所以单独一张表，而不是塞在"一台一条"的 node_profile 里。
type Article struct {
	ID        string `json:"id"`
	NodeID    string `json:"node_id"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`    // 列表页只显示这个
	ContentMD string `json:"content_md"` // 全文只在弹窗里渲染
	SortOrder int    `json:"sort_order"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type RegisterRequest struct {
	InstallToken string `json:"install_token"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Kernel       string `json:"kernel"`
	Arch         string `json:"arch"`
	Virt         string `json:"virt,omitempty"`
	AgentVersion string `json:"agent_version"`
	CPUModel     string `json:"cpu_model,omitempty"`
	CPUCores     int    `json:"cpu_cores"`
	MemTotal     int64  `json:"mem_total"`
	DiskTotal    int64  `json:"disk_total"`

	// TCP 加速：拥塞控制算法 + 队列规则。都是静态信息，注册时上报一次。
	TCPCC    string `json:"tcp_cc,omitempty"`    // bbr / cubic / ...
	TCPQdisc string `json:"tcp_qdisc,omitempty"` // fq / fq_codel / ...

	// LocalIPs 是本机网卡上的 IPv4。
	// **只用来判 NAT**（和 Hub 看到的来源 IP 比对），Hub 侧不存、不显示。
	LocalIPs []string `json:"local_ips,omitempty"`
}

type RegisterResponse struct {
	NodeID        string `json:"node_id"`
	NodeToken     string `json:"node_token"`
	IntervalMS    int    `json:"interval_ms"`
	CAFingerprint string `json:"ca_fingerprint,omitempty"`
	HubVersion    string `json:"hub_version"`
}

// ---- 上报响应与命令 ----

type ReportResponse struct {
	// FactsOK 表示 Hub 已经收到并保存了这台机器的静态信息。
	//
	// 为什么要这个：静态信息只在启动后第一次上报里带一次，
	// 而「第一次」可能撞上"Hub 还没升级到认识 facts 的版本"、
	// 或者那一次请求正好失败 —— 那就永远补不上了。
	// agent 看到 true 才停发，稳一点。
	FactsOK    bool      `json:"facts_ok,omitempty"`
	OK         bool      `json:"ok"`
	IntervalMS int       `json:"interval_ms"`
	ServerTime int64     `json:"server_time"`
	Commands   []Command `json:"commands"`
}

type Command struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"` // shell | upgrade | reconfig | restart | uninstall
	Payload map[string]any `json:"payload,omitempty"`
}

type CommandResult struct {
	ID         string `json:"id"`
	OK         bool   `json:"ok"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Stdout     string `json:"stdout,omitempty"`
	Stderr     string `json:"stderr,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

// ---- 节点 ----

// Visibility 节点可见性。
type Visibility string

const (
	VisibilityPublic   Visibility = "public"
	VisibilityUnlisted Visibility = "unlisted"
	VisibilityPrivate  Visibility = "private"
)

type Node struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Slug       string     `json:"slug"`
	TokenHash  string     `json:"-"`
	OwnerID    string     `json:"owner_id"`
	Region     string     `json:"region"`
	Provider   string     `json:"provider,omitempty"`
	Tags       []string   `json:"tags,omitempty"`
	Group      string     `json:"group,omitempty"`
	Visibility Visibility `json:"visibility"`
	Hostname   string     `json:"hostname"`
	OS         string     `json:"os"`
	Kernel     string     `json:"kernel"`
	Arch       string     `json:"arch"`
	Virt       string     `json:"virt,omitempty"`
	CPUModel   string     `json:"cpu_model,omitempty"`
	CPUCores   int        `json:"cpu_cores"`
	TCPCC      string     `json:"tcp_cc,omitempty"`
	TCPQdisc   string     `json:"tcp_qdisc,omitempty"`
	// NAT 表示这台机器在 NAT 后面（公网 IP 不在自己网卡上）。
	// 只存布尔值 —— 具体 IP 不落库、不展示。
	NAT         bool   `json:"nat,omitempty"`
	MemTotal    int64  `json:"mem_total"`
	DiskTotal   int64  `json:"disk_total"`
	AgentVer    string `json:"agent_version"`
	IP          string `json:"ip,omitempty"`
	Country     string `json:"country,omitempty"`
	City        string `json:"city,omitempty"`
	ASN         int    `json:"asn,omitempty"`
	Online      bool   `json:"online"`
	LastSeen    int64  `json:"last_seen"`
	OfflineWarn bool   `json:"offline_warn"`
	CreatedAt   int64  `json:"created_at"`
	SortOrder   int    `json:"sort_order"`
}

// NodeView 是页面渲染用的结构：节点静态信息 + 最新指标 + 内容。
type NodeView struct {
	Node
	Latest  *Metrics      `json:"latest,omitempty"`
	Profile *NodeProfile  `json:"profile,omitempty"`
	Daily   *TrafficDaily `json:"daily,omitempty"`
}

type TrafficDaily struct {
	Up       int64 `json:"up"`
	Down     int64 `json:"down"`
	Total    int64 `json:"total"`
	ResetDay int   `json:"reset_day"`
}

// ---- 内容（博客页 / 评论 / 交易意向）----

type NodeProfile struct {
	NodeID    string            `json:"node_id"`
	Cover     string            `json:"cover,omitempty"`
	Summary   string            `json:"summary,omitempty"`
	ContentMD string            `json:"content_md,omitempty"`
	Album     []string          `json:"album,omitempty"`
	Price     string            `json:"price,omitempty"`
	ExpireAt  string            `json:"expire_at,omitempty"`
	Specs     map[string]string `json:"specs,omitempty"`
	PV        int64             `json:"pv"`
	UV        int64             `json:"uv"`
	UpdatedAt int64             `json:"updated_at"`
}

type CommentStatus string

const (
	CommentPending  CommentStatus = "pending"
	CommentApproved CommentStatus = "approved"
	CommentSpam     CommentStatus = "spam"
)

type Comment struct {
	ID        string        `json:"id"`
	NodeID    string        `json:"node_id"`
	ParentID  string        `json:"parent_id,omitempty"`
	Author    string        `json:"author"`
	Contact   string        `json:"-"` // 仅主人可见
	Content   string        `json:"content"`
	Status    CommentStatus `json:"status"`
	IPHash    string        `json:"-"`
	CreatedAt int64         `json:"created_at"`
	// Pinned 表示站长把这条置顶了。置顶的排在最前面，
	// 无论它的时间多早 —— 这是站长想让人先看到的内容。
	Pinned bool `json:"pinned"`
}

// VoteValue 是单张票的取值。
type VoteValue int

const (
	VoteNone VoteValue = 0
	VoteUp   VoteValue = 1
	VoteDown VoteValue = -1
)

// VoteCount 是某个目标的投票聚合。Mine 是当前访客自己投的那张票（0 表示没投）。
type VoteCount struct {
	Up   int `json:"up"`
	Down int `json:"down"`
	Mine int `json:"mine"`
}

// Score 返回净支持数（赞 - 踩）。
func (v VoteCount) Score() int { return v.Up - v.Down }

// MineUp / MineDown 供模板判断按钮高亮，避免在模板里写负数比较。
func (v VoteCount) MineUp() bool   { return v.Mine > 0 }
func (v VoteCount) MineDown() bool { return v.Mine < 0 }

type TradeOffer struct {
	ID           string `json:"id"`
	NodeID       string `json:"node_id"`
	FromName     string `json:"from_name"`
	ContactType  string `json:"contact_type"` // telegram | email | qq | wechat | other
	ContactValue string `json:"-"`            // 仅主人可见
	PriceOffer   string `json:"price_offer,omitempty"`
	Message      string `json:"message"`
	Status       string `json:"status"` // new | read | accepted | declined
	CreatedAt    int64  `json:"created_at"`
}

// ---- 主题 ----

type Theme struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Author    string `json:"author,omitempty"`
	Version   string `json:"version"`
	Homepage  string `json:"homepage,omitempty"`
	Enabled   bool   `json:"enabled"`
	SourceURL string `json:"source_url,omitempty"`
	Checksum  string `json:"checksum,omitempty"`
	Manifest  []byte `json:"-"`
	CSS       []byte `json:"-"`
	// Bundle 是导入时的原始 .kokoro-theme 包字节（若有）。
	// 留着是为了「导出成包」时能原样再发出去，也为了让重启后
	// 主题的资源与摘要哈希不至于丢失。
	Bundle []byte `json:"-"`
	// Meta 是包元数据（package.sha256 / files / signed 等）的 JSON 快照。
	Meta      []byte `json:"-"`
	CreatedAt int64  `json:"created_at"`
}

// ---- 告警 ----

type AlertRule struct {
	ID        string   `json:"id"`
	NodeID    string   `json:"node_id,omitempty"` // 空表示全局
	Metric    string   `json:"metric"`            // cpu | mem | disk | offline | load | traffic
	Op        string   `json:"op"`                // gt | lt
	Threshold float64  `json:"threshold"`
	Duration  int      `json:"duration"` // 持续秒数
	Silenced  bool     `json:"silenced"`
	Channels  []string `json:"channels,omitempty"`
}

type AlertEvent struct {
	ID       string `json:"id"`
	NodeID   string `json:"node_id"`
	RuleID   string `json:"rule_id"`
	Message  string `json:"message"`
	FiredAt  int64  `json:"fired_at"`
	Resolved int64  `json:"resolved,omitempty"`
}

// ---- 用户 ----

type User struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	PassHash    string `json:"-"`
	TOTPSecret  string `json:"-"`
	Role        string `json:"role"` // owner | admin | viewer
	CreatedAt   int64  `json:"created_at"`
	LastLoginAt int64  `json:"last_login_at,omitempty"`
}

// ---- 配置 ----

// AgentConfig 是 /etc/kokoro/config.toml 的结构。
type AgentConfig struct {
	Hub           string `toml:"hub"`
	Token         string `toml:"token"`
	IntervalMS    int    `toml:"interval_ms"`
	CAFingerprint string `toml:"ca_fingerprint,omitempty"`
	NodeName      string `toml:"node_name,omitempty"`
	Insecure      bool   `toml:"insecure,omitempty"`
	NodeID        string `toml:"node_id,omitempty"`

	// NetQIntervalMin 三网探测间隔（分钟）：-1 关闭，0 用默认（30 分钟）。
	NetQIntervalMin int `toml:"netq_interval_min,omitempty"`
}

// HubConfig 是 Hub 侧配置。
type HubConfig struct {
	Listen      string `toml:"listen"`
	BehindProxy bool   `toml:"behind_proxy"`
	Domain      string `toml:"domain,omitempty"`
	TLSMode     string `toml:"tls_mode"` // auto | proxy | selfsigned | none
	DBPath      string `toml:"db_path"`
	DataDir     string `toml:"data_dir"`
	SiteName    string `toml:"site_name"`
	SiteTagline string `toml:"site_tagline,omitempty"`
	PublicIndex bool   `toml:"public_index"`
}

// ---- 事件流 ----

// Event 是首页「实时动态流」里的一条。
//
// kind: online | offline | alert | resolved | task
// node_id 为空表示与具体节点无关（面板自身的公告之类）。
type Event struct {
	ID     int64  `json:"id"`
	TS     int64  `json:"ts"`
	NodeID string `json:"node_id,omitempty"`
	Kind   string `json:"kind"`
	Text   string `json:"text"`
	Ref    string `json:"ref,omitempty"`
}

// ---- 节点测试任务 ----

// TaskStatus 是测试任务的状态。
type TaskStatus string

const (
	// TaskQueued 已入队，还没下发给 agent。
	TaskQueued TaskStatus = "queued"
	// TaskRunning 已下发，agent 正在跑。跑分动辄十几分钟，必须能看出"在跑"。
	TaskRunning TaskStatus = "running"
	// TaskDone 成功回传。
	TaskDone TaskStatus = "done"
	// TaskFailed 超时 / 退出码非 0 / agent 报错。
	TaskFailed TaskStatus = "failed"
)

// NodeTask 是一条从面板下发的测试任务。
type NodeTask struct {
	ID         string     `json:"id"`
	NodeID     string     `json:"node_id"`
	Kind       string     `json:"kind"`  // bench | ipquality | netquality | custom
	Title      string     `json:"title"` // 给人看的名字，例如「硬件跑分」
	Cmd        string     `json:"cmd"`   // 实际下发的 shell
	Status     TaskStatus `json:"status"`
	Summary    string     `json:"summary"` // 卡片上的一句话
	Detail     string     `json:"detail"`  // 原始输出
	Error      string     `json:"error,omitempty"`
	CreatedAt  int64      `json:"created_at"`
	StartedAt  int64      `json:"started_at"`
	FinishedAt int64      `json:"finished_at"`
}
