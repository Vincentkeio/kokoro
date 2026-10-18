package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Vincentkeio/kokoro/internal/store"
	"html/template"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Vincentkeio/kokoro/internal/flags"
	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/notify"
)

// ---- 页面数据 ----

// ownerCard 是站长名片：头像 + 名字 + 签名，显示在首页和详情页底部。
type ownerCard struct {
	Name    string
	Avatar  string
	Bio     string
	Has     bool
	Visible bool
}

type homeData struct {
	Theme    themeView
	SiteName string
	Nodes    []nodeCard
	// GroupFilters 是首页节点分组筛选项，顺序为全部、各分组、未分组。
	GroupFilters []homeGroupFilter
	Online       int
	Total        int
	// Offline 是"总数 - 在线"，供总览卡的离线数据块直接使用；
	// 放在后端算，模板里就不必引入减法函数。
	Offline  int
	Visitors int64
	Year     int
	Query    string // 搜索关键词，非空表示当前是搜索结果页
	Owner    ownerCard
	Hero     heroView
	Regions  []string

	// 信息面板：时钟、点阵地球
	//
	// 统计卡（statCard）已下线 —— 数字都并进「总览」和「全网速率」了。
	Clocks []clockZone
	Globe  globeData
	// GlobeJSON 是喂给前端的地球数据（已序列化）。用 template.JS 是为了
	// 不被 HTML 转义成 &quot; ——它要塞进 <script type="application/json">。
	GlobeJSON template.JS
	// GlobeLandURL 带版本号，改了掩码能立刻生效（浏览器缓存按 URL 区分）
	GlobeLandURL string
	// Feed 是「实时动态流」的内容（上下线 / 告警 / 留言 / 文章 / 测试）。
	//
	// ⚠️ 首页**不再渲染**它了 —— boss 要求撤掉"实时动态"卡片，
	// 位置让给"全网速率"。所以首页也不再调用 homeFeed()：
	// 那会白跑 4 次数据库查询（events/comments/articles/tasks）。
	// 函数保留着，后台或详情页想用随时可以接。
	Feed []feedItem
	// OwnerEmptyHint：当前访客是管理员、但站长名片还是空的。
	// 只在管理员自己看的时候为真——访客不该看到"这里缺东西"的提示。
	OwnerEmptyHint bool

	// Dash 是仪表盘那份数据。并到首页之后两边共用，
	// 不会出现"首页说 3 台在线、仪表盘说 4 台"。
	Dash dashboardData
	// DashJSON 是给前端画波动图的数据（已序列化）。
	// 用 template.JS 是为了不被 HTML 转义成 &quot; —— 它要塞进
	// <script type="application/json">。
	DashJSON template.JS

	// ResJSON 是给前端画 CPU/内存平均使用率图的数据（同样已序列化）。
	ResJSON template.JS
}

type homeGroupFilter struct {
	Name  string
	Key   string
	Count int
}

func makeHomeGroupFilters(nodes []nodeCard, groups []store.NodeGroup) []homeGroupFilter {
	counts := make(map[string]int, len(groups)+1)
	for _, n := range nodes {
		if n.Group == "" {
			counts["ungrouped"]++
		} else {
			counts["group:"+n.Group]++
		}
	}
	filters := make([]homeGroupFilter, 0, len(groups)+2)
	filters = append(filters, homeGroupFilter{Name: "全部", Key: "all", Count: len(nodes)})
	for _, group := range groups {
		key := "group:" + group.Name
		filters = append(filters, homeGroupFilter{Name: group.Name, Key: key, Count: counts[key]})
	}
	filters = append(filters, homeGroupFilter{Name: "未分组", Key: "ungrouped", Count: counts["ungrouped"]})
	return filters
}

// feedItem 是首页「实时动态流」里的一条。
//
// 这一块把四路来源合成一条时间线：状态跃迁（events 表）、告警、
// 留言、文章更新。之所以在 Go 里合并而不是写一条 UNION SQL：
// 每路的"展示文案"规则不同（谁的名字放前面、什么算标题），
// 在 SQL 里拼字符串会把可读性彻底毁掉。
type feedItem struct {
	Kind     string // online | offline | alert | resolved | task | comment | post
	Icon     string
	Text     string
	NodeName string
	Slug     string
	When     string
	At       int64
}

// feedIcon 给每种事件配一个符号。用字符而不是图标字体——
// 单二进制不引外部字体，emoji 在各平台渲染都稳定。
func feedIcon(kind string) string {
	switch kind {
	case "online":
		return "●"
	case "offline":
		return "○"
	case "alert":
		return "⚠"
	case "resolved":
		return "✓"
	case "task":
		return "🔧"
	case "comment":
		return "💬"
	case "post":
		return "📄"
	}
	return "·"
}

