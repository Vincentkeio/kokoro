package hub

// 首页的「信息面板」部分：时钟、统计卡、点阵地球。
//
// 单独一个文件是因为 renderHome 本来就不短，再往里塞统计与地理计算会失控。
// 这里只做「把已有数据整理成模板能直接渲染的形状」，不碰存储与渲染细节。

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/geo"
	"github.com/Vincentkeio/kokoro/internal/store"
)

// ---- 时钟 ----

// clockZone 是时钟条上的一格。
//
// 只输出**时区名**，具体时间由前端用 Intl 算。
// 服务端算时间会踩两个坑：一是服务器时区未必是访客的，二是 SSR 出来的时间
// 到浏览器显示时已经过去几百毫秒，秒针会"跳"。
// clockZone 是时钟条上的一格。具体时间由前端算，服务端只给"怎么算"：
//
//	TZ 非空  -> 用 Intl 按 IANA 时区名算（自动处理夏令时）
//	HasOffset -> 用固定 UTC 偏移算
//
// 后者给"主机时间"用：服务器不一定设了 IANA 时区（常常只是 Local），
// 而我们要的就是**这台机器自己的墙上时间**，固定偏移反而最准。
type clockZone struct {
	Label     string
	TZ        string // IANA 时区名（用 Intl 算，自动处理夏令时）
	OffsetMin int    // 相对 UTC 的分钟数（东八区 = 480）
	HasOffset bool   // 用固定偏移算，见下面的说明
	Note      string // 补充说明，例如 "UTC+08:00"
}

// homeClocks 只返回**主机自己的时间**。
//
// 原来铺了 本地/UTC/东京/洛杉矶/法兰克福 五格，占一整行却没什么信息量——
// 站长真正关心的是"面板这台机器现在几点"（对日志、对备份窗口都靠它）。
// homeClocks 返回时钟条要显示的时区。
//
// 固定几个主要落点即可，不必按节点动态生成——那样节点一多会挤满一行，
// 而且"主机在哪个时区"才是这里真正想表达的信息。
func homeClocks() []clockZone {
	now := time.Now()
	_, off := now.Zone() // 东八区 = 28800
	sign, abs := "+", off
	if off < 0 {
		sign, abs = "-", -off
	}
	return []clockZone{{
		Label:     "主机时间",
		OffsetMin: off / 60,
		HasOffset: true,
		Note:      fmt.Sprintf("UTC%s%02d:%02d", sign, abs/3600, (abs%3600)/60),
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
	Key    string  `json:"key"`
	Name   string  `json:"name"`
	Sub    string  `json:"sub"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	Online bool    `json:"online"`
}

type globeData struct {
	Hub    globePlace   `json:"hub"`
	Places []globePlace `json:"places"`
	HasHub bool         `json:"hasHub"`
}

// buildGlobe 组装地球数据。
//
// 主机坐标来自后台设置（hub_lat / hub_lon）。**不猜、不自动定位**：
// 猜错的表现是所有航线都连到错误的大洲，比干脆不画航线更误导。
// 没配置时 HasHub=false，前端只画节点点阵、不画航线。
func (h *Hub) buildGlobe(cards []nodeCard) globeData {
	var g globeData
	g.Places = make([]globePlace, 0, len(cards))
	for _, c := range cards {
		if !c.HasCoord {
			continue
		}
		g.Places = append(g.Places, globePlace{
			Key:    c.ID,
			Name:   c.Name,
			Sub:    c.RegionLabel(),
			Lat:    c.Lat,
			Lon:    c.Lon,
			Online: c.Online,
		})
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
const staticVer = "59"

// staticAssetURL 给静态资源拼上版本号。
func staticAssetURL(name string) string {
	return "/static/" + name + "?v=" + staticVer
}
