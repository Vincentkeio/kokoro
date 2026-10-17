package hub

// 后台「主题」页：切换主题、导入、一键获取别人站点的主题、删除、导出。

import (
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/netprobe"
	"github.com/Vincentkeio/kokoro/internal/theme"
)

// themeCard 是后台主题选择器里的一张卡。
type themeCard struct {
	ID      string
	Name    string
	Desc    string
	Author  string
	Version string
	License string
	Tags    []string
	// Swatch 是色板。类型是 template.CSS 而不是 string ——
	// 它要原样出现在 style 属性里，而普通字符串会被 html/template 的
	// cssValueFilter 无脑拒掉一切含括号的合法颜色（详见 swatchesCSS）。
	Swatch  []template.CSS
	Active  bool
	Builtin bool
	// Source 是这份主题的来源地址（一键获取或贴 GitHub 链接时记下）。
	// 只作追溯用：主题拉到本地后与来源断开关联，不检查更新。
	Source string
	// Layout 摘要是给管理员看的"这套主题改了什么"，用中文直说而不是让他猜。
	Summary string
}

// fillThemePanel 把「主题」页签需要的数据填进 adminData。
//
// 主题面板挂在 /admin 里（不再单独一页），所以这些数据是**后台页面数据的一部分**，
// 由 adminBase 统一带上 —— 少一个"进后台时漏填"的机会。
func (h *Hub) fillThemePanel(d *adminData, r *http.Request) {
	d.Msg = strings.TrimSpace(r.URL.Query().Get("msg"))
	d.ActiveID = h.ActiveThemeID()
	if h.themes != nil {
		for _, m := range h.themes.List() {
			d.Themes = append(d.Themes, themeCard{
				ID:      m.ID,
				Name:    m.Name,
				Desc:    m.Description,
				Author:  m.Author,
				Version: m.Version,
				License: m.License,
				Tags:    m.Tags,
				Swatch:  swatchesCSS(m.Swatches),
				Active:  m.ID == d.ActiveID,
				Builtin: m.Builtin,
				Source:  m.SourceURL,
				Summary: themeSummary(m),
			})
		}
	}
	d.TokenNames = theme.AllowedTokenNames()
	d.Wallpaper = h.wallpaperFor(strings.TrimSpace(r.URL.Query().Get("wperr")))
	// 三网分省探测范围：跟主题同一个填充入口，免得"进后台漏填"。
	d.NetQProvinces = h.netqProvinces()
	sel := make(map[string]bool, len(d.NetQProvinces))
	for _, p := range d.NetQProvinces {
		sel[p] = true
	}
	// 每个省的本机实测通率（近 2 天全部机器聚合）—— 社区表只提供
	// 候选 IP，"通不通"以我们小鸡实测为准（boss 2026-10-12）。
	hit := h.netqProvHitRates()
	for _, p := range netprobe.Provinces() {
		st := hit[p]
		d.NetQProvOpts = append(d.NetQProvOpts, netqProvOption{
			Name: p, On: sel[p],
			HitPct: st.Pct, Samples: st.N,
		})
	}
	d.NetQTargets = netqTargetCount(d.NetQProvinces)
	d.NetQInterval = h.netqInterval()
}

// netqProvOption 是后台探测范围里的一个省份勾选框。
type netqProvOption struct {
	Name string
	On   bool
	// HitPct/Samples 是该省的**本机实测**通率与样本数（近 2 天全部
	// 机器聚合）。通不通以我们小鸡 ping 的结果为准（boss 2026-10-12），
	// 社区表只提供候选 IP。
	HitPct  float64
	Samples int
}

// netqProvHitRate 是某个省份的本机实测通率。
type netqProvHitRate struct {
	N   int     // 近 2 天的探测点数（样本数）
	Pct float64 // 通的占比（0-100）
}