// homeFeed 合成首页动态流。
func (h *Hub) homeFeed(nodes []model.Node, limit int) []feedItem {
	byID := make(map[string]model.Node, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}
	nameOf := func(id string) (string, string) {
		n, ok := byID[id]
		if !ok {
			return "", ""
		}
		return n.Name, n.Slug
	}

	var items []feedItem

	// 1) 状态跃迁 / 任务完成（events 表）
	if evs, err := h.store.ListEvents(limit * 2); err == nil {
		for _, e := range evs {
			name, slug := nameOf(e.NodeID)
			if e.NodeID != "" && name == "" {
				continue // 节点已删，历史事件不再展示
			}
			items = append(items, feedItem{
				Kind: e.Kind, Icon: feedIcon(e.Kind), Text: e.Text,
				NodeName: name, Slug: slug, When: relativeTime(e.TS), At: e.TS,
			})
		}
	}

	// 2) 留言
	if cs, err := h.store.ListRecentComments(limit*2, model.CommentApproved); err == nil {
		for _, c := range cs {
			name, slug := nameOf(c.NodeID)
			if name == "" {
				continue
			}
			txt := clampRunes(strings.TrimSpace(strings.ReplaceAll(c.Content, "\n", " ")), 42)
			items = append(items, feedItem{
				Kind: "comment", Icon: feedIcon("comment"),
				Text:     c.Author + " 在 " + name + " 留言：" + txt,
				NodeName: name, Slug: slug, When: relativeTime(c.CreatedAt), At: c.CreatedAt,
			})
		}
	}

	// 3) 文章更新
	if ps, err := h.store.ListRecentProfiles(limit); err == nil {
		for _, p := range ps {
			name, slug := nameOf(p.NodeID)
			if name == "" {
				continue
			}
			what := "更新了文章"
			if s := strings.TrimSpace(p.Summary); s != "" {
				what = "更新了文章《" + clampRunes(s, 30) + "》"
			}
			items = append(items, feedItem{
				Kind: "post", Icon: feedIcon("post"),
				Text:     "站长给 " + name + " " + what,
				NodeName: name, Slug: slug, When: relativeTime(p.UpdatedAt), At: p.UpdatedAt,
			})
		}
	}

	// 按时间倒序，取前 limit 条
	sort.Slice(items, func(i, j int) bool { return items[i].At > items[j].At })
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

// nodeTasks 取某台机器最近的测试记录，把 NDJSON 解析成模板能直接渲染的结构。
//
// 只展示最近几条：跑一次测试动辄几分钟，历史堆多了页面会长得没法看。
func (h *Hub) nodeTasks(nodeID string, limit int) []nodeTaskView {
	ts, err := h.store.ListTasks(nodeID, limit)
	if err != nil {
		return nil
	}
	out := make([]nodeTaskView, 0, len(ts))
	// 每种测试**只留最新的一份**。
	//
	// 列表是按时间倒序的，所以第一次见到的就是最新的。
	// 不筛的话，跑过几次就会出现一模一样的卡片摞在一起
	// （boss 看到过两份「IP 质量与解锁」+ 两份失败的「线路与三网质量」）。
	seenKind := map[string]bool{}
	for _, t := range ts {
		if seenKind[t.Kind] {
			continue
		}
		seenKind[t.Kind] = true

		v := nodeTaskView{
			Title:   t.Title,
			Kind:    t.Kind,
			Status:  string(t.Status),
			When:    relativeTime(t.CreatedAt),
			Summary: t.Summary,
			Error:   t.Error,
		}
		if t.StartedAt > 0 && t.FinishedAt >= t.StartedAt {
			v.ElapsedS = (t.FinishedAt - t.StartedAt) / 1000
		}
		if (t.Status == model.TaskDone || t.Status == model.TaskFailed) && t.Detail != "" {
			rep := parseBenchNDJSON(t.Detail)
			// 摘要在老任务里是空的（那时解析器还不认 NDJSON）。
			// 与其写迁移脚本回填，不如渲染时现算 —— 自愈，而且
			// 以后解析规则改进了，历史任务也会跟着受益。
			if v.Summary == "" {
				v.Summary = summarizeBench(rep)
			}
			if len(rep.Items) > 0 {
				v.HasResult = true
				if rep.ElapsedM > 0 {
					v.ElapsedS = rep.ElapsedM / 1000
				}
				// 任务级错误和条目错误常常是同一句话（Hub 把失败条目的原因
				// 也写进了任务级 Error）。条目里已经有了就别再顶一条，
				// 否则详情页会把失败原因显示两遍。
				if benchErrorDuplicated(rep.Items, v.Error) {
					v.Error = ""
				}
				for _, it := range rep.Items {
					v.Items = append(v.Items, nodeTaskItem{
						Label:  benchLabel(it.Test),
						OK:     it.OK,
						Err:    it.Err,
						Fields: benchFields(it.Test, it.Data),
					})
				}
			}
		}
		out = append(out, v)
	}
	// 展示顺序按类型排：系统硬件 → 三网质量 → IP 质量（同类保持时间倒序）。
	sort.SliceStable(out, func(i, j int) bool {
		return taskKindRank(out[i].Kind) < taskKindRank(out[j].Kind)
	})
	return out
}

// populateTaskCard builds the testing summary shared by SSR and live task updates.
func (h *Hub) populateTaskCard(c *nodeCard) {
	if c == nil || c.ID == "" {
		return
	}
	ts, err := h.store.ListTaskCardTasks(c.ID)
	if err != nil {
		return
	}
	// 每次重建都从空开始：本函数是唯一往 TaskItems 里写的地方，
	// 不清的话同一个卡片对象被刷新两次就会出现两套条目。
	c.TaskItems = nil
	// produced 先按任务类型收条目，最后统一排序（系统硬件 → 三网 → IP 质量）。
	type kindItem struct {
		kind string
		item nodeTaskItem
	}
	var produced []kindItem
	seenKind := make(map[string]bool, 3)
	seenItems := make(map[string]bool)
	var routeSummary []string
	familyRoutes := map[string]map[string]string{}
	var ipSummary string
	familyIPRows := map[string]string{}
	hardwareSummary := map[string]string{}
	for _, t := range ts {
		if seenKind[t.Kind] {
			continue
		}
		if t.Status == model.TaskRunning {
			if c.Online {
				c.TaskRunning = true
			} else {
				c.TaskOffline = true
			}
			continue
		}
		if t.Status == model.TaskQueued {
			if c.Online {
				c.TaskQueued = true
			} else {
				c.TaskOffline = true
			}
			continue
		}
		seenKind[t.Kind] = true
		if t.Status == model.TaskFailed {
			msg := strings.TrimSpace(t.Error)
			if msg == "" {
				msg = "任务执行失败"
			}
			items := parseBenchNDJSON(t.Detail).Items
			// 条目里已经带着同一条失败原因时不再单列任务级错误 ——
			// 否则悬浮面板会把"IPv6 质量与解锁：…退出码 60"显示两遍
			// （boss 2026-10-17 报过）。
			if len(items) == 0 || !benchErrorDuplicated(items, msg) {
				produced = append(produced, kindItem{kind: t.Kind, item: nodeTaskItem{Label: t.Title, Err: msg}})
			}
			if c.TaskWhen == "" {
				at := t.FinishedAt
				if at == 0 {
					at = t.CreatedAt
				}
				c.TaskWhen = relativeTime(at)
			}
			if len(items) == 0 {
				continue
			}
		}
		if t.Status != model.TaskDone && t.Status != model.TaskFailed {
			continue
		}
		var taskRouteRows []string
		if c.TaskWhen == "" {
			at := t.FinishedAt
			if at == 0 {
				at = t.CreatedAt
			}
			c.TaskWhen = relativeTime(at)
		}
		if t.Detail != "" {
			rep := parseBenchNDJSON(t.Detail)
			collectCardRoutes(rep, familyRoutes)
			if len(rep.Items) > 0 {
				for _, row := range strings.Split(summarizeBenchCard(rep), "\n") {
					for _, family := range []string{"IPv4", "IPv6"} {
						if strings.HasPrefix(row, family+" ") && familyIPRows[family] == "" {
							familyIPRows[family] = row
						}
					}
				}
				for _, row := range strings.Split(summarizeBenchCard(rep), "\n") {
					if strings.HasPrefix(row, "回程 ") {
						taskRouteRows = append(taskRouteRows, row)
					} else if strings.HasPrefix(row, "IP质量") && ipSummary == "" {
						ipSummary = row
					} else if strings.HasPrefix(row, "CPU 跑分") {
						hardwareSummary["cpu"] = row
					} else if strings.HasPrefix(row, "内存读写") {
						hardwareSummary["memory"] = row
					} else if strings.HasPrefix(row, "磁盘") {
						hardwareSummary["disk"] = row
					}
				}
			} else {
				legacy := firstNonEmpty(summarizeTask(t.Kind, t.Detail), t.Summary)
				if t.Kind == "ipquality" && legacy != "" && ipSummary == "" {
					ipSummary = "IP质量 " + legacy
				} else if t.Kind == "netquality" && legacy != "" {
					taskRouteRows = append(taskRouteRows, "回程 "+legacy)
				} else {
					for _, row := range cardTaskSummaryRows(legacy) {
						if strings.HasPrefix(row, "回程 ") {
							taskRouteRows = append(taskRouteRows, row)
						} else if strings.HasPrefix(row, "IP质量") && ipSummary == "" {
							ipSummary = row
						}
					}
				}
			}
			for _, item := range rep.Items {
				if seenItems[item.Test] {
					continue
				}
				seenItems[item.Test] = true
				produced = append(produced, kindItem{kind: t.Kind, item: nodeTaskItem{
					Label: benchLabel(item.Test), OK: item.OK, Err: item.Err,
					Fields: benchFields(item.Test, item.Data),
				}})
			}
		} else {
			for _, row := range cardTaskSummaryRows(t.Summary) {
				if strings.HasPrefix(row, "回程 ") {
					taskRouteRows = append(taskRouteRows, row)
				} else if strings.HasPrefix(row, "IP质量") && ipSummary == "" {
					ipSummary = row
				}
			}
		}
		if len(routeSummary) == 0 && len(taskRouteRows) > 0 {
			routeSummary = taskRouteRows
		}
	}
	var rows []string
	for _, item := range []struct{ key, placeholder string }{
		{"cpu", "CPU 跑分 —"},
		{"memory", "内存读写 —"},
		{"disk", "磁盘读写 —"},
	} {
		if row := hardwareSummary[item.key]; row != "" {
			rows = append(rows, row)
		} else {
			rows = append(rows, item.placeholder)
		}
	}
	routes := routeSummaryByCarrier(routeSummary)
	for _, isp := range []string{"电信", "联通", "移动"} {
		v4, v6 := "—", "—"
		if familyRoutes["IPv4"] != nil {
			v4 = firstNonEmpty(familyRoutes["IPv4"][isp], "—")
		} else if _, seen := familyRoutes["IPv4"]; !seen {
			v4 = firstNonEmpty(cardBackboneName(routes[isp]), "—")
		}
		if familyRoutes["IPv6"] != nil {
			v6 = firstNonEmpty(familyRoutes["IPv6"][isp], "—")
		}
		rows = append(rows, "回程 "+isp+" V4 "+v4+" V6 "+v6)
	}
	if len(familyIPRows) > 0 {
		for _, family := range []string{"IPv4", "IPv6"} {
			if row := familyIPRows[family]; row != "" {
				rows = append(rows, row)
			}
		}
	} else {
		if ipSummary == "" {
			ipSummary = "IP质量 —"
		}
		rows = append(rows, ipSummary)
	}
	// 条目按类型排序后再交给模板：系统硬件 → 三网质量 → IP 质量。
	// 任务列表是时间倒序，照原样铺出来会让"最后跑的"排最前面。
	sort.SliceStable(produced, func(i, j int) bool {
		return taskKindRank(produced[i].kind) < taskKindRank(produced[j].kind)
	})
	for _, p := range produced {
		c.TaskItems = append(c.TaskItems, p.item)
	}
	c.TaskSummary = strings.Join(rows, "\n")
	c.TaskSummaryRows = makeTaskSummaryRows(rows)
}

var routeCarrierPrefix = regexp.MustCompile(`(?:^|\s*/\s*)(电信|联通|移动)\s*`)

func routeSummaryByCarrier(rows []string) map[string]string {
	carriers := []string{"电信", "联通", "移动"}
	out := make(map[string]string, len(carriers))
	fallback := ""
	for _, row := range rows {
		value := strings.TrimSpace(strings.TrimPrefix(row, "回程"))
		matches := routeCarrierPrefix.FindAllStringSubmatchIndex(value, -1)
		if len(matches) == 0 && fallback == "" {
			fallback = value
		}
		for i, match := range matches {
			end := len(value)
			if i+1 < len(matches) {
				end = matches[i+1][0]
			}
			// Slashes within a backbone name (4837/169) or observations
			// from different cities belong to this carrier, not another row.
			out[value[match[2]:match[3]]] = strings.TrimSpace(value[match[1]:end])
		}
	}
	if fallback != "" {
		for _, isp := range carriers {
			if out[isp] == "" {
				out[isp] = fallback
			}
		}
	}
	return out
}

type taskSummaryRow struct {
	Label  string
	Value  string
	Badges []routeBadge
	Route  bool
}

func makeTaskSummaryRows(rows []string) []taskSummaryRow {
	out := make([]taskSummaryRow, 0, len(rows))
	for _, row := range rows {
		label, value := "", row
		for _, prefix := range []struct{ text, label string }{
			{"IPv4 IP质量", "IPv4"}, {"IPv6 IP质量", "IPv6"}, {"CPU 跑分", "CPU"}, {"内存读写", "内存"}, {"磁盘读写", "磁盘"},
			{"磁盘读取", "磁盘"}, {"磁盘写入", "磁盘"}, {"回程", "回程"}, {"IP质量", "IP 质量"},
		} {
			if strings.HasPrefix(row, prefix.text) {
				label = prefix.label
				value = strings.TrimSpace(strings.TrimPrefix(row, prefix.text))
				break
			}
		}
		if label == "" {
			label, value = "结果", strings.TrimSpace(row)
		}
		summary := taskSummaryRow{Label: label, Value: value}
		if dual, ok := dualRouteCardRow(row); ok {
			out = append(out, dual)
			continue
		}
		if label == "回程" {
			summary.Badges = cardRouteBadges(value)
		}
		out = append(out, summary)
	}
	return out
}

// cardTaskSummaryRows 兼容旧任务里已保存的摘要，只抽出卡片允许显示的线路/IP 两类。
func cardTaskSummaryRows(summary string) []string {
	var rows []string
	for _, row := range strings.Split(summary, "\n") {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		routeAt := strings.Index(row, "回程")
		ipAt := strings.Index(row, "IP质量")
		if ipAt < 0 {
			ipAt = strings.Index(row, "IP 质量")
		}
		if routeAt >= 0 {
			end := len(row)
			if ipAt > routeAt {
				end = ipAt
			}
			if value := strings.Trim(row[routeAt:end], " \t·•|:-："); value != "" {
				rows = append(rows, value)
			}
		}
		if ipAt >= 0 {
			end := len(row)
			if routeAt > ipAt {
				end = routeAt
			}
			if value := strings.Trim(row[ipAt:end], " \t·•|:-："); value != "" {
				rows = append(rows, value)
			}
		}
	}
	return rows
}

// heroView 是首页顶部那块横幅的数据，由主题 layout.home.hero 决定要不要。
type heroView struct {
	Enabled   bool
	Title     string
	Subtitle  string
	ShowStats bool
}

type nodeCard struct {
	model.Node
	Latest   *model.Metrics
	CPUPct   float64
	MemPct   float64
	DiskPct  float64
	FlagCode string // ISO alpha-2，用于列表上画国旗

	// ---- 点阵地球 ----
	Lat, Lon float64
	HasCoord bool // 认不出坐标就不画点：画错位置比不画更糟

	// ---- 卡片上的图形 ----
	// 指标一律给「数值 + 配色档位」，让模板直接画条，不在模板里算逻辑。
	CPULv, MemLv, DiskLv string
	LoadPct              float64
	LoadLv               string
	SparkLine            string // 网络面积图的折线路径
	SparkArea            string // 面积图的填充路径
	SparkRange           string // 节点小图当前时间范围，与首页总览同步
	SparkInfo            string // 数据不足或无流量时的说明
	// SparkPeak 是所选时间范围里的上下行峰值（B/s），
	// 给卡片标注个刻度——光一条曲线不写量级，看不出"这算大还是小"。
	// 用 int64 是为了直接喂 fmtRate（跟卡片上其它速率一个格式）。
	SparkPeak   int64
	LatencyPct  float64
	LatencyText string
	LatLv       string // 延迟档位（越低越好，判据与占用率相反）
	// TaskSummary 是最近一次测试的一句话结果（跑分/解锁/线路），
	// 卡片上显示成一行；没跑过测试就是空串。
	TaskSummary     string
	TaskSummaryRows []taskSummaryRow
	TaskRunning     bool
	TaskQueued      bool
	TaskOffline     bool
	// 鼠标悬停在摘要上时弹出的详情。卡片上只放得下一行，
	// 但解锁了哪些服务、是不是机房 IP 这些恰恰是访客最想看的。
	TaskTitle string
	TaskWhen  string
	TaskItems []nodeTaskItem
	// Uptime 是状态时间轴（近 30 天，一天一格）
	Uptime *uptimeSummary

	// ExpireText / ExpireLv 是到期倒计时的标签，如「剩 45 天」。
	// 空串表示没填到期日 —— 那就不显示这个标签（没填 ≠ 永久）。
	ExpireText string
	ExpireLv   string
	RenewText  string // 固定续费标签，未填写时显示「¥- /年」。
	RenewEmpty bool   // 空值与已填写资料使用不同的标签样式。

	// ---- 三网延迟 + 流量（卡片上那两行）----
	//
	// 三网延迟用**最近一轮探测**的结果，跟详情页口径一致 ——
	// 不一致的话卡片写 45ms、点进去写 82ms，站长会以为有一个是错的。
	Net3 []net3Cell
	// 流量是**估算值**（窗口平均速率 × 时长），不是精确计数。
	// 卡片上够用；要精确得另存累计差值。
	TodayUp, TodayDown string
	MonthUp, MonthDown string
	// Specs 是卡片上那行静态规格（CPU 型号/内存/Swap/虚拟化/加速）
	Specs []specItem

	// ---- 卡片底部的互动数据 ----
	VoteUp, VoteDown int
	MineUp, MineDown bool
	Articles         int // 文章数
	Comments         int // 已审核的评论数
}

// RegionLabel 给卡片上的地区文案兜底，避免模板里写三层 if。
// 导出是因为模板要直接取这个字段名。
func (c *nodeCard) RegionLabel() string {
	if r := strings.TrimSpace(c.Region); r != "" {
		return r
	}
	if s := strings.TrimSpace(c.Country); s != "" {
		return s
	}
	return "未知地区"
}

// flagCode 决定一台小鸡挂哪面旗：先看显式设置的 country 字段，
// 认不出来再从 region 里猜（比如 region 写着「美国 · 洛杉矶」）。
func flagCode(n *model.Node) string {
	if c := flags.Normalize(n.Country); c != "" {
		return c
	}
	return flags.Normalize(n.Region)
}

// commentView 是模板用的评论：原评论 + 它自己的票数 + 回复对象的昵称。
type commentView struct {
	model.Comment
	Votes   model.VoteCount
	ReplyTo string
}

// summaryRank 给卡片摘要里的各段排优先级（数字小的排前面）。
//
// IP 质量放最前 —— 买家扫一眼卡片最先想知道的是"这 IP 干不干净、
// 是不是机房"，而不是跑了多少 IOPS。
func summaryRank(part string) int {
	switch {
	case strings.HasPrefix(part, "回程"):
		return 0 // 线路在上
	case strings.Contains(part, "IP质量"):
		return 1 // IP 在下
	}
	return 2
}

// articleView 是详情页上的一篇文章。
//
// 列表里**只放标题和摘要** —— 全文渲染成 HTML 塞进弹窗。
// 不把全文直接铺在页面上：一篇几千字，五篇就是几万字，
// 打开详情页要等半天、滚动条长得没法用。
type articleView struct {
	ID      string
	Title   string
	Summary string
	When    string
}

// specItem 是卡片上「规格」行的一格：标签 + 值 + 可选的状态色。
type specItem struct {
	Label string
	Value string
	Level string // "" | ok | warn | bad —— 给加速/NAT 这类可好可坏的项上色
	// Full 表示独占整行**并且高亮**（浅底 + 加粗）—— 只给 CPU 型号用。
	Full bool
	// Wide 表示独占整行但**不高亮**。给 Swap 和系统这类在半栏容易挤坏的值用：
	// 系统名还要留位置给发行版图标；独占整行可以保持完整的一行。
	Wide bool
	// Icon 是值前面那个小图标的 SVG path 数据（目前只有「系统」用得到）。
	Icon string
	// IconColor 是发行版品牌色；路径仍然内嵌，页面无需依赖外部图标服务。
	IconColor string
	// Tip 是悬停提示。给那些"卡片上放不下、但丢掉又可惜"的解释用 ——
	// 比如 NAT 的完整含义。**卡片上先求短，长解释挪到悬停**。
	Tip string
	// Bare 表示不要标签行、只显示值 —— CPU 型号用它（boss 2026-10-11：
	// "CPU 这几个字删了，内容型号保留"）。型号本身就是一行小标题，
	// 再挂个"CPU"标签等于把同一句话说两遍。
	Bare bool
}

// nodeSpecs 组装卡片上那行静态规格。
//
// 大多数是静态规格；Swap 使用量/总量来自实时指标，随资源条一起更新。
// 规格单独成行，便于卡片上快速浏览。
//
// 只显示拿得到的：老 agent 没上报的字段（tcp_cc 等）直接不显示，
// 不摆一个空值占位置。
func nodeSpecs(n model.Node, m *model.Metrics) []specItem {
	var out []specItem
	add := func(label, value, level string) {
		if strings.TrimSpace(value) != "" {
			out = append(out, specItem{Label: label, Value: value, Level: level})
		}
	}

	// CPU 型号单独占一整行、放最前面。
	//
	// 它比其它字段长得多（"Intel Xeon Platinum 8272CL"），塞在双栏里
	// 只会显示半截 —— boss 反馈过两次。独占一行才排得开。
	//
	// 不挂"CPU"标签（Bare）：型号本身已经是小标题，标签是废话。
	if m := shortCPUModel(n.CPUModel); m != "" {
		out = append(out, specItem{Value: m, Full: true, Bare: true})
	}
	// 原来的"CPU"行改成核心数：型号已经单独一行了，
	// 这里再写一遍 CPU 只会让人以为有两块 CPU。
	if n.CPUCores > 0 {
		add("核心数", fmt.Sprintf("%d 核", n.CPUCores), "")
	}
	if n.MemTotal > 0 {
		add("内存", humanBytes(float64(n.MemTotal)), "")
	}
	// Swap：小鸡上有没有 swap 很关键 —— 内存爆了是靠它兜底的。
	// 只显示已用 / 总量，节省卡片空间。
	if m != nil && m.Mem.SwapTotal > 0 {
		used := float64(m.Mem.SwapUsed) / float64(m.Mem.SwapTotal) * 100
		lv := ""
		if used >= 50 {
			lv = "warn"
		}
		// Swap 独占整行；空间不足时由前端自适应缩小字号，
		// 塞半栏会被截成 "104.0 MB / 1024" —— boss 反馈过两次。
		out = append(out, specItem{
			Label: "Swap", Level: lv, Wide: true,
			Value: fmt.Sprintf("%s / %s",
				humanBytes(float64(m.Mem.SwapUsed)),
				humanBytes(float64(m.Mem.SwapTotal))),
		})
	} else if m != nil {
		add("Swap", "无", "")
	}
	if n.DiskTotal > 0 {
		add("磁盘", humanBytes(float64(n.DiskTotal)), "")
	}
	if n.OS != "" {
		// 系统名前面挂个发行版图标 —— 一眼能认出是哪家，
		// 比读 "Debian GNU/Linux 13 (trixie)" 快得多。
		out = append(out, specItem{Label: "系统", Value: n.OS, Wide: true, Icon: osIcon(n.OS), IconColor: osIconColor(n.OS)})
	}
	if n.Virt != "" {
		add("虚拟化", virtLabel(n.Virt), "")
	}
	// NAT 不再进规格表（boss 2026-10-11："NAT 不要显示在网络里，
	// 网络这一项删掉"）。它挪到了卡片标签行当**固定 tag** 显示 ——
	// NAT 和"大盘鸡"这类自定义标签是同一性质的信息（机器属性），
	// 而规格表只留"这台机器有多大、多快"的硬指标。
	// 标签渲染见 home.html 的 .tags，样式是 .tag.nat。
	// TCP 加速：BBR 是 VPS 圈最常被问的一项
	if n.TCPCC != "" {
		lv := ""
		switch strings.ToLower(n.TCPCC) {
		case "bbr":
			lv = "ok"
		case "cubic":
			lv = "warn" // 内核默认值，没优化过
		}
		v := strings.ToUpper(n.TCPCC)
		// BBR 建议配 fq；光有 bbr 没配 fq 效果打折扣，所以一起显示
		if n.TCPQdisc != "" {
			v += " + " + n.TCPQdisc
		}
		add("加速", v, lv)
	}
	if m != nil {
		if m.Host.Uptime > 0 {
			add("运行", FmtDuration(m.Host.Uptime), "")
		}
		// 负载 = 1 分钟平均负载（/proc/loadavg），统计"在跑 + 排队等 CPU"的进程数。
		// 1 核机器上超过 1 就是在排队了，所以分档线按核数换算。
		lv := ""
		cores := float64(n.CPUCores)
		if cores < 1 {
			cores = 1
		}
		switch per := m.CPU.Load1 / cores; {
		case per >= 1:
			lv = "bad"
		case per >= 0.7:
			lv = "warn"
		}
		add("负载", fmt.Sprintf("%.2f", m.CPU.Load1), lv)
	}
	return out
}

// shortCPUModel 把 CPU 型号洗成人能一眼扫完的样子。
//
// 原始值长这样：`Intel(R) Xeon(R) Platinum 8272CL CPU @ 2.60GHz`
// —— 卡片只有 300px 宽，原样放要占三行还全是噪音。
// 去掉 (R)/(TM) 这类商标噪音和尾部的"CPU @ 2.60GHz"（主频卡上别处也有意义）。
func shortCPUModel(m string) string {
	if m == "" {
		return ""
	}
	m = strings.NewReplacer("(R)", "", "(TM)", "", "(tm)", "").Replace(m)
	// 砍掉 " CPU @ 2.60GHz" 这一段
	if i := strings.Index(m, " CPU @"); i > 0 {
		m = m[:i]
	}
	// AMD 的写法是 " @ 2.9GHz"，同理砍掉
	if i := strings.Index(m, " @ "); i > 0 {
		m = m[:i]
	}
	fields := strings.Fields(m)
	clean := fields[:0]
	for _, field := range fields {
		if strings.EqualFold(strings.Trim(field, "(),"), "processor") {
			continue
		}
		clean = append(clean, field)
	}
	return strings.Join(clean, " ")
}

// virtLabel 把虚拟化类型说成人话。
func virtLabel(v string) string {
	switch strings.ToLower(v) {
	case "kvm":
		return "KVM"
	case "openvz":
		return "OpenVZ"
	case "lxc":
		return "LXC"
	case "docker":
		return "Docker"
	case "none":
		return "独服（无虚拟化）"
	}
	return v
}

// nodeTaskView 是详情页上一条测试记录。
type nodeTaskView struct {
	Title     string
	Kind      string
	Status    string // queued | running | done | failed
	When      string
	Summary   string
	Error     string
	ElapsedS  int64 // 秒。模板里的 fmtDur 收的就是秒，别传毫秒
	Items     []nodeTaskItem
	HasResult bool
}

// nodeTaskItem 是测试里的一项（磁盘 / CPU / IP…）。
type nodeTaskItem struct {
	Label  string
	OK     bool
	Err    string
	Fields [][2]string
}

// rangeOpt 是详情页走势图的一个时间窗选项。
type rangeOpt struct {
	Key   string
	Label string
}

// rangeOptions 是走势图可切换的时间窗（顺序即按钮顺序）。
// 详情页「历史趋势」和首页「总览」共用这一套 —— 按钮上写什么、
// 解析认哪些 key，两处永远一致。
var rangeOptions = []rangeOpt{
	{"1h", "1 小时"},
	{"6h", "6 小时"},
	{"12h", "12 小时"},
	{"1d", "1 天"},
	{"7d", "7 天"},
	{"30d", "30 天"},
}

// rangeWindow 是解析好的时间窗：查询跨度 + 最多几个点。
type rangeWindow struct {
	key       string
	dur       time.Duration
	maxPoints int
}

// netqWindow 把 ?range= 解析成时间窗。认不出来的值一律退回 1 小时 ——
// 走势图宁可按默认画，也不能因为一个错参数就空着。
//
// maxPoints 是给 store.History 的采样上限：跨度超过 6 小时它会自动改读
// metrics_5m 汇总表，再把桶按 GROUP BY 归并到 maxPoints 以内。
func netqWindow(key string) rangeWindow {
	switch key {
	case "6h":
		return rangeWindow{"6h", 6 * time.Hour, 180}
	case "12h":
		return rangeWindow{"12h", 12 * time.Hour, 180}
	case "1d":
		return rangeWindow{"1d", 24 * time.Hour, 288}
	case "7d":
		return rangeWindow{"7d", 7 * 24 * time.Hour, 336}
	case "30d":
		return rangeWindow{"30d", 30 * 24 * time.Hour, 360}
	default:
		return rangeWindow{"1h", time.Hour, 180}
	}
}

type nodePageData struct {
	Theme    themeView
	SiteName string
	Node     *model.Node
	Latest   *model.Metrics
	Profile  *model.NodeProfile
	CPUPct   float64
	MemPct   float64
	DiskPct  float64
	Points   []pointAlias
	Year     int

	Votes        model.VoteCount // 这台小鸡的赞/踩
	CommentCount int

	CommentErr string // 提交留言失败的原因
	CommentOK  bool   // 刚提交成功
	CommentOn  bool   // 是否开放留言
	IsAdmin    bool   // 主人视角：能看到联系方式并审核

	// Clocks 是主机时钟。跟 Owner 一起进顶栏（ownerchip partial），
	// 详情页也要显示。
	Clocks []clockZone

	// ---- 走势图时间窗（?range=1h|1d|7d|30d）----
	// Range 是当前生效的窗（用于高亮按钮）；RangeOpts 是全部可选项。
	Range     string
	RangeOpts []rangeOpt

	NetQ *NetQSummary // 网络质量（三网分省探测），还没有数据时为 nil

	// Tasks 是最近跑过的测试（硬件/磁盘/CPU/IP/线路…），按时间倒序。
	// 卡片上只显示一行摘要，完整结果在这里展开看。
	Tasks []nodeTaskView
	// Uptime 是状态时间轴（近 30 天，一天一格）
	Uptime *uptimeSummary

	FlagCode string // 详情页标题上的国旗

	// Articles 是这台机器的文章列表。
	// 列表里只显示标题+摘要，全文在弹窗里 —— 不把全文铺在页面上，
	// 几篇几千字的文章会让详情页变得又长又难滚。
	ArticleCount int

	// Owner 是站长名片。挂在详情页底部，让"这台小鸡是谁在卖"有归属——
	// 后台那句提示一直写着"会显示在首页顶部与每台小鸡的页面底部"，
	// 但详情页此前根本没渲染它，属于空头承诺。
	Owner ownerCard
}

// pointAlias 让模板拿到历史点，避免本包直接依赖 store 的内部类型。
type pointAlias struct {
	Ts      int64
	CPU     float64
	MemUsed int64
	NetUp   int64
	NetDown int64
}

// modComment 是后台审核列表里的一行：评论 + 它属于哪台小鸡。
// net3Cell 是卡片上「三网延迟」里的一格。
type net3Cell struct {
	Label       string // 电信 / 联通 / 移动
	MS          string
	Unavailable bool // 最近窗口内该运营商的探测点都没有响应
	HasLoss     bool
	Level       string  // "" | ok | warn | bad —— 越低越好，判据与占用率相反
	Loss        float64 // 最近一轮丢包率 %
	SparkLine   string  // 最近几轮延迟的波动路径（空 = 数据不够画）
	SparkArea   string
}

// nodeComments 是一台机器下的全部评论，按小鸡分组用。
type nodeComments struct {
	NodeID   string
	Name     string
	Slug     string
	Comments []modComment
}

type adminNodeGroup struct {
	Name  string
	Nodes []model.Node
}

func groupAdminNodes(nodes []model.Node) []adminNodeGroup {
	groups := make([]adminNodeGroup, 0)
	index := make(map[string]int)
	for _, node := range nodes {
		name := strings.TrimSpace(node.Group)
		if name == "" {
			name = "未分组"
		}
		i, ok := index[name]
		if !ok {
			i = len(groups)
			index[name] = i
			groups = append(groups, adminNodeGroup{Name: name})
		}
		groups[i].Nodes = append(groups[i].Nodes, node)
	}
	return groups
}

type modComment struct {
	model.Comment
	NodeName string
	NodeSlug string
	Score    int
}

type adminData struct {
	Theme    themeView
	SiteName string
	// SelectedID 是后台主题页"正在编辑哪套主题"（点卡片切换，不等于启用）：
	// 壁纸面板按它取数据，站点默认主题不受影响。
	SelectedID     string
	Nodes          []model.Node
	ArticleCounts  map[string]int
	NodeGroups     []adminNodeGroup
	Groups         []store.NodeGroup
	GroupMsg       string
	ArticleDeleted bool
	SelectedGroup  string
	TaskMsg        string
	TaskMsgError   bool
	Tokens         []installToken
	HubURL         string
	BootPass       string

	Comments []modComment
	// BuyIntents 是站内消息：访客购买咨询会先保存，再尝试 Telegram / Webhook 推送。
	// 即使外部渠道未配置或发送失败，管理员仍可在后台查看。
	SiteMessages        []store.SiteMessage
	SiteMessagesTotal   int
	InboxUnread         int
	InboxError          bool
	InboxSelectionError bool
	InboxDeleted        int
	InboxPage           int
	InboxPages          int
	InboxPrevPage       int
	InboxNextPage       int
	InboxHasPrev        bool
	InboxHasNext        bool
	ExpiryReminderOn    bool
	ExpiryReminderDays  int
	ExpiryReminderMsg   string
	ExpiryReminderBad   bool

	// CommentsByNode 是按小鸡分好组的评论。
	// 一个平铺的长表格里，"这条评的是哪台机器"要靠单独一列去认，
	// 评论一多根本对不上 —— 分组之后每台一块。
	CommentsByNode []nodeComments
	PendingN       int
	CommentOn      bool
	AutoApprove    bool

	// HubLat / HubLon 是主机（面板所在机器）的经纬度，首页地球用它当航线中心。
	HubLat string
	HubLon string

	// ---- 下面这几个是后台「主题」页签的数据 ----
	// 主题面板就挂在 /admin 里（和「节点」「告警」一样是个页签），
	// 所以这些数据每次进后台都要带上，不再是单独一页的专属。
	Themes []themeCard
	// Msg 是主题操作后的提示（导入成功 / 失败原因）。
	Msg string
	// ActiveID 是当前生效的主题 ID。
	ActiveID string
	// TokenNames 列出可用的 CSS 变量名，方便管理员复制内置主题去改。
	TokenNames []string
	// Wallpaper 是站点壁纸面板的数据（上传 / URL / 清除）。
	Wallpaper wallpaperView

	// ---- 三网分省探测（后台「节点」页签里的一块）----
	// NetQProvinces 是当前生效的探测范围（省份名，已校验）。
	NetQProvinces []string
	// NetQProvOpts 是勾选框列表：内置表里的全部省份 + 是否选中。
	// （模板没有 in 之类的函数，选项在 Go 侧拼好再交给模板。）
	NetQProvOpts []netqProvOption
	// NetQTargets 是当前范围的探测点数（省数 × 3 网）。
	NetQTargets int
	// NetQInterval 是探测间隔（分钟）：0=连续（默认）、-1=关、正数=分钟。
	NetQInterval int

	OwnerName    string
	OwnerVisible bool
	// OwnerDisplayName 用于后台标题栏展示站长的个性名，不是登录账户名。
	OwnerDisplayName string
	OwnerBio         string
	OwnerAvatar      string

	// 告警
	AlertRules       []model.AlertRule
	AlertEvents      []alertEventView
	NotifyOn         bool
	NotifyChat       string
	NotifyToken      string // 打码后的 bot token
	NotifyQuiet      string
	NotifyQuietStart int
	NotifyQuietEnd   int
	NotifySilent     bool
	WebhookOn        bool
	WebhookURL       string

	// Profiles 是每台机器的名片（价格 / 到期日）。
	//
	// 放在这里而不是塞进 model.Node：那两样是"站长填的"，不是机器上报的，
	// 本来就存在 node_profile 里。模板按节点 ID 取。
	Profiles map[string]*model.NodeProfile

	// ---- 面板主机自身（原来在 /dashboard 上，那个页面删掉后并到这儿）----
	Host        *model.Metrics
	HostNodeID  string
	HostMemPct  float64
	HostDiskPct float64
	HostNote    string

	CC []ccOption // 国家/地区代码候选，给输入框做 datalist

	AdminUser  string
	AccountMsg string
	AccountBad bool
}

// ccOption 是国旗选择器的一项。
type ccOption struct {
	Code  string
	Label string
}

// alertEventView 后台告警事件一行：事件 + 小鸡名。
type alertEventView struct {
	model.AlertEvent
	NodeName string
}

// notifyView 是后台通知表单要展示的配置（token 已打码）。
type notifyView struct {
	TelegramEnabled bool
	TelegramChatID  string
	TelegramToken   string
	TelegramSilent  bool
	QuietStart      int
	QuietEnd        int
	WebhookEnabled  bool
	WebhookURL      string
}

// ---- 首页 ----

func (h *Hub) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	h.renderHome(w, r, "")
}

