package hub

// 首页的「信息面板」部分：时钟、统计卡、点阵地球。
//
// 单独一个文件是因为 renderHome 本来就不短，再往里塞统计与地理计算会失控。
// 这里只做「把已有数据整理成模板能直接渲染的形状」，不碰存储与渲染细节。

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/geo"
	"github.com/Vincentkeio/kokoro/internal/store"
)

// ---- 时钟 ----

// clockZone 是首页本地时钟的模板数据。时区由访问者的浏览器决定。
type clockZone struct {
	Label string
}

// homeClocks 返回一格按访问者浏览器本地时区运行的时钟。
func homeClocks() []clockZone {
	return []clockZone{{
		Label: "本地时间",
	}}
}

// ---- 统计卡 ----

type statCard struct {
	Label string
	Value string
	Note  string
}

// 统计卡已下线（boss 要求把数字分配到「总览」和「全网速率」里）。
//
// 原来那六张卡：节点总数/在线/覆盖地区 -> 总览，
// 今日流量/本月流量 -> 全网速率，探测点 -> 删除。
// 同一屏出现两遍同样的数字，访客只会犹豫哪个才是准的。

// startOfDay / startOfMonth 用**服务器本地时区**算。
//
// 对个人自建探针来说，"今天"指的是站长所在时区的今天，而不是 UTC 的今天——
// 后者在国内会凭空多出 8 小时的错位，看着像统计错了。
func startOfDay() int64 {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, n.Location()).UnixMilli()
}

func startOfMonth() int64 {
	n := time.Now()
	return time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, n.Location()).UnixMilli()
}

// ---- 点阵地球 ----