// netqProvHitRates 聚合各机器近 2 天的上报，算出每省通率。
//
// 为什么以本机为准：上游社区表（spiritLHLS/icmp_targets）的"可用"
// 是从国内机器爬的，我们的小鸡全在海外，同一批 IP 实测大片不通
// （2026-10-12 实测 28+ 个格子 0%）。勾选范围时给站长看到真实通率，
// 0% 的省直接在后台标灰，不用勾了才发现整页红叉。
func (h *Hub) netqProvHitRates() map[string]netqProvHitRate {
	h.netqHitMu.Lock()
	defer h.netqHitMu.Unlock()
	if h.netqHitData != nil && time.Since(h.netqHitAt) < time.Minute {
		return h.netqHitData
	}
	out := map[string]netqProvHitRate{}
	rows, err := h.store.DB().Query(
		`SELECT province, COUNT(*), SUM(ok) FROM net_quality
		 WHERE ts >= ? GROUP BY province`,
		time.Now().Add(-48*time.Hour).UnixMilli())
	if err != nil {
		log.Printf("[hub] 聚合探测点通率失败: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var prov string
		var n, okn int
		if err := rows.Scan(&prov, &n, &okn); err != nil {
			continue
		}
		if n <= 0 {
			continue
		}
		out[prov] = netqProvHitRate{N: n, Pct: float64(okn) * 100 / float64(n)}
	}
	if rows.Err() == nil {
		h.netqHitData, h.netqHitAt = out, time.Now()
	}
	return out
}

// swatchesCSS 把色板颜色转成能直出进 style 属性的 template.CSS。
//
// 为什么需要一层转换：色板渲染成 <i style="background:…">，而
// html/template 对 style 属性里的**普通字符串**会过 cssValueFilter ——
// 它见到括号就整体拒掉（返回 ZgotmplZ），于是 rgba() / hsl() /
// color-mix() 这些**令牌沙箱放行**的合法颜色在后台主题卡上直接少一个
// 色块，且不报错（2026-10-11 毛玻璃主题的 rgba 表面色就这么挂过）。
//
// 标成 template.CSS 就绕过了那个过滤器，所以必须在 Go 侧把安全补回来：
// theme.SafeColorValue 用沙箱同级规则复核（是合法颜色 + 不含能闭合
// style 属性的引号/反斜杠）。**有一条不满足就丢掉这个色板** ——
// 宁可少展示一个色块，也不能输出一个坏属性。
func swatchesCSS(in []string) []template.CSS {
	out := make([]template.CSS, 0, len(in))
	for _, v := range in {
		if theme.SafeColorValue(v) {
			out = append(out, template.CSS(v))
		}
	}
	return out
}

// 注：原来这里有个 handleAdminThemes 渲染**独立页面** /admin/themes。
// 主题面板已经并进 /admin 的「主题」页签（boss 要求后台所有区块都是页签，
// 不要单独跳一页），所以页面渲染那部分删掉了；
// 留下的是 /admin/themes/import、/grab、/delete 这几个**动作**端点，
// 它们本来就不渲染页面（做完 Redirect 回 /admin#themes）。

// themeSummary 用一句中文说清这套主题的布局取向。
// 管理员在后台看到的应该是"这台机器上会变成什么样"，而不是一堆 JSON 键名。
func themeSummary(m *theme.Manifest) string {
	l := m.Layout
	bits := []string{"列表 " + listModeName(l.Home.List.Mode)}

	if l.Home.Hero.Enabled != nil && *l.Home.Hero.Enabled {
		bits = append(bits, "首页有 Hero 区（"+heroVariantName(l.Home.Hero.Variant)+"）")
	}
	switch l.Detail.Header.Variant {
	case "hero":
		bits = append(bits, "详情页大图头")
	case "compact":
		bits = append(bits, "详情页紧凑头")
	case "none":
		bits = append(bits, "详情页无头图")
	}
	if l.Charts.Type != "" && l.Charts.Type != "area" {
		bits = append(bits, "图表用"+chartTypeName(l.Charts.Type))
	}
	if m.Swatches != nil {
		bits = append(bits, "字体 "+fontName(m))
	}
	if !m.SupportsDark() {
		bits = append(bits, "仅浅色")
	}
	return strings.Join(bits, " · ")
}

func listModeName(m string) string {
	switch m {
	case "card":
		return "卡片"
	case "table":
		return "表格"
	case "compact":
		return "紧凑行"
	case "map":
		return "地图"
	}
	return m
}

func heroVariantName(v string) string {
	switch v {
	case "plain":
		return "素色"
	case "gradient":
		return "渐变"
	case "image":
		return "配图"
	case "split":
		return "左右分栏"
	}
	return v
}

func chartTypeName(v string) string {
	switch v {
	case "line":
		return "折线"
	case "area":
		return "面积"
	case "bar":
		return "柱状"
	}
	return v
}

// fontName 从 tokens 里判断是不是等宽/衬线，给一句人话。
func fontName(m *theme.Manifest) string {
	f := m.Tokens["--kokoro-font-sans"]
	lf := strings.ToLower(f)
	switch {
	case strings.Contains(lf, "mono") && !strings.Contains(lf, "system-ui"):
		return "等宽"
	case strings.Contains(lf, "serif") || strings.Contains(lf, "georgia"):
		return "衬线"
	}
	// 没声明就是继承命名表默认的系统无衬线栈。
	return "无衬线"
}

// hasPrefix 是给模板用的前缀判断（.Msg 的成功/失败分色）。
func hasPrefix(s, p string) bool { return strings.HasPrefix(s, p) }

// themePickURL 生成主题切换链接，并带上"切换后回到哪"。
func themePickURL(id, back string) string {
	q := url.Values{}
	q.Set("back", back)
	return "/theme/" + id + "?" + q.Encode()
}

// siteBaseURL 返回本站的对外根地址（不带末尾斜杠）。
//
// 优先用启动参数 --domain：反代架构下 r.Host 是内网主机名或 127.0.0.1，
// 拿去告诉访客"填这个地址"是错的。没配 --domain 才退回请求里的 Host。
//
// 这个值会出现在「获取本站皮肤」的复制框里，所以它必须是一个
// 别人能直接填进自己面板、且真的能连上的地址。
func (h *Hub) siteBaseURL(r *http.Request) string {
	base := ""
	if h.cfg != nil {
		base = strings.TrimSpace(h.cfg.Domain)
	}
	if base == "" && r != nil {
		base = r.Host
	}
	if base == "" {
		return ""
	}
	if !strings.HasPrefix(base, "http") {
		base = "https://" + base
	}
	return strings.TrimRight(base, "/")
}

// adminBase 造一份只填了站点级字段的 adminData。
//
// 后台各子页面（节点、告警、主题…）都需要站点名与 Hub 地址，
// 但不需要把节点列表、告警规则全查一遍——那几页用不到，白查白费。
func (h *Hub) adminBase(r *http.Request) adminData {
	d := adminData{
		SiteName:         h.cfg.SiteName,
		HubURL:           h.siteBaseURL(r),
		CommentOn:        h.commentEnabled(),
		AutoApprove:      h.autoApprove(),
		OwnerName:        h.owner().Name,
		OwnerDisplayName: h.owner().Name,
		OwnerBio:         h.owner().Bio,
		OwnerAvatar:      h.owner().Avatar,
		AdminUser:        h.adminUsername(),
		CC:               countryOptions(),
	}
	// 主题面板挂在 /admin 里，所以每次构造后台数据都要带上它。
	h.fillThemePanel(&d, r)
	return d
}