// handleSearch 搜索小鸡：/search?q=关键词。
func (h *Hub) handleSearch(w http.ResponseWriter, r *http.Request) {
	h.renderHome(w, r, strings.TrimSpace(r.URL.Query().Get("q")))
}

type homeCardSeriesResponse struct {
	Line string `json:"line"`
	Area string `json:"area"`
	Peak string `json:"peak"`
	Info string `json:"info"`
}

type cardSeriesCacheEntry struct {
	expires  time.Time
	from, to int64
	series   map[string][]store.NetPoint
}

// handleHomeCardSeries refreshes only one public node card's sparkline. The
// overview range remains page-wide; this endpoint keeps per-card ranges local.
func (h *Hub) handleHomeCardSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	key := r.URL.Query().Get("range")
	if id == "" {
		http.Error(w, "missing node id", http.StatusBadRequest)
		return
	}
	validRange := false
	for _, opt := range rangeOptions {
		if key == opt.Key {
			validRange = true
			break
		}
	}
	if !validRange {
		http.Error(w, "invalid range", http.StatusBadRequest)
		return
	}
	nodes, err := h.store.ListNodes(false)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	public := false
	for _, n := range nodes {
		if n.ID == id {
			public = true
			break
		}
	}
	if !public {
		http.NotFound(w, r)
		return
	}
	win := netqWindow(key)
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	cacheKey := key + "\x00" + strings.Join(ids, "\x00")
	h.cardSeriesMu.Lock()
	if h.cardSeriesCache == nil {
		h.cardSeriesCache = make(map[string]cardSeriesCacheEntry)
	}
	entry, ok := h.cardSeriesCache[cacheKey]
	if !ok || time.Now().After(entry.expires) {
		to := time.Now().UnixMilli()
		from := to - win.dur.Milliseconds()
		series, queryErr := h.store.NetSeriesByNode(ids, from, to, dashMaxPoints)
		if queryErr != nil {
			h.cardSeriesMu.Unlock()
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		entry = cardSeriesCacheEntry{expires: time.Now().Add(5 * time.Second), from: from, to: to, series: series}
		for k, v := range h.cardSeriesCache {
			if time.Now().After(v.expires) {
				delete(h.cardSeriesCache, k)
			}
		}
		if len(h.cardSeriesCache) >= 8 {
			h.cardSeriesCache = make(map[string]cardSeriesCacheEntry)
		}
		h.cardSeriesCache[cacheKey] = entry
	}
	h.cardSeriesMu.Unlock()
	series, from, to := entry.series, entry.from, entry.to
	points := series[id]
	line, area := sparkPathsInWindowScale(points, from, to, sparkSeriesWindowMax(series, from, to))
	response := homeCardSeriesResponse{Line: line, Area: area}
	if len(points) == 0 {
		response.Info = "暂无数据"
	} else if len(points) < 2 {
		response.Info = "数据不足"
	} else if line == "" {
		response.Info = "暂无流量"
	}
	var peak int64
	for _, p := range points {
		if v := int64(p.Up); v > peak {
			peak = v
		}
		if v := int64(p.Down); v > peak {
			peak = v
		}
	}
	response.Peak = FmtRate(peak)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(response)
}