// globePlace 是地球上的一个点。字段名即前端 JSON 的键，改名字要同步改 globe.js。
type globePlace struct {
	Country string  `json:"country,omitempty"`
	City    string  `json:"city,omitempty"`
	Key     string  `json:"key"`
	Name    string  `json:"name"`
	Sub     string  `json:"sub"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	Online  bool    `json:"online"`
}

type globeData struct {
	Hub    globePlace   `json:"hub"`
	Places []globePlace `json:"places"`
	HasHub bool         `json:"hasHub"`
	// LitCountries 是有节点落在的国家数（ISO alpha-2 去重）。
	// LitRuns 是这些国家在 0.5° 格网里占据的格子，压缩成
	// "start:len,start:len"（纹理序），前端据此把地球纹理里的
	// 那片陆地染成高亮色 —— 也就是"点亮区域"。
	LitCountries int    `json:"litCountries,omitempty"`
	LitRuns      string `json:"litRuns,omitempty"`
}

// buildGlobe 组装地球数据。
//
// 主机坐标来自后台设置（hub_lat / hub_lon）。**不猜、不自动定位**：
// 猜错的表现是所有航线都连到错误的大洲，比干脆不画航线更误导。
// 没配置时 HasHub=false，前端只画节点点阵、不画航线。
func (h *Hub) buildGlobe(cards []nodeCard) globeData {
	var g globeData
	g.Places = make([]globePlace, 0, len(cards))
	// 哪个国家有鸡，就点亮哪个国家。用国家码（alpha-2）而不是省/市，
	// 因为要点亮的是地球上的一整片区域。
	litSet := make(map[string]struct{}, len(cards))
	for _, c := range cards {
		if !c.HasCoord {
			continue
		}
		g.Places = append(g.Places, globePlace{
			Country: c.FlagCode,
			City:    firstNonEmpty(c.City, c.RegionLabel(), c.Name),
			Key:     c.ID,
			Name:    c.Name,
			Sub:     c.RegionLabel(),
			Lat:     c.Lat,
			Lon:     c.Lon,
			Online:  c.Online,
		})
		// FlagCode 已经是 ISO alpha-2（列表画国旗用的那份），直接拿它当
		// 点亮用的国家码，不去猜 Country 字段的文本格式。
		if code := strings.ToUpper(strings.TrimSpace(c.FlagCode)); code != "" {
			litSet[code] = struct{}{}
		}
	}
	if len(litSet) > 0 {
		codes := make([]string, 0, len(litSet))
		for code := range litSet {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		g.LitRuns = geo.LitRanges(codes)
		g.LitCountries = len(codes)
	}
	if lat, lon, ok := h.hubCoord(); ok {
		name := "主机"
		if h.cfg.Domain != "" {
			name = h.cfg.Domain
		}
		g.Hub = globePlace{Key: "__hub__", Name: name, Sub: "Hub", Lat: lat, Lon: lon, Online: true}
		g.HasHub = true
	}
	return g
}

// hubCoord 读取后台配置的主机坐标。
func (h *Hub) hubCoord() (lat, lon float64, ok bool) {
	rawLat, _ := h.store.GetSetting(settingHubLat)
	rawLon, _ := h.store.GetSetting(settingHubLon)
	la, err1 := strconv.ParseFloat(strings.TrimSpace(rawLat), 64)
	lo, err2 := strconv.ParseFloat(strings.TrimSpace(rawLon), 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	if la < -90 || la > 90 || lo < -180 || lo > 180 {
		return 0, 0, false
	}
	return la, lo, true
}

const (
	settingHubLat = "hub_lat"
	settingHubLon = "hub_lon"
)

// ---- 节点卡的小图 ----

// 小图用的虚拟坐标系，和模板里 SVG 的 viewBox 必须一致。
const (
	sparkW = 260.0
	sparkH = 40.0
)

// sparkPaths 把速率序列转成 SVG 路径。
//
// 以「上下行合计」的最大值为满量程。序列不足 2 个点或全为 0 时返回空串——
// 宁可不出图，也不要画一条贴在底边的直线让人以为是"零流量"。
func sparkPaths(points []store.NetPoint) (line, area string) {
	if len(points) < 2 {
		return "", ""
	}
	maxV := 0.0
	for _, p := range points {
		if v := p.Up + p.Down; v > maxV {
			maxV = v
		}
	}
	if maxV <= 0 {
		return "", ""
	}
	var b strings.Builder
	var a strings.Builder
	step := sparkW / float64(len(points)-1)
	const pad = 3.0 // 上下留白，免得峰值顶到边框
	for i, p := range points {
		v := (p.Up + p.Down) / maxV
		x := float64(i) * step
		y := sparkH - pad - v*(sparkH-2*pad)
		if i == 0 {
			fmt.Fprintf(&b, "M%.1f %.1f", x, y)
			fmt.Fprintf(&a, "M%.1f %.1f", x, y)
		} else {
			fmt.Fprintf(&b, " L%.1f %.1f", x, y)
			fmt.Fprintf(&a, " L%.1f %.1f", x, y)
		}
	}
	fmt.Fprintf(&a, " L%.1f %.1f L0 %.1f Z", sparkW, sparkH, sparkH)
	return b.String(), a.String()
}

// sparkWindowMax gives one node card a local vertical scale so its own
// traffic changes remain visible. The peak label still communicates the
// absolute traffic rate for comparisons between nodes.
func sparkWindowMax(points []store.NetPoint, from, to int64) float64 {
	maxV := 0.0
	for _, p := range points {
		if p.Bucket < from || p.Bucket > to {
			continue
		}
		if v := p.Up + p.Down; v > maxV {
			maxV = v
		}
	}
	return maxV
}

// sparkScaleMax rounds a node's peak to a readable, zero-based axis boundary.
// Aim for about five ticks, as an automatic numeric chart axis does. No other
// node's traffic participates in this scale.
func sparkScaleMax(peak float64) float64 {
	if !(peak > 0) || math.IsInf(peak, 0) {
		return 0
	}
	roughStep := peak / 4
	base := math.Pow(10, math.Floor(math.Log10(roughStep)))
	if base == 0 {
		return peak
	}
	for _, multiple := range [...]float64{1, 2, 2.5, 5, 10} {
		step := multiple * base
		if step >= roughStep {
			upper := math.Ceil(peak/step) * step
			if math.IsInf(upper, 0) {
				return peak
			}
			return upper
		}
	}
	return peak
}

// sparkPathsInWindow maps samples to their real position inside a fixed time
// window. Sparse or late-starting data therefore stays sparse instead of
// stretching the first and last sample across the whole chart.
func sparkPathsInWindow(points []store.NetPoint, from, to int64) (line, area string) {
	return sparkPathsInWindowScale(points, from, to, sparkScaleMax(sparkWindowMax(points, from, to)))
}

func sparkPathsInWindowScale(points []store.NetPoint, from, to int64, maxV float64) (line, area string) {
	if len(points) < 2 || to <= from {
		return "", ""
	}
	if maxV <= 0 {
		return "", ""
	}
	type point struct{ x, y float64 }
	var segments [][]point
	var segment []point
	gapMS := (to - from) / int64(dashMaxPoints) * 3
	if gapMS < int64(time.Minute/time.Millisecond) {
		gapMS = int64(time.Minute / time.Millisecond)
	}
	prev := int64(0)
	const pad = 3.0
	for _, p := range points {
		if p.Bucket < from || p.Bucket > to {
			continue
		}
		if len(segment) > 0 && p.Bucket-prev > gapMS {
			segments = append(segments, segment)
			segment = nil
		}
		x := float64(p.Bucket-from) / float64(to-from) * sparkW
		v := (p.Up + p.Down) / maxV
		y := sparkH - pad - v*(sparkH-2*pad)
		segment = append(segment, point{x: x, y: y})
		prev = p.Bucket
	}
	if len(segment) > 0 {
		segments = append(segments, segment)
	}
	var lines, areas strings.Builder
	for _, seg := range segments {
		if len(seg) == 1 {
			fmt.Fprintf(&lines, "M%.1f %.1f", seg[0].x, seg[0].y)
			continue
		}
		fmt.Fprintf(&lines, "M%.1f %.1f", seg[0].x, seg[0].y)
		fmt.Fprintf(&areas, "M%.1f %.1f", seg[0].x, seg[0].y)
		for _, p := range seg[1:] {
			fmt.Fprintf(&lines, " L%.1f %.1f", p.x, p.y)
			fmt.Fprintf(&areas, " L%.1f %.1f", p.x, p.y)
		}
		last, first := seg[len(seg)-1], seg[0]
		fmt.Fprintf(&areas, " L%.1f %.1f L%.1f %.1f Z", last.x, sparkH, first.x, sparkH)
	}
	return lines.String(), areas.String()
}

// levelClass 把占用率映射成配色档位。颜色表达状态，别让人去算数字。
func levelClass(pct float64) string {
	switch {
	case pct >= 85:
		return "bad"
	case pct >= 65:
		return "warn"
	default:
		return "ok"
	}
}

// ---- 三网延迟波动图 ----

// 小波用的虚拟坐标系，和模板里 SVG 的 viewBox 必须一致。
const (
	netqWaveW = 108.0
	netqWaveH = 22.0
)

// netqWavePaths 把一串延迟（毫秒，旧→新）转成 SVG 路径。
//
// 按序列**自身的 min~max** 缩放，不贴零轴：延迟都在几十毫秒量级抖动，
// 以零为参考系的话波动只有几个像素，图等于白画。上下各留 15% 余量，
// 峰值不顶边、谷值不贴底。
//
// 序列不足 2 个点或全平（min==max）时不给线 —— 全平画一条居中的直线，
// 会被误读成"接口抖动"，而"最近只有一轮数据"用空白表达更诚实。
func netqWavePaths(vals []float64) (line, area string) {
	if len(vals) < 2 {
		return "", ""
	}
	min, max, valid := math.Inf(1), math.Inf(-1), 0
	for _, v := range vals {
		if v <= 0 {
			continue
		}
		valid++
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	span := max - min
	if valid < 2 || span <= 0 {
		return "", ""
	}

	const pad = 2.0
	inner := netqWaveH - pad*2
	var b, a strings.Builder
	step := netqWaveW / float64(len(vals)-1)
	segment := make([][2]float64, 0, len(vals))
	flush := func() {
		if len(segment) == 0 {
			return
		}
		fmt.Fprintf(&b, "M%.1f %.1f", segment[0][0], segment[0][1])
		for _, pt := range segment[1:] {
			fmt.Fprintf(&b, " L%.1f %.1f", pt[0], pt[1])
		}
		if len(segment) > 1 {
			fmt.Fprintf(&a, "M%.1f %.1f", segment[0][0], netqWaveH)
			fmt.Fprintf(&a, " L%.1f %.1f", segment[0][0], segment[0][1])
			for _, pt := range segment[1:] {
				fmt.Fprintf(&a, " L%.1f %.1f", pt[0], pt[1])
			}
			fmt.Fprintf(&a, " L%.1f %.1f Z", segment[len(segment)-1][0], netqWaveH)
		}
		segment = segment[:0]
	}
	for i, v := range vals {
		if v <= 0 {
			flush()
			continue
		}
		x := float64(i) * step
		frac := 0.15 + (v-min)/span*0.7
		y := netqWaveH - pad - frac*inner
		segment = append(segment, [2]float64{x, y})
	}
	flush()
	return b.String(), a.String()
}

// resolveCoord 确定一台节点的坐标。
func resolveCoord(country, region, city string) (lat, lon float64, ok bool) {
	c, ok := geo.Lookup(country, region, city)
	if !ok {
		return 0, 0, false
	}
	return c.Lat, c.Lon, true
}

// latencyPct 把延迟映射成 0~100 的条长，以 300ms 为满格。
//
// 300ms 这个刻度是照着"境外小鸡"定的：国内互访普遍在 50ms 内，
// 300ms 已经是很差的跨洲线路，再长就该换个目标点了。
func latencyPct(ms float64) float64 {
	if ms <= 0 {
		return 0
	}
	return math.Min(100, ms/300*100)
}

// CPU/Mem/Disk 三行独立规格。
//
// ⚠️ 参数收**字段值**而不是 *model.Node：模板里传的是 nodeCard，
// 它内嵌了 model.Node 但类型不同，Go 模板**不会**自动转换——
// 传错了模板会在那一行静默中断，页面只剩半截，极难查。
//
// 原来拼成「1C / 967 MB / 14.7 GB」塞进一格；boss 觉得信息被压扁看不出全貌，
// 拆成独立项：CPU / 内存 / 磁盘。哪项缺就显示「—」，不写 "0C" 之类占位。
func specCPU(cores int) string {
	if cores > 0 {
		return fmt.Sprintf("%d 核", cores)
	}
	return "—"
}
func specMem(total int64) string {
	if total > 0 {
		return FmtBytes(total)
	}
	return "—"
}
func specDisk(total int64) string {
	if total > 0 {
		return FmtBytes(total)
	}
	return "—"
}

// staticVer 是静态资源的版本号。
//
// 改了 CSS/JS 必须同步 +1：宝塔默认给 js/css 设 12 小时缓存，
// URL 不变浏览器就吃旧文件，表现成"代码改了但页面没变"。
// 加 ?v= 是唯一能让缓存立即失效的办法。
const staticVer = "77"

// staticAssetURL 给静态资源拼上版本号。
func staticAssetURL(name string) string {
	return "/static/" + name + "?v=" + staticVer
}

// ---- 节点卡片分组 ----

// nodeGroup 是一组卡片，Title 是分组标题。
type nodeGroup struct {
	Key   string
	Title string
	Nodes []nodeCard
}

// nodeGroups 把卡片分成若干组，**保证至少返回一个组**。
//
// 为什么不分组时返回 nil：模板只有一种渲染路径（循环分组），
// 而"不分组"只是"一个没有标题的组"—— 这样卡片 HTML 一份就够，
// 不用分两套写法维护。分组标题为空时模板跳过 <h3>。
func nodeGroups(cards []nodeCard, by string) []nodeGroup {
	if g := groupCards(cards, by); len(g) > 0 {
		return g
	}
	return []nodeGroup{{Nodes: cards}}
}

// groupCards 按主题 layout.home.list.groupBy 的设置把卡片分组。
//
// 返回空表示"不分组"（groupBy 为空 / none）。
//
// ⚠️ **tag 是特例**：一台机器可以带多个标签，所以它会**同时出现在多个组**里。
// 这不是 bug —— 按标签分组本来就是"每个标签下都能看到它"。
func groupCards(cards []nodeCard, by string) []nodeGroup {
	if by == "" || by == "none" {
		return nil
	}
	var out []nodeGroup
	idx := map[string]int{}

	add := func(title string, c nodeCard) {
		if title == "" {
			title = "未分组"
		}
		key := by + ":" + title
		i, ok := idx[key]
		if !ok {
			out = append(out, nodeGroup{Key: key, Title: title})
			i = len(out) - 1
			idx[key] = i
		}
		out[i].Nodes = append(out[i].Nodes, c)
	}

	for _, c := range cards {
		switch by {
		case "status":
			if c.Online {
				add("在线", c)
			} else {
				add("离线", c)
			}
		case "region":
			add(c.Region, c)
		case "provider":
			add(c.Provider, c)
		case "group":
			add(c.Group, c)
		case "tag":
			if len(c.Tags) == 0 {
				add("未分组", c)
				continue
			}
			for _, t := range c.Tags {
				add(t, c)
			}
		}
	}
	return out
}