func (h *Hub) renderHome(w http.ResponseWriter, r *http.Request, query string) {
	nodes, err := h.store.ListNodes(false)
	// 到期倒计时要用名片里的 expire_at。**一次批量查**，别在循环里逐个查。
	profiles, _ := h.store.ProfilesByNode()

	// 卡片上的三网延迟与流量。同样是批量 —— 一屏几十张卡，
	// 每张各查一次就是几百次往返。
	netq, _ := h.store.NetQByNode()
	now := time.Now()
	today, _ := h.store.TrafficByNodeSince(dayStartMS(now))
	month, _ := h.store.TrafficByNodeSince(monthStartMS(now))
	if err != nil {
		log.Printf("[hub] 读取节点失败: %v", err)
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}
	if query != "" {
		nodes, err = h.store.SearchNodes(query, false)
		if err != nil {
			log.Printf("[hub] 搜索节点失败: %v", err)
		}
	}
	rangeWin := netqWindow(r.URL.Query().Get("range"))
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	snap := h.latestSnapshot()

	// 节点卡片和总览共用所选时间范围。一次批量查询并按时间分桶，
	// 避免逐节点读取，也避免把少量样本拉伸到整条图表宽度。
	seriesTo := now.UnixMilli()
	seriesFrom := seriesTo - rangeWin.dur.Milliseconds()
	series, _ := h.store.NetSeriesByNode(ids, seriesFrom, seriesTo, dashMaxPoints)
	sparkScale := sparkSeriesWindowMax(series, seriesFrom, seriesTo)

	// 卡片底部的互动数据，**一次批量取完**再按节点分发。
	// 每张卡各查三次的话，几十台就是一二百次往返。
	voter := h.voterID(w, r)
	voteAll, _ := h.store.VoteCounts("node", ids, voter)
	artCnt, _ := h.store.CountByNode("node_articles", "")
	cmtCnt, _ := h.store.CountByNode("comments", "status = 'approved'")

	cards := make([]nodeCard, 0, len(nodes))
	online := 0
	for _, n := range nodes {
		m := snap[n.ID]
		c := nodeCard{Node: n, Latest: m, FlagCode: flagCode(&n), SparkRange: dashSpanPrefix(rangeWin.key)}
		if v, ok := voteAll[n.ID]; ok {
			c.VoteUp, c.VoteDown = v.Up, v.Down
			c.MineUp, c.MineDown = v.MineUp(), v.MineDown()
		}
		c.Articles, c.Comments = artCnt[n.ID], cmtCnt[n.ID]
		if m != nil {
			c.CPUPct = m.CPU.Usage
			c.MemPct = Pct(float64(m.Mem.Used), float64(m.Mem.Total))
			c.DiskPct = Pct(float64(m.Disk.Used), float64(m.Disk.Total))
		}
		// 指标配色档位：颜色表达状态，别让人去算数字
		c.CPULv, c.MemLv, c.DiskLv = levelClass(c.CPUPct), levelClass(c.MemPct), levelClass(c.DiskPct)
		if m != nil && n.CPUCores > 0 {
			// 负载按核数归一化，否则 8 核机永远显示很低，等于没意义
			c.LoadPct = math.Min(100, m.CPU.Load1/float64(n.CPUCores)*100)
			c.LoadLv = levelClass(c.LoadPct)
		}
		if m != nil {
			points := series[n.ID]
			c.SparkLine, c.SparkArea = sparkPathsInWindowScale(points, seriesFrom, seriesTo, sparkScale)
			switch {
			case len(points) == 0:
				c.SparkInfo = "暂无数据"
			case len(points) < 2:
				c.SparkInfo = "数据不足"
			case c.SparkLine == "":
				c.SparkInfo = "暂无流量"
			}
			// 曲线量级：取窗口内的上下行峰值当刻度标注
			for _, p := range points {
				if up := int64(p.Up); up > c.SparkPeak {
					c.SparkPeak = up
				}
				if down := int64(p.Down); down > c.SparkPeak {
					c.SparkPeak = down
				}
			}
			if m.NetQ != nil && m.NetQ.HubLatencyMS > 0 {
				c.LatencyPct = latencyPct(m.NetQ.HubLatencyMS)
				c.LatencyText = fmt.Sprintf("%.0fms", m.NetQ.HubLatencyMS)
				// 延迟越低越好，所以档位判据是反的：通到主机很快才算"好"
				if c.LatencyPct >= 66 {
					c.LatLv = "bad"
				} else if c.LatencyPct >= 33 {
					c.LatLv = "warn"
				} else {
					c.LatLv = "ok"
				}
			} else {
				c.LatencyText = "—"
				c.LatLv = "ok"
			}
		} else {
			c.LatencyText = "—"
		}
		h.populateTaskCard(&c)
		c.Specs = nodeSpecs(n, m)
		c.Uptime = h.loadUptime(n.ID)
		// 卡片标签行只有一行：老节点可能存了超过上限的标签，
		// 显示时也截一刀，别让历史数据把固定槽挤掉。
		if len(c.Node.Tags) > maxNodeTags {
			c.Node.Tags = c.Node.Tags[:maxNodeTags]
		}
		// 到期倒计时的标签。没填到期日就空着，不显示
		// （没填 ≠ 永久，站长可能只是还没填）。
		if pr := profiles[n.ID]; pr != nil {
			c.ExpireText, c.ExpireLv = expireTag(pr.ExpireAt)
			c.RenewText = formatPrice(pr.Price)
		}
		if c.RenewText == "" {
			c.RenewEmpty = true
			c.RenewText = "¥- /年"
		}

		// 三网延迟：固定按 电信 → 联通 → 移动 排，缺哪个就少哪个
		c.Net3 = net3Cells(netq[n.ID])

		// 流量：本日 / 本月。估算值，见 store.TrafficByNodeSince 的注释。
		if t, ok := today[n.ID]; ok {
			c.TodayUp, c.TodayDown = FmtBytes(t[0]), FmtBytes(t[1])
		}
		if t, ok := month[n.ID]; ok {
			c.MonthUp, c.MonthDown = FmtBytes(t[0]), FmtBytes(t[1])
		}
		c.Lat, c.Lon, c.HasCoord = resolveCoord(n.Country, n.Region, n.City)
		if n.Online {
			online++
		}
		cards = append(cards, c)
	}

	regions := distinctRegions(nodes)
	globe := h.buildGlobe(cards)
	globeJSON, err := json.Marshal(globe)
	if err != nil {
		// 序列化失败不该让整个首页挂掉，退化成"不显示地球"
		log.Printf("[hub] 地球数据序列化失败: %v", err)
		globe = globeData{}
		globeJSON = []byte(`{"places":[]}`)
	}
	dash := h.buildDashboard(rangeWin)
	// 波动图数据：字段名压到最短（1 分钟一个点、60 个点，
	// 全写成 {timestamp, up_bytes_per_sec} 会让 HTML 白胖一圈）
	dashJSON, err := json.Marshal(ratePointsJSON(dash.Points))
	if err != nil {
		dashJSON = []byte("[]")
	}
	// CPU / 内存占用率图：同一窗口的另一组点，单独一个数据块
	resJSON, err := json.Marshal(resPointsJSON(dash.ResPoints))
	if err != nil {
		resJSON = []byte("[]")
	}

	// 主题是每个请求都要算一次的（访客 cookie / 站点默认 / 深浅色），
	// 这里提出来复用。
	tv := h.themeFor(r)
	groups, groupErr := h.store.ListNodeGroups()
	if groupErr != nil {
		log.Printf("[hub] 首页读取节点分组失败: %v", groupErr)
	}
	visitors := h.recordHomeVisitor(w, r)
	h.render(w, "home.html", &homeData{
		Theme:          tv,
		SiteName:       h.cfg.SiteName,
		Nodes:          cards,
		GroupFilters:   makeHomeGroupFilters(cards, groups),
		Online:         online,
		Total:          len(nodes),
		Offline:        len(nodes) - online,
		Visitors:       visitors,
		Year:           time.Now().Year(),
		Query:          query,
		Owner:          h.owner(),
		Hero:           h.buildHero(r, len(nodes), online),
		Regions:        regions,
		OwnerEmptyHint: h.adminAuthed(r) && !h.owner().Has,
		Clocks:         homeClocks(),
		Globe:          globe,
		GlobeJSON:      template.JS(globeJSON),
		GlobeLandURL:   staticAssetURL("land.bin"),
		// 仪表盘数据并在首页上用 —— 两边同一个函数，数字永远一致
		Dash:     dash,
		DashJSON: template.JS(dashJSON),
		ResJSON:  template.JS(resJSON),
	}, r)
}

const homeVisitorCookie = "kokoro_visitor"

// recordHomeVisitor counts unique browser cookies that have opened the home
// panel. This is a lightweight UV estimate: no raw IP or visit log is stored.
func (h *Hub) recordHomeVisitor(w http.ResponseWriter, r *http.Request) int64 {
	if r.Method != http.MethodGet {
		count, _ := h.store.SiteVisitorCount()
		return count
	}
	if c, err := r.Cookie(homeVisitorCookie); err == nil && validHomeVisitorID(c.Value) {
		count, err := h.store.SiteVisitorCount()
		if err != nil {
			log.Printf("[hub] 读取首页访客数失败: %v", err)
		}
		return count
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		log.Printf("[hub] 生成访客标识失败: %v", err)
		return 0
	}
	visitorID := hex.EncodeToString(id[:])
	count, err := h.store.IncrementSiteVisitor()
	if err != nil {
		log.Printf("[hub] 更新首页访客数失败: %v", err)
		return 0
	}
	secure := r.TLS != nil || h.cfg.BehindProxy
	http.SetCookie(w, &http.Cookie{
		Name: homeVisitorCookie, Value: visitorID, Path: "/",
		Expires: time.Now().AddDate(10, 0, 0), MaxAge: 10 * 365 * 24 * 60 * 60,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	return count
}

func validHomeVisitorID(value string) bool {
	if len(value) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

// buildHero 按当前主题的 layout 决定首页 Hero 的内容与显隐。
//
// 为什么 Hero 的文案不写进主题里：主题是"皮肤"，不该替站长决定站点叫什么。
// 主题只决定"要不要这块、什么形态"，标题与副标题从站点设置取。
func (h *Hub) buildHero(r *http.Request, total, online int) heroView {
	m := h.requestManifest(r)
	if m == nil {
		return heroView{}
	}
	hero := m.Layout.Home.Hero
	v := heroView{}
	if hero.Enabled == nil || !*hero.Enabled {
		return v
	}
	v.Enabled = true
	v.Title = h.cfg.SiteName
	v.Subtitle = strings.TrimSpace(hero.Subtitle)
	if v.Subtitle == "" {
		v.Subtitle = h.cfg.SiteTagline
	}
	v.ShowStats = pickBoolDefault(hero.ShowStats, true)
	return v
}

// distinctRegions 收集出现过的地区并去重，顺序按首次出现。
func distinctRegions(nodes []model.Node) []string {
	seen := make(map[string]struct{}, len(nodes))
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		k := strings.TrimSpace(n.Region)
		if k == "" {
			k = strings.TrimSpace(n.Country)
		}
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

// themeFor 是 render 之外单独取主题数据的便捷入口。
func (h *Hub) themeFor(r *http.Request) themeView {
	m := h.requestManifest(r)
	if m == nil {
		return themeView{}
	}
	return h.buildThemeView(r, m)
}

// ---- 站长名片：头像 + 签名 ----

// owner 读站长资料。没设置过昵称时，以管理员用户名作为初始个性名。
// owner 组装站长名片。
//
// Has 的判定是「站长到底填过什么」，而不是「渲染出来的东西和默认值像不像」。
// 早期版本用的是 `name != h.cfg.SiteName`，于是在后台把昵称填成和站点同名
// （很常见的操作）时名片会突然消失——明明填了却看不到，纯属给人添堵。
func (h *Hub) owner() ownerCard {
	rawName, _ := h.store.GetSetting("owner_name")
	bio, _ := h.store.GetSetting("owner_bio")
	avatar, _ := h.store.GetSetting("owner_avatar")
	visibleRaw, _ := h.store.GetSetting("owner_visible")

	name := strings.TrimSpace(rawName)
	if name == "" {
		name = h.adminUsername()
	}
	// 头像只接受站内路径或显式 http(s) 地址。
	//
	// ⚠️ 协议相对地址（`//evil.com/x.png`）必须单独挡：它的前缀确实是 `/`，
	// 看着像站内路径，但浏览器会把它解析成 `https://evil.com/x.png`——
	// 既绕过了"站内"这个约束，也让外部主机拿到访客 IP。
	if avatar != "" && !safeAvatarURL(avatar) {
		avatar = ""
	}
	hasProfile := strings.TrimSpace(rawName) != "" || strings.TrimSpace(bio) != "" || avatar != ""
	visible := visibleRaw == "1"
	// 兼容老面板：在新增开关前已填写的名片保持原来的公开状态。
	// 新安装没有资料时默认不显示；一旦管理员明确保存开关，后续以开关为准。
	if visibleRaw == "" {
		visible = hasProfile
	}
	return ownerCard{
		Name:   name,
		Bio:    bio,
		Avatar: avatar,
		// Has 表示资料是否填写，Visible 单独表示公开显示开关。
		Has:     hasProfile,
		Visible: visible,
	}
}

// safeAvatarURL 判断头像地址是否可安全地放进 <img src>。
//
// 只放行两类：
//   - 站内绝对路径 `/avatar?v=1`。**不接受** `//` 开头——
//     那是协议相对地址，浏览器会当成外站域名。
//   - 显式的 http:// 或 https:// 绝对地址。
//
// 其余（javascript:、data:、vbscript:、裸相对路径等）一律拒绝。
func safeAvatarURL(s string) bool {
	switch {
	case strings.HasPrefix(s, "//"):
		return false // 协议相对 = 外站，不是站内路径
	case strings.HasPrefix(s, "/"):
		return true
	case strings.HasPrefix(s, "http://"), strings.HasPrefix(s, "https://"):
		return true
	default:
		return false
	}
}

// handleAdminProfile 保存站长资料，头像以文件上传的方式落到数据目录。
func (h *Hub) handleAdminProfile(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		// 抽干 body 再跳转：否则带 body 的 POST 会因未读数据触发 RST，
		// 连 303 都发不出去，浏览器只看到"连接被重置"。
		drainBody(w, r)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := r.ParseMultipartForm(4 << 20); err != nil { // 头像限 4MB
		http.Error(w, "上传内容太大或格式不对", http.StatusRequestEntityTooLarge)
		return
	}
	_ = h.store.SetSetting("owner_name", clampRunes(strings.TrimSpace(r.FormValue("owner_name")), 64))
	_ = h.store.SetSetting("owner_bio", clampRunes(strings.TrimSpace(r.FormValue("owner_bio")), 200))
	ownerVisible := "0"
	if r.FormValue("owner_visible") == "1" {
		ownerVisible = "1"
	}
	_ = h.store.SetSetting("owner_visible", ownerVisible)

	if r.FormValue("avatar_clear") == "1" {
		_ = h.store.SetSetting("owner_avatar", "")
		_ = h.removeAvatar()
	} else if _, hdr, err := r.FormFile("avatar"); err == nil && hdr != nil && hdr.Size > 0 {
		if hdr.Size > 2<<20 {
			http.Error(w, "头像不能超过 2MB", http.StatusRequestEntityTooLarge)
			return
		}
		ext, ok := imageExt(hdr.Filename)
		if !ok {
			http.Error(w, "头像只支持 png / jpg / webp / gif", http.StatusBadRequest)
			return
		}
		if dst, err := h.saveAvatar(hdr, ext); err == nil {
			_ = h.store.SetSetting("owner_avatar", dst)
		} else {
			log.Printf("[hub] 保存头像失败: %v", err)
		}
	}
	http.Redirect(w, r, "/admin#profile", http.StatusSeeOther)
}

// imageExt 按文件名后缀判定类型，只放行常见位图（不接受 svg，避免脚本注入）。
func imageExt(name string) (string, bool) {
	lower := strings.ToLower(name)
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".webp", ".gif"} {
		if strings.HasSuffix(lower, ext) {
			if ext == ".jpeg" {
				return ".jpg", true
			}
			return ext, true
		}
	}
	return "", false
}

// saveAvatar 把上传的头像写到数据目录，旧文件先删掉。返回可访问的 URL。
func (h *Hub) saveAvatar(fh *multipart.FileHeader, ext string) (string, error) {
	if h.cfg.DataDir == "" {
		return "", errors.New("未配置数据目录")
	}
	f, err := fh.Open()
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := os.MkdirAll(h.cfg.DataDir, 0o755); err != nil {
		return "", err
	}
	_ = h.removeAvatar()
	dst := filepath.Join(h.cfg.DataDir, "avatar"+ext)
	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	// 版本参数绕开浏览器缓存
	return fmt.Sprintf("/avatar?v=%d", time.Now().UnixMilli()), nil
}

func (h *Hub) removeAvatar() error {
	if h.cfg.DataDir == "" {
		return nil
	}
	var last error
	for _, ext := range []string{".png", ".jpg", ".webp", ".gif"} {
		if err := os.Remove(filepath.Join(h.cfg.DataDir, "avatar"+ext)); err != nil && !os.IsNotExist(err) {
			last = err
		}
	}
	return last
}

// handleAvatar 直接吐数据目录里的头像文件（不在 embed 里，要单独开个路由）。
func (h *Hub) handleAvatar(w http.ResponseWriter, r *http.Request) {
	if h.cfg.DataDir == "" {
		http.NotFound(w, r)
		return
	}
	for _, p := range []struct{ ext, ctype string }{
		{".png", "image/png"}, {".jpg", "image/jpeg"},
		{".webp", "image/webp"}, {".gif", "image/gif"},
	} {
		path := filepath.Join(h.cfg.DataDir, "avatar"+p.ext)
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		w.Header().Set("Content-Type", p.ctype)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(b)
		return
	}
	http.NotFound(w, r)
}

// ---- 小鸡详情页 ----

func (h *Hub) handleNodePage(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/n/"), "/")
	if rest == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	parts := strings.Split(rest, "/")
	slug := parts[0]

	// /n/<slug>/comment 与 /n/<slug>/vote 是写操作，其余一律当详情页渲染。
	if len(parts) > 1 && r.Method == http.MethodPost {
		switch parts[1] {
		case "comment":
			h.handleNodeComment(w, r, slug)
			return
		case "vote":
			h.handleNodeVote(w, r, slug)
			return
		}
	}
	// 抽屉的数据接口：卡片上点「文章 3」「评论 5」时按需拉。
	// 做成 JSON 而不是整页渲染，是因为评论可能上百条 ——
	// 全塞进首页会让首页体积翻好几倍，而绝大多数访客根本不点开。
	if len(parts) > 1 {
		switch parts[1] {
		case "netq":
			h.handleNodeNetQ(w, r, slug)
			return
		case "articles.json":
			h.handleNodeArticles(w, r, slug)
			return
		case "comments.json":
			h.handleNodeComments(w, r, slug)
			return
		}
		// 认不出的子路径直接回详情页，别渲染成 404
		http.Redirect(w, r, "/n/"+slug, http.StatusFound)
		return
	}
	h.renderNodePage(w, r, slug, "", false)
}

func (h *Hub) renderNodePage(w http.ResponseWriter, r *http.Request, slug, commentErr string, ok bool) {
	node, err := h.store.GetNodeBySlug(slug)
	if err != nil || node == nil {
		http.NotFound(w, r)
		return
	}
	if node.Visibility == model.VisibilityPrivate {
		http.NotFound(w, r)
		return
	}

	latest, _ := h.store.LatestMetrics(node.ID)
	profile, _ := h.store.GetProfile(node.ID)
	if profile == nil {
		profile = &model.NodeProfile{NodeID: node.ID}
	}

	isAdmin := h.adminAuthed(r)
	voter := h.voterID(w, r)

	data := nodePageData{
		SiteName:   h.cfg.SiteName,
		Node:       node,
		Latest:     latest,
		Profile:    profile,
		Year:       time.Now().Year(),
		CommentOn:  h.commentEnabled(),
		IsAdmin:    isAdmin,
		CommentErr: commentErr,
		CommentOK:  ok,
		FlagCode:   flagCode(node),
		Owner:      h.owner(),
		Clocks:     homeClocks(),
	}
	if latest != nil {
		data.CPUPct = latest.CPU.Usage
		data.MemPct = Pct(float64(latest.Mem.Used), float64(latest.Mem.Total))
		data.DiskPct = Pct(float64(latest.Disk.Used), float64(latest.Disk.Total))
	}

	// 历史走势的时间窗：1 小时（默认）/ 1 天 / 7 天 / 30 天。
	// 跨度超过 6 小时时 store.History 会自动改读 metrics_5m 汇总表
	// （原始表只留 24 小时），所以三种长窗都不用额外聚合。
	now := time.Now().UnixMilli()
	win := netqWindow(r.URL.Query().Get("range"))
	data.Range = win.key
	data.RangeOpts = rangeOptions
	pts, err := h.store.History(node.ID, now-win.dur.Milliseconds(), now, win.maxPoints)
	if err == nil {
		for _, p := range pts {
			data.Points = append(data.Points, pointAlias{
				Ts: p.Ts, CPU: p.CPU, MemUsed: p.MemUsed, NetUp: p.NetUp, NetDown: p.NetDown,
			})
		}
	}

	// 赞/踩：小鸡自己一票，每条评论各一票，一次查询批量取
	if counts, err := h.store.VoteCounts("node", []string{node.ID}, voter); err == nil {
		data.Votes = counts[node.ID]
	}
	data.CommentCount, _ = h.store.CountComments(node.ID, !isAdmin)
	data.NetQ = h.LoadNetQRange(node.ID, r.URL.Query().Get("netq_range"))
	data.Tasks = h.nodeTasks(node.ID, 6)
	data.Uptime = h.loadUptime(node.ID)
	data.ArticleCount, _ = h.store.CountArticles(node.ID)

	// 访问统计（粗粒度：PV 每次 +1）
	profile.PV++
	profile.UpdatedAt = time.Now().UnixMilli()
	_ = h.store.SaveProfile(profile)

	h.render(w, "node.html", &data, r)
}

// handleNodeNetQ renders the refreshable network-quality section at its selected range.
func (h *Hub) handleNodeNetQ(w http.ResponseWriter, r *http.Request, slug string) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 GET")
		return
	}
	node, err := h.store.GetNodeBySlug(slug)
	if err != nil || node == nil || node.Visibility == model.VisibilityPrivate {
		http.NotFound(w, r)
		return
	}
	sum := h.LoadNetQRange(node.ID, r.URL.Query().Get("range"))
	if sum == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(h.renderPartial("netq.html", sum)))
}

// ---- 留言与投票 ----

const (
	commentContentMax = 100
	commentContactMax = 20
)

// handleNodeComment 处理 /n/<slug>/comment 的提交。
func (h *Hub) handleNodeComment(w http.ResponseWriter, r *http.Request, slug string) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	node, err := h.store.GetNodeBySlug(slug)
	if err != nil || node == nil || node.Visibility == model.VisibilityPrivate {
		http.NotFound(w, r)
		return
	}
	if !h.commentEnabled() {
		if wantsJSON(r) {
			writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": "这台机器暂时关闭了留言"})
			return
		}
		http.Redirect(w, r, "/n/"+slug+"#comments", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		h.commentFail(w, r, slug, "表单解析失败")
		return
	}

	author := strings.TrimSpace(r.FormValue("author"))
	content := strings.TrimSpace(r.FormValue("content"))
	contact := strings.TrimSpace(r.FormValue("contact"))
	parentID := strings.TrimSpace(r.FormValue("parent_id"))
	author = clampRunes(author, 32)

	if content == "" {
		h.commentFail(w, r, slug, "留言内容不能为空")
		return
	}
	if len([]rune(content)) > commentContentMax {
		h.commentFail(w, r, slug, "留言内容最多 100 个字")
		return
	}
	if len([]rune(contact)) > commentContactMax {
		h.commentFail(w, r, slug, "联系方式最多 20 个字")
		return
	}
	ipHash := h.rateLimitIPHash(r)
	if author == "" {
		author = "匿名访客"
	}

	voter := h.voterID(w, r)
	if !h.limiter.allow("cm:"+voter, 15*time.Second) {
		h.commentRateLimited(w, r, slug, "发得太快了，歇 15 秒再来")
		return
	}

	status := model.CommentPending
	if h.autoApprove() {
		status = model.CommentApproved
	}
	c := &model.Comment{
		NodeID:    node.ID,
		ParentID:  parentID,
		Author:    author,
		Contact:   contact,
		Content:   content,
		Status:    status,
		IPHash:    ipHash,
		CreatedAt: time.Now().UnixMilli(),
	}
	added, err := h.store.AddCommentWithIPRateLimit(c, time.Now().Add(-perNodeWindow).UnixMilli(), perNodeSendMax)
	if err != nil {
		log.Printf("[hub] 保存留言失败: %v", err)
		h.commentFail(w, r, slug, "保存失败，稍后再试")
		return
	}
	if !added {
		h.commentRateLimited(w, r, slug, "同一 IP 对这台小鸡 5 分钟内最多留言 2 条，请稍后再试")
		return
	}
	_ = h.store.AddAudit("guest", "comment.add", node.ID, author)

	// 卡片上的评论弹窗走 JSON：提交完原地刷新列表，不跳页。
	// （详情页的老表单没有 JS 时仍退回 303。）
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true,
			// pending=true 表示要等站长审核才显示 —— 弹窗据此提示访客
			"pending": status == model.CommentPending,
		})
		return
	}
	http.Redirect(w, r, "/n/"+slug+"#comments", http.StatusSeeOther)
}

// commentFail 留言提交失败：fetch 请求回 JSON，普通表单退回详情页并带错误文案。
func (h *Hub) commentFail(w http.ResponseWriter, r *http.Request, slug, msg string) {
	if wantsJSON(r) {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": msg})
		return
	}
	h.renderNodePage(w, r, slug, msg, false)
}

func (h *Hub) commentRateLimited(w http.ResponseWriter, r *http.Request, slug, msg string) {
	if wantsJSON(r) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"ok": false, "error": msg})
		return
	}
	w.WriteHeader(http.StatusTooManyRequests)
	h.renderNodePage(w, r, slug, msg, false)
}

// handleNodeVote 处理 /n/<slug>/vote：target=node|comment，v=up|down|cancel。
func (h *Hub) handleNodeVote(w http.ResponseWriter, r *http.Request, slug string) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	node, err := h.store.GetNodeBySlug(slug)
	if err != nil || node == nil || node.Visibility == model.VisibilityPrivate {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}

	var val int
	switch r.FormValue("v") {
	case "up":
		val = 1
	case "down":
		val = -1
	case "cancel":
		val = 0
	default:
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}

	targetType, targetID := "node", node.ID
	if r.FormValue("target") == "comment" {
		id := strings.TrimSpace(r.FormValue("id"))
		if !strings.HasPrefix(id, "cm_") {
			http.Error(w, "参数错误", http.StatusBadRequest)
			return
		}
		targetType, targetID = "comment", id
	}

	voter := h.voterID(w, r)
	if !h.limiter.allow("vt:"+voter, 300*time.Millisecond) {
		http.Error(w, "手速太快", http.StatusTooManyRequests)
		return
	}
	if err := h.store.SetVote(targetType, targetID, voter, val); err != nil {
		log.Printf("[hub] 投票失败: %v", err)
		http.Error(w, "内部错误", http.StatusInternalServerError)
		return
	}

	counts, _ := h.store.VoteCounts(targetType, []string{targetID}, voter)
	res := counts[targetID]
	body := map[string]any{"ok": true, "up": res.Up, "down": res.Down, "mine": res.Mine, "score": res.Score()}

	// 带 JS 的走 JSON 原地刷新；没 JS 的（老浏览器/爬虫）退回整页刷新。
	if r.Header.Get("X-Requested-With") == "XMLHttpRequest" || r.FormValue("json") == "1" {
		writeJSON(w, http.StatusOK, body)
		return
	}
	http.Redirect(w, r, "/n/"+slug+"#comments", http.StatusSeeOther)
}

// commentEnabled 是否开放留言。
func (h *Hub) commentEnabled() bool {
	v, err := h.store.GetSetting("comment_enabled")
	if err != nil || v == "" {
		return true
	}
	return v == "1"
}

// autoApprove 留言是否免审核直接上墙（主人可在后台关掉）。
func (h *Hub) autoApprove() bool {
	v, err := h.store.GetSetting("comment_auto_approve")
	if err != nil || v == "" {
		return true
	}
	return v == "1"
}

// visitorHash 给留言记一个不可逆的访客指纹，用于限流与识别，不存明文 IP。
func (h *Hub) visitorHash(r *http.Request) string {
	ip := h.clientIP(r)
	sum := sha256.Sum256([]byte(ip + "|" + r.UserAgent() + "|kokoro"))
	return hex.EncodeToString(sum[:])[:24]
}

// rateLimitIPHash uses only the normalized client IP so changing user agents
// cannot bypass per-IP posting limits. The raw address is never persisted.
func (h *Hub) rateLimitIPHash(r *http.Request) string {
	sum := sha256.Sum256([]byte("kokoro-rate|" + h.clientIP(r)))
	return hex.EncodeToString(sum[:])[:24]
}

// voterID 给每个访客发一个随机标识，用来判定「这一票是不是你投的」。
// 只当标识用，不做任何鉴权；拿不到 cookie 时退回 IP 哈希。
func (h *Hub) voterID(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(voterCookie); err == nil && validVoterID(c.Value) {
		return c.Value
	}
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err == nil {
		v := "v_" + hex.EncodeToString(buf)
		http.SetCookie(w, &http.Cookie{
			Name:     voterCookie,
			Value:    v,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			Secure:   h.cfg.BehindProxy || h.cfg.TLSMode == "auto",
			MaxAge:   365 * 24 * 3600,
		})
		return v
	}
	return "f_" + h.visitorHash(r)
}

const voterCookie = "kokoro_vid"

func validVoterID(v string) bool {
	if len(v) < 3 || len(v) > 64 {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' {
			continue
		}
		return false
	}
	return true
}

// clampRunes 按字符截断，避免把多字节汉字切坏。
// parseTags 把后台输入的标签串规整成切片。
//
// 分隔符兼容中英文逗号、顿号、分号和空白。这一条是刻意的：
// 站长多半是从别处把一串标签粘过来，因为用了「，」而整串变成一个标签，
// 是最容易踩的坑。
//
// 去重按大小写不敏感——前端展示保留原样，但不允许 "VPS" 和 "vps" 同时出现，
// 那在卡片上看着就像重复了。
// maxNodeTags 是卡片上显示的自定义标签数量上限。标签行不换行，
// 塞太多就会把续费/到期两个固定槽挤出可视区，而到期天数正是站长最要看的信息。
const maxNodeTags = 4

func parseTags(raw string, maxN, maxLen int) []string {
	if maxN <= 0 {
		maxN = 8
	}
	if maxLen <= 0 {
		maxLen = 16
	}
	out := make([]string, 0, maxN)
	seen := make(map[string]bool, maxN)
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '，', '、', ';', '；':
			return true
		}
		return unicode.IsSpace(r)
	}) {
		// 顺手剥掉开头的 #：有人习惯写 #香港，展示时不该带着
		t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(part), "#"))
		if t == "" {
			continue
		}
		t = clampRunes(t, maxLen)
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
		if len(out) >= maxN {
			break
		}
	}
	return out
}

// clampRunes 按**字符数**（不是字节数）截断，避免把中文截成半个字。
func clampRunes(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	rs := []rune(s)
	return string(rs[:max])
}

// ---- 后台 ----
//
// 鉴权细节在 auth.go：用户名 + 口令（PBKDF2），登录后发随机会话令牌。

func (h *Hub) handleAdmin(w http.ResponseWriter, r *http.Request) {
	// 确保库里有一个管理员账号（没有就随机生成一个并打印到日志）
	h.ensureAdmin()

	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, HEAD, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Method == http.MethodPost {
		loginIP := loginThrottleKey(h.clientIP(r))
		if h.loginThrottle != nil && !h.loginThrottle.begin(loginIP, time.Now()) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusTooManyRequests)
			h.render(w, "login.html", map[string]any{
				"SiteName": h.cfg.SiteName,
				"Error":    "登录尝试次数过多，请 15 分钟后再试",
			}, r)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "参数错误", http.StatusBadRequest)
			return
		}
		user := strings.TrimSpace(r.FormValue("username"))
		pass := r.FormValue("password")
		if !h.loginOK(w, r, user, pass) {
			return
		}
		if h.loginThrottle != nil {
			h.loginThrottle.succeeded(loginIP)
		}
		h.newSession(w, r)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	if !h.adminAuthed(r) {
		h.render(w, "login.html", map[string]any{"SiteName": h.cfg.SiteName}, r)
		return
	}
	h.renderAdmin(w, r)
}

// loginOK 校验用户名与口令；成功返回 true，失败就地渲染登录页并返回 false。
func (h *Hub) loginOK(w http.ResponseWriter, r *http.Request, user, pass string) bool {
	fail := func(msg string) bool {
		h.render(w, "login.html", map[string]any{
			"SiteName": h.cfg.SiteName,
			"User":     user,
			"Error":    msg,
		}, r)
		return false
	}

	wantUser := h.adminUsername()
	if subtle.ConstantTimeCompare([]byte(user), []byte(wantUser)) != 1 {
		// 用户名错也要走一次口令校验再报错，避免用响应时间区分「用户名对不对」
		if h_, err := h.store.GetSetting(settingAdminPass); err == nil && h_ != "" {
			verifyPassword(h_, pass)
		}
		return fail("用户名或密码不对")
	}
	hash, err := h.store.GetSetting(settingAdminPass)
	if err != nil || hash == "" {
		return fail("还没有设置管理员密码，请用 kokoro passwd 命令设置")
	}
	ok, upgrade := verifyPassword(hash, pass)
	if !ok {
		return fail("用户名或密码不对")
	}
	if upgrade {
		// 旧库是裸 sha256，校验通过后顺手升级成 PBKDF2
		if newHash, err := hashPassword(pass); err == nil {
			_ = h.store.SetSetting(settingAdminPass, newHash)
			log.Printf("[hub] 管理员口令哈希已从旧格式升级为 PBKDF2")
		}
	}
	return true
}

// handleAdminLogout 注销会话。
func (h *Hub) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "只接受 POST", http.StatusMethodNotAllowed)
		return
	}
	h.dropSession(w, r)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// handleAdminAccount 修改管理员用户名 / 密码。
func (h *Hub) handleAdminAccount(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		// 抽干 body 再跳转：否则带 body 的 POST 会因未读数据触发 RST，
		// 连 303 都发不出去，浏览器只看到"连接被重置"。
		drainBody(w, r)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}

	cur := r.FormValue("current_password")
	hash, _ := h.store.GetSetting(settingAdminPass)
	if ok, _ := verifyPassword(hash, cur); !ok {
		http.Redirect(w, r, "/admin?err=pass", http.StatusSeeOther)
		return
	}

	user := strings.TrimSpace(r.FormValue("username"))
	if user == "" {
		user = h.adminUsername()
	}
	if len([]rune(user)) > 32 {
		http.Redirect(w, r, "/admin?err=user", http.StatusSeeOther)
		return
	}

	pass := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")
	if pass != "" {
		if len(pass) < 8 {
			http.Redirect(w, r, "/admin?err=short", http.StatusSeeOther)
			return
		}
		if pass != confirm {
			http.Redirect(w, r, "/admin?err=mismatch", http.StatusSeeOther)
			return
		}
		if err := ValidateAdminPassword(pass); err != nil {
			// 走到这里只剩控制字符一种情况（长度和一致性已经查过）。
			http.Redirect(w, r, "/admin?err=chars", http.StatusSeeOther)
			return
		}
		if err := h.setAdminCredentials(user, pass); err != nil {
			log.Printf("[hub] 修改管理员账号失败: %v", err)
			http.Redirect(w, r, "/admin?err=io", http.StatusSeeOther)
			return
		}
		// 改完密码把所有会话踢掉，重新登录
		h.dropAllSessions()
		h.dropSession(w, r)
		http.Redirect(w, r, "/admin?ok=1", http.StatusSeeOther)
		return
	}

	if err := h.store.SetSetting(settingAdminUser, user); err != nil {
		log.Printf("[hub] 修改管理员用户名失败: %v", err)
		http.Redirect(w, r, "/admin?err=io", http.StatusSeeOther)
		return
	}
	_ = h.store.AddAudit("admin", "admin.rename", user, "")
	http.Redirect(w, r, "/admin#account", http.StatusSeeOther)
}

// dropAllSessions 让所有已登录的会话立即失效。
func (h *Hub) dropAllSessions() {
	all, err := h.store.ListSettings(sessionKeyPrefix)
	if err != nil {
		return
	}
	for k := range all {
		_ = h.store.SetSetting(k, "")
	}
}

func (h *Hub) renderAdmin(w http.ResponseWriter, r *http.Request) {
	nodes, err := h.store.ListNodes(true)
	if err != nil {
		nodes = nil
	}
	articleCounts, _ := h.store.CountByNode("node_articles", "")
	// 每台机器的名片（价格 / 到期日）。一次批量查 ——
	// 节点表里每行都要显示，逐个查就是 N 次往返。
	profiles, err := h.store.ProfilesByNode()
	if err != nil {
		profiles = nil
	}
	tokens, err := h.ListInstallTokens()
	if err != nil {
		tokens = nil
	}
	hubURL := h.cfg.Domain
	if hubURL == "" {
		hubURL = r.Host
	}
	if !strings.HasPrefix(hubURL, "http") {
		hubURL = "https://" + hubURL
	}

	// 留言审核：最近 50 条，配上小鸡名
	ownerOf := h.owner()
	data := adminData{
		SiteName:         h.cfg.SiteName,
		Nodes:            nodes,
		ArticleCounts:    articleCounts,
		NodeGroups:       groupAdminNodes(nodes),
		GroupMsg:         strings.TrimSpace(r.URL.Query().Get("groupmsg")),
		ArticleDeleted:   r.URL.Query().Get("article_deleted") == "1",
		SelectedGroup:    strings.TrimSpace(r.URL.Query().Get("group")),
		TaskMsg:          strings.TrimSpace(r.URL.Query().Get("taskmsg")),
		TaskMsgError:     r.URL.Query().Get("taskmsg_error") == "1",
		Profiles:         profiles,
		Tokens:           tokens,
		HubURL:           hubURL,
		CommentOn:        h.commentEnabled(),
		AutoApprove:      h.autoApprove(),
		OwnerName:        ownerOf.Name,
		OwnerVisible:     ownerOf.Visible,
		OwnerDisplayName: ownerOf.Name,
		OwnerBio:         ownerOf.Bio,
		OwnerAvatar:      ownerOf.Avatar,
	}
	if groups, err := h.store.ListNodeGroups(); err == nil {
		data.Groups = groups
	}
	if raw, err := h.store.GetSetting(settingHubLat); err == nil {
		data.HubLat = raw
	}
	if raw, err := h.store.GetSetting(settingHubLon); err == nil {
		data.HubLon = raw
	}
	// ⚠️ 这里是**手工构造** adminData（没走 adminBase，因为它还带
	// Nodes/Profiles/Tokens 这些只有后台首页才要的东西）。
	// 所以主题面板那几项得**显式补上** —— 漏了的话「主题」页签里
	// 主题卡片会是空的（列表渲染出 0 张，还不报错）。
	// 2026-10-11 合并主题页时就是这么踩的：DOM 里 th-grid 是空的。
	h.fillThemePanel(&data, r)

	nameOf := make(map[string]string, len(nodes))
	slugOf := make(map[string]string, len(nodes))
	for _, n := range nodes {
		nameOf[n.ID] = n.Name
		slugOf[n.ID] = n.Slug
	}

	// 告警规则与最近事件
	if rules, err := h.store.ListAlertRules(); err == nil {
		data.AlertRules = rules
	}
	if evs, err := h.store.ListAlertEvents("", 10); err == nil {
		for _, e := range evs {
			v := alertEventView{AlertEvent: e}
			if nm, ok := nameOf[e.NodeID]; ok {
				v.NodeName = nm
			}
			data.AlertEvents = append(data.AlertEvents, v)
		}
	}
	// 面板主机自身（Hub 所在机器）的资源占用。
	// 原来在 /dashboard 上，那个页面删掉后并到后台 ——
	// "面板会不会先撑不住"是站长该关心的事，跟访客无关。
	data.HostNodeID, data.Host, data.HostNote = h.hostSelfSnapshot(nodes)
	if data.Host != nil {
		data.HostMemPct = Pct(float64(data.Host.Mem.Used), float64(data.Host.Mem.Total))
		data.HostDiskPct = Pct(float64(data.Host.Disk.Used), float64(data.Host.Disk.Total))
	}

	if cfg, err := notify.LoadConfig(h.store); err == nil {
		data.NotifyOn = cfg.TelegramEnabled
		data.NotifyChat = cfg.TelegramChatID
		data.NotifyToken = notify.MaskToken(cfg.TelegramBotToken)
		data.NotifySilent = cfg.TelegramSilent
		data.WebhookOn = cfg.WebhookEnabled
		data.WebhookURL = cfg.WebhookURL
		data.NotifyQuiet = fmt.Sprintf("%d–%d", cfg.QuietHoursStart, cfg.QuietHoursEnd)
		data.NotifyQuietStart = cfg.QuietHoursStart
		data.NotifyQuietEnd = cfg.QuietHoursEnd
	}
	data.ExpiryReminderOn, data.ExpiryReminderDays = h.expiryReminderSettings()
	if r.URL.Query().Get("expiry_saved") == "1" {
		data.ExpiryReminderMsg = "到期提醒设置已保存。"
	}
	if r.URL.Query().Get("expiry_error") == "days" {
		data.ExpiryReminderMsg, data.ExpiryReminderBad = "提醒天数请输入 1–365 之间的整数。", true
	}
	if r.URL.Query().Get("expiry_error") == "save" {
		data.ExpiryReminderMsg, data.ExpiryReminderBad = "保存失败，请稍后重试。", true
	}
	// 按小鸡分组用的桶。评论一次查回来，边装边分。
	//
	// ⚠️ 只在这里声明一次。下面别再写 `grouped := ...` ——
	// 那会遮蔽掉这个，外层永远是空的，表现成"分组一个都不显示"
	// 而且不报错。（踩过一次）
	grouped := map[string][]modComment{}
	cs, err := h.store.ListRecentComments(200, "")
	if err == nil {
		ids := make([]string, 0, len(cs))
		for _, c := range cs {
			ids = append(ids, c.ID)
		}
		counts, _ := h.store.VoteCounts("comment", ids, "")
		for _, c := range cs {
			mc := modComment{Comment: c, Score: counts[c.ID].Score()}
			if nm, ok := nameOf[c.NodeID]; ok {
				mc.NodeName = nm
				mc.NodeSlug = slugOf[c.NodeID]
			}
			data.Comments = append(data.Comments, mc)
			grouped[c.NodeID] = append(grouped[c.NodeID], mc)
		}
	}
	if pending, err := h.store.ListRecentComments(200, model.CommentPending); err == nil {
		data.PendingN = len(pending)
	}

	// 按小鸡分组。**顺序跟着节点列表走**，不是跟着评论出现顺序 ——
	// 后台的节点是有固定排序的，评论分组跟着它才不会每次都换位置。
	//
	// 没有任何评论的机器不显示分组（空块只会占地方）。
	for _, n := range nodes {
		if g := grouped[n.ID]; len(g) > 0 {
			data.CommentsByNode = append(data.CommentsByNode, nodeComments{
				NodeID: n.ID, Name: n.Name, Slug: n.Slug, Comments: g,
			})
			delete(grouped, n.ID)
		}
	}
	// 剩下的是「评论还在、节点已经删了」的孤儿，兜底也列出来，
	// 否则那些评论在后台永远看不到、也删不掉。
	for id, g := range grouped {
		data.CommentsByNode = append(data.CommentsByNode, nodeComments{
			NodeID: id, Name: nameOf[id], Slug: slugOf[id], Comments: g,
		})
	}

	const inboxPageSize = 10
	data.InboxPage, _ = strconv.Atoi(r.URL.Query().Get("inbox_page"))
	if data.InboxPage < 1 {
		data.InboxPage = 1
	}
	if total, err := h.store.CountSiteMessages(); err == nil {
		data.SiteMessagesTotal = total
		data.InboxPages = (total + inboxPageSize - 1) / inboxPageSize
	}
	if data.InboxPages == 0 {
		data.InboxPages = 1
	}
	if data.InboxPage > data.InboxPages {
		data.InboxPage = data.InboxPages
	}
	data.InboxHasPrev = data.InboxPage > 1
	data.InboxHasNext = data.InboxPage < data.InboxPages
	data.InboxPrevPage = data.InboxPage - 1
	data.InboxNextPage = data.InboxPage + 1
	if messages, err := h.store.ListSiteMessages(inboxPageSize, (data.InboxPage-1)*inboxPageSize); err == nil {
		data.SiteMessages = messages
	}
	data.InboxUnread, _ = h.store.CountUnreadSiteMessages()
	data.InboxError = r.URL.Query().Get("inbox_error") == "1"
	data.InboxSelectionError = r.URL.Query().Get("inbox_error") == "selection"
	data.InboxDeleted, _ = strconv.Atoi(r.URL.Query().Get("inbox_deleted"))

	// 国家/地区候选：把常用机房所在地排前面，其余按代码顺序兜底
	if data.CC == nil {
		data.CC = countryOptions()
	}

	// 管理员账号
	data.AdminUser = h.adminUsername()
	switch r.URL.Query().Get("err") {
	case "pass":
		data.AccountMsg, data.AccountBad = "当前密码不对", true
	case "short":
		data.AccountMsg, data.AccountBad = "新密码至少 8 位", true
	case "mismatch":
		data.AccountMsg, data.AccountBad = "两次输入的新密码不一致", true
	case "chars":
		data.AccountMsg, data.AccountBad = "新密码不能包含控制字符，请手动输入", true
	case "user":
		data.AccountMsg, data.AccountBad = "用户名太长（最多 32 字）", true
	case "io":
		data.AccountMsg, data.AccountBad = "保存失败，请查看 Hub 日志", true
	}
	if r.URL.Query().Get("ok") == "1" {
		data.AccountMsg = "密码已更新，请用新密码重新登录"
	}

	h.render(w, "admin.html", &data, r)
}

// countryOptions 生成国家/地区候选项。常用的机房所在地放最前面，
// 其余按代码升序跟在后面——用键盘输入代码一样能选中。
func countryOptions() []ccOption {
	common := []string{
		"CN", "HK", "TW", "MO", "JP", "KR", "SG", "US", "CA", "GB",
		"DE", "FR", "NL", "RU", "AU", "IN", "VN", "TH", "MY", "ID",
		"PH", "AE", "TR", "BR", "IT", "ES", "SE", "CH", "PL", "FI",
	}
	seen := make(map[string]bool, len(common))
	out := make([]ccOption, 0, 240)
	add := func(code string) {
		if seen[code] || !flags.Has(code) {
			return
		}
		seen[code] = true
		out = append(out, ccOption{Code: code, Label: code + " " + flags.Name(code)})
	}
	for _, c := range common {
		add(c)
	}
	for _, c := range flags.Codes() {
		add(c)
	}
	return out
}

// handleAdminComments 后台留言审核：通过 / 标记垃圾 / 删除。
func (h *Hub) handleAdminComments(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		// 抽干 body 再跳转：否则带 body 的 POST 会因未读数据触发 RST，
		// 连 303 都发不出去，浏览器只看到"连接被重置"。
		drainBody(w, r)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}
	id := r.FormValue("id")
	switch r.FormValue("action") {
	case "approve":
		_ = h.store.ModerateComment(id, model.CommentApproved)
	case "spam":
		_ = h.store.ModerateComment(id, model.CommentSpam)
	case "pending":
		_ = h.store.ModerateComment(id, model.CommentPending)
	case "pin":
		_ = h.store.PinComment(id, true)
		_ = h.store.AddAudit("admin", "comment.pin", id, "")
	case "unpin":
		_ = h.store.PinComment(id, false)
	case "delete":
		_ = h.store.DeleteComment(id)
		_ = h.store.AddAudit("admin", "comment.delete", id, "")
	}
	http.Redirect(w, r, "/admin#comments", http.StatusSeeOther)
}

// handleAdminSettings 后台开关：留言总开关与免审核开关。
func (h *Hub) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		// 抽干 body 再跳转：否则带 body 的 POST 会因未读数据触发 RST，
		// 连 303 都发不出去，浏览器只看到"连接被重置"。
		drainBody(w, r)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}
	on := func(name string) string {
		if r.FormValue(name) == "1" {
			return "1"
		}
		return "0"
	}
	_ = h.store.SetSetting("comment_enabled", on("comment_enabled"))
	_ = h.store.SetSetting("comment_auto_approve", on("comment_auto_approve"))

	// 主机坐标：首页地球用它当航线中心。
	// 只做范围校验，非法值直接清空——比起把节点连到一个错误的位置，
	// 不如干脆不画航线（buildGlobe 里 HasHub=false 就是这个效果）。
	if _, has := r.Form["hub_lat"]; has {
		lat := strings.TrimSpace(r.FormValue("hub_lat"))
		lon := strings.TrimSpace(r.FormValue("hub_lon"))
		if f, err := strconv.ParseFloat(lat, 64); err != nil || f < -90 || f > 90 {
			lat = ""
		}
		if f, err := strconv.ParseFloat(lon, 64); err != nil || f < -180 || f > 180 {
			lon = ""
		}
		_ = h.store.SetSetting(settingHubLat, lat)
		_ = h.store.SetSetting(settingHubLon, lon)
	}

	// 注：这里原来还处理「抓主题的本机地址白名单」表单（theme_fetch_hosts）。
	// 那个入口已按 boss 要求撤掉——它对普通站长没有任何用处（自建站直接跑
	// Hub，不存在"反代之后换域名抓自己"的场景）。**安全策略本身没动**：
	// theme_ssrf.go 仍然默认拒绝回环、只放行 --domain 与库里已有的白名单，
	// 只是不再提供界面去改它。
	http.Redirect(w, r, "/admin#comments", http.StatusSeeOther)
}

// handleAdminExpiryReminder saves the reminder switch and its lead time in days.
func (h *Hub) handleAdminExpiryReminder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin#alerts", http.StatusSeeOther)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}
	days, err := strconv.Atoi(strings.TrimSpace(r.FormValue("days")))
	if err != nil || days < 1 || days > maxExpiryReminderDay {
		http.Redirect(w, r, "/admin?expiry_error=days#alerts", http.StatusSeeOther)
		return
	}
	on := "0"
	if r.FormValue("enabled") == "1" {
		on = "1"
	}
	if err := h.store.SetSettings(map[string]string{settingExpiryReminderOn: on, settingExpiryReminderDay: strconv.Itoa(days)}); err != nil {
		log.Printf("[hub] 保存到期提醒设置失败: %v", err)
		http.Redirect(w, r, "/admin?expiry_error=save#alerts", http.StatusSeeOther)
		return
	}
	if on == "1" {
		go h.checkExpiryReminders(context.Background())
	}
	http.Redirect(w, r, "/admin?expiry_saved=1#alerts", http.StatusSeeOther)
}

// handleAdminMessages processes inbox read-state actions.
func (h *Hub) handleAdminMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}
	var err error
	var deleted int64
	switch r.FormValue("action") {
	case "read":
		err = h.store.MarkSiteMessageRead(strings.TrimSpace(r.FormValue("id")))
	case "read_all":
		err = h.store.MarkAllSiteMessagesRead()
	case "delete":
		if len(r.Form["ids"]) == 0 {
			http.Redirect(w, r, "/admin?inbox_page="+strconv.Itoa(parsePositivePage(r.FormValue("page")))+"&inbox_error=selection#buy-intents", http.StatusSeeOther)
			return
		}
		deleted, err = h.store.DeleteSiteMessages(r.Form["ids"])
		if err == nil && deleted == 0 {
			http.Redirect(w, r, "/admin?inbox_page="+strconv.Itoa(parsePositivePage(r.FormValue("page")))+"&inbox_error=selection#buy-intents", http.StatusSeeOther)
			return
		}
	default:
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}
	if err != nil {
		log.Printf("[hub] 更新站内消息已读状态失败: %v", err)
		http.Redirect(w, r, "/admin?inbox_page="+strconv.Itoa(parsePositivePage(r.FormValue("page")))+"&inbox_error=1#buy-intents", http.StatusSeeOther)
		return
	}
	if r.FormValue("action") == "delete" {
		http.Redirect(w, r, "/admin?inbox_page="+strconv.Itoa(parsePositivePage(r.FormValue("page")))+"&inbox_deleted="+strconv.FormatInt(deleted, 10)+"#buy-intents", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin?inbox_page="+strconv.Itoa(parsePositivePage(r.FormValue("page")))+"#buy-intents", http.StatusSeeOther)
}

func parsePositivePage(raw string) int {
	page, err := strconv.Atoi(raw)
	if err != nil || page < 1 {
		return 1
	}
	return page
}

// handleAdminTokens 创建/吊销安装令牌。
func (h *Hub) handleAdminTokens(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		// 抽干 body 再跳转：否则带 body 的 POST 会因未读数据触发 RST，
		// 连 303 都发不出去，浏览器只看到"连接被重置"。
		drainBody(w, r)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}

	switch r.FormValue("action") {
	case "create":
		label := r.FormValue("label")
		if label == "" {
			label = "默认"
		}
		ttlH, _ := strconv.Atoi(r.FormValue("ttl_hours"))
		uses, _ := strconv.Atoi(r.FormValue("max_uses"))
		if _, err := h.CreateInstallToken(label, time.Duration(ttlH)*time.Hour, uses, label); err != nil {
			log.Printf("[hub] 创建安装令牌失败: %v", err)
		}
		_ = h.store.AddAudit("admin", "token.create", label, "")
	case "revoke":
		tok := r.FormValue("token")
		_ = h.store.SetSetting(tokenKeyPrefix+tok, "")
		_ = h.store.AddAudit("admin", "token.revoke", tok, "")
	}
	http.Redirect(w, r, "/admin#sys", http.StatusSeeOther)
}

// handleAdminGroups 创建分组、移动节点或删除分组。
func (h *Hub) handleAdminGroups(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		drainBody(w, r)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin#nodes", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}
	message := ""
	selectedGroup := ""
	switch r.FormValue("action") {
	case "create":
		name := strings.TrimSpace(r.FormValue("name"))
		if name == "" || len([]rune(name)) > 32 || name == "全部" {
			message = "分组名称需为 1–32 个字，且不能使用保留名称“全部”"
		} else if err := h.store.CreateNodeGroup(name); err != nil {
			message = err.Error()
		} else {
			message = "已创建分组：" + name
			selectedGroup = name
		}
	case "rename":
		oldName := strings.TrimSpace(r.FormValue("old_name"))
		newName := strings.TrimSpace(r.FormValue("new_name"))
		if len([]rune(newName)) > 32 || newName == "" {
			message = "分组名称需为 1–32 个字"
			selectedGroup = oldName
		} else if newName == "全部" {
			message = "“全部”是固定标签，不能用作分组名称"
			selectedGroup = oldName
		} else if err := h.store.RenameNodeGroup(oldName, newName); err != nil {
			message = err.Error()
			selectedGroup = oldName
		} else {
			message = "已将分组“" + oldName + "”改为“" + newName + "”"
			selectedGroup = newName
		}
	case "assign":
		name := strings.TrimSpace(r.FormValue("name"))
		selectedGroup = name
		if err := h.store.AssignNodesToGroup(name, r.Form["node_ids"]); err != nil {
			message = err.Error()
		} else {
			message = "所选节点已加入分组：" + name
		}
	case "delete":
		name := strings.TrimSpace(r.FormValue("name"))
		if err := h.store.DeleteNodeGroup(name); err != nil {
			message = err.Error()
		} else {
			message = "已删除分组：" + name
		}
	default:
		message = "未知的分组操作"
	}
	to := "/admin?groupmsg=" + url.QueryEscape(message)
	if selectedGroup != "" {
		to += "&group=" + url.QueryEscape(selectedGroup)
	}
	http.Redirect(w, r, to+"#nodes", http.StatusSeeOther)
}

// handleAdminNodes 后台节点操作：改名、改可见性、删除。
func (h *Hub) handleAdminNodes(w http.ResponseWriter, r *http.Request) {
	wantsJSON := strings.Contains(r.Header.Get("Accept"), "application/json")
	if !h.adminAuthed(r) {
		// 抽干 body 再跳转：否则带 body 的 POST 会因未读数据触发 RST，
		// 连 303 都发不出去，浏览器只看到"连接被重置"。
		drainBody(w, r)
		if wantsJSON {
			writeErr(w, http.StatusUnauthorized, "登录已过期，请重新进入后台")
			return
		}
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "参数错误", http.StatusBadRequest)
		return
	}

	id := r.FormValue("id")
	node, err := h.store.GetNode(id)
	if err != nil || node == nil {
		if wantsJSON {
			writeErr(w, http.StatusNotFound, "节点不存在")
			return
		}
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}

	switch r.FormValue("action") {
	case "sort_order":
		sortOrder, parseErr := strconv.Atoi(strings.TrimSpace(r.FormValue("sort_order")))
		if parseErr != nil || sortOrder < -1000000 || sortOrder > 1000000 {
			if wantsJSON {
				writeErr(w, http.StatusBadRequest, "排序 ID 需为 -1000000 到 1000000 之间的整数")
			} else {
				http.Redirect(w, r, "/admin#nodes", http.StatusSeeOther)
			}
			return
		}
		if err := h.store.SetNodeSortOrder(node.ID, sortOrder); err != nil {
			if wantsJSON {
				writeErr(w, http.StatusInternalServerError, "保存排序 ID 失败，请重试")
			} else {
				http.Error(w, "保存排序 ID 失败", http.StatusInternalServerError)
			}
			return
		}
		_ = h.store.AddAudit("admin", "node.sort_order", node.ID, strconv.Itoa(sortOrder))
		if wantsJSON {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": node.ID, "sort_order": sortOrder})
			return
		}
	case "rename":
		var nameUpdate *string
		var tagsUpdate *[]string
		if name := strings.TrimSpace(r.FormValue("name")); name != "" {
			node.Name = name
			nameUpdate = &node.Name
		}
		// ⚠️ **国家 / 地区不在这里改**（管理员要求）。
		// 它们由 agent 上报 —— agent 从自己所在的网络问 Cloudflare
		// 的 cdn-cgi/trace，拿 loc（国家）和 colo（最近数据中心 → 城市）。
		//
		// 为什么不用 IP 归属库：对部分 VPS 不准。而 Cloudflare 是按**真实网络路径**判的。
		//
		// 表单里已经没有这两个输入框了；这里保留读取是为了兼容老表单
		// （万一有人用旧页面提交），但**只接受空值以外的忽略** ——
		// 实际上就是一律不动。
		_ = r.FormValue("country")
		_ = r.FormValue("region")

		// 到期日 / 续费费用存在 node_profile 里（不是 nodes）——
		// 它们是"站长填的资料"，不是机器上报的。
		//
		// ⚠️ 只在表单**真的带了资料字段**时才写：这个 rename 分支
		// 还被别处复用（比如只改个名字），无脑覆盖会把已填的抹掉。
		_, hasExpire := r.Form["expire_at"]
		_, hasExpireForever := r.Form["expire_forever"]
		_, hasStructuredPrice := r.Form["price_amount"]
		_, hasFreePrice := r.Form["price"]
		if hasExpire || hasExpireForever || hasStructuredPrice || hasFreePrice {
			prof, _ := h.store.GetProfile(node.ID)
			if prof == nil {
				prof = &model.NodeProfile{NodeID: node.ID}
			}
			// 到期日：勾了「永久」就存固定词，否则存 YYYY-MM-DD。
			//
			// ⚠️ 勾了永久时浏览器**不会提交**被禁用的日期框，
			// 所以不能只看 expire_at —— 会变成空串（= 没填）。
			if hasExpireForever && r.FormValue("expire_forever") != "" {
				prof.ExpireAt = expireForever
			} else if hasExpire {
				prof.ExpireAt = clampRunes(normalizeExpireDateInput(r.FormValue("expire_at")), 32)
			}
			// 自由文本续费信息直接保存；旧版结构化表单仍兼容。
			if hasFreePrice {
				prof.Price = clampRunes(strings.TrimSpace(r.FormValue("price")), 64)
			} else if hasStructuredPrice {
				prof.Price = priceFromForm(
					r.FormValue("price_amount"),
					r.FormValue("price_cur"),
					r.FormValue("price_per"))
			}
			prof.UpdatedAt = time.Now().UnixMilli()
			if err := h.store.SaveProfile(prof); err != nil {
				log.Printf("[hub] 保存到期/费用失败 node=%s: %v", node.ID, err)
				http.Error(w, "保存到期/费用失败，请重试", http.StatusInternalServerError)
				return
			}
		}
		// 自定义标签：最多 maxNodeTags 个、每个最多 16 字。
		// 只在表单真的带了 tags 字段时才覆盖——否则别处的 rename 调用
		// （比如只改名字）会顺手把标签清空。
		if _, has := r.Form["tags"]; has {
			node.Tags = parseTags(r.FormValue("tags"), maxNodeTags, 16)
			tagsUpdate = &node.Tags
		}
		if err := h.store.UpdateNodeLabels(node.ID, nameUpdate, tagsUpdate); err != nil {
			http.Error(w, "保存节点资料失败，请重试", http.StatusInternalServerError)
			return
		}
	case "visibility":
		switch r.FormValue("visibility") {
		case "public", "unlisted", "private":
			node.Visibility = model.Visibility(r.FormValue("visibility"))
			if err := h.store.SetNodeVisibility(node.ID, node.Visibility); err != nil {
				http.Error(w, "保存展示状态失败，请重试", http.StatusInternalServerError)
				return
			}
		}
	case "toggle":
		// 一键显示/隐藏：public <-> private。unlisted（不索引但仍可访问）算「显示」，
		// 所以隐藏只认 private。
		if desired := r.FormValue("visibility"); desired == "public" || desired == "private" {
			node.Visibility = model.Visibility(desired)
		} else if node.Visibility == model.VisibilityPrivate {
			node.Visibility = model.VisibilityPublic
		} else {
			node.Visibility = model.VisibilityPrivate
		}
		if err := h.store.SetNodeVisibility(node.ID, node.Visibility); err != nil {
			if wantsJSON {
				writeErr(w, http.StatusInternalServerError, "保存展示状态失败，请重试")
			} else {
				http.Error(w, "保存展示状态失败", http.StatusInternalServerError)
			}
			return
		}
		_ = h.store.AddAudit("admin", "node.visibility", node.ID, string(node.Visibility))
		if wantsJSON {
			w.Header().Set("Cache-Control", "no-store")
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "id": node.ID, "visibility": node.Visibility})
			return
		}
	case "save_profile":
		h.savePost(node, r.Form)
		if r.FormValue("back") == "post" {
			http.Redirect(w, r,
				"/admin/post?node="+url.QueryEscape(node.ID)+"&saved=1",
				http.StatusSeeOther)
			return
		}
	case "delete_article":
		deleted, err := h.store.DeleteNodeArticles(node.ID)
		if err != nil {
			log.Printf("[hub] 删除文章失败 node=%s: %v", node.ID, err)
			http.Error(w, "删除文章失败，请重试", http.StatusInternalServerError)
			return
		}
		if deleted > 0 {
			_ = h.store.AddAudit("admin", "article.delete", node.ID, node.Name)
		}
		http.Redirect(w, r, "/admin?article_deleted=1#nodes", http.StatusSeeOther)
		return
	case "delete":
		_ = h.store.DeleteNode(node.ID)
		_ = h.store.AddAudit("admin", "node.delete", node.ID, node.Name)
	case "swap":
		sizeMB, err := swapGiBToMiB(r.FormValue("swap_gib"))
		if err != nil {
			http.Redirect(w, r, "/admin?taskmsg="+urlQueryEscape(err.Error())+"&taskmsg_error=1#nodes", http.StatusSeeOther)
			return
		}
		_, err = h.DispatchSwap(node.ID, sizeMB)
		if err != nil {
			http.Redirect(w, r, "/admin?taskmsg="+urlQueryEscape("增加 SWAP 未排入："+err.Error())+"&taskmsg_error=1#nodes", http.StatusSeeOther)
			return
		}
		_ = h.store.AddAudit("admin", "node.swap", node.ID, swapSizeLabel(sizeMB))
		msg := fmt.Sprintf("已为 %s 排入增加 SWAP（%s）；节点下次上报时开始执行", node.Name, swapSizeLabel(sizeMB))
		http.Redirect(w, r, "/admin?taskmsg="+urlQueryEscape(msg)+"#nodes", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

// ---- 渲染 ----

// themed 是所有需要主题数据的页面数据要实现的一个接口。
//
// 用接口而不是在 render 里 switch 具体类型：以后新增页面只要实现了这个方法
// 就自动拿到主题，忘了也不会白屏——这是"漏了不报错"和"漏了静默失效"的分界。
type themed interface {
	setTheme(themeView)
}

func (d *homeData) setTheme(t themeView)      { d.Theme = t }
func (d *nodePageData) setTheme(t themeView)  { d.Theme = t }
func (d *dashboardData) setTheme(t themeView) { d.Theme = t }
func (d *adminData) setTheme(t themeView)     { d.Theme = t }

// render 渲染一个页面。
//
// 主题是横切关注点：每个页面都要拿到同一份 tokens / layout /
// data-k-* 属性。与其让每个调用点各自记得塞一遍，不如在这里统一注入——
// 模板里只要写 {{.Theme.Tokens}} 就能用。
//
// data 必须是 map 或实现了 themed 的指针，否则主题为空，
// 页面会退回 style.css 里的兜底变量（仍可读，只是没有主题）。
func (h *Hub) render(w http.ResponseWriter, name string, data any, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	// ⚠️ HTML **必须每次回源**，别让它进启发式缓存。
	//
	// 不给这条头时，浏览器对没有 Cache-Control / Last-Modified 的 HTML 会
	// 自行"启发式缓存"，于是出现：部署了新版本、static 的 ?v=N 也 bump 了，
	// 用户刷新却仍拿着旧 HTML —— 里面引用的还是旧版 CSS/JS，
	// 表现成"明明修了却没变化"，而且从服务端完全看不出异常。
	// 2026-10-10 的地球修复就在这里卡了一轮。
	//
	// HTML 体积不大（~85KB）且内容本就实时（在线状态、流量都在变），
	// 每次重新取是划算的。
	w.Header().Set("Cache-Control", "no-cache")

	// 主题系统整体不可用时（内置主题解析失败）也要能出页面：
	// 给一份空的 themeView，模板里的条件分支会退回原始样式。
	var tv themeView
	if h.themes != nil {
		tv = h.themeFor(r)
	}

	switch d := data.(type) {
	case map[string]any:
		if _, ok := d["Theme"]; !ok {
			d["Theme"] = tv
		}
	case themed:
		d.setTheme(tv)
	}

	if err := h.tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("[hub] 渲染 %s 失败: %v", name, err)
	}
}

// ensureBootstrap 首次启动时准备管理员账号与第一个安装令牌。
func (h *Hub) ensureBootstrap() {
	h.ensureAdmin()
	tokens, err := h.ListInstallTokens()
	if err != nil {
		log.Printf("[hub] 读取安装令牌失败: %v", err)
		return
	}
	if len(tokens) == 0 {
		t, err := h.CreateInstallToken("默认", 0, 0)
		if err != nil {
			log.Printf("[hub] 创建首个安装令牌失败: %v", err)
			return
		}
		log.Printf("[hub] 首个安装令牌: %s", t.Token)
		log.Printf("[hub] 安装命令: curl -fsSL %s/i/%s | sh", h.hubURLHint(), t.Token)
	}
}

func (h *Hub) hubURLHint() string {
	if h.cfg.Domain != "" {
		return "https://" + h.cfg.Domain
	}
	return fmt.Sprintf("http://%s", h.cfg.Listen)
}

var _ = fmt.Sprintf
