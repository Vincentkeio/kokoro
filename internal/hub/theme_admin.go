package hub

// 后台「主题」页：切换主题、导入、一键获取别人站点的主题、删除、导出。

import (
	"net/http"
	"net/url"
	"strings"

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
	Swatch  []string
	Active  bool
	Builtin bool
	// Source 是这份主题的来源地址（一键获取或贴 GitHub 链接时记下）。
	// 只作追溯用：主题拉到本地后与来源断开关联，不检查更新。
	Source string
	// Layout 摘要是给管理员看的"这套主题改了什么"，用中文直说而不是让他猜。
	Summary string
}

// themePageData 是 /admin/themes 的页面数据。
type themePageData struct {
	adminData
	Themes   []themeCard
	Msg      string
	Err      bool
	ActiveID string
	// TokenNames 列出可用的 CSS 变量名，方便管理员复制内置主题去改。
	TokenNames []string
	// Wallpaper 是站点壁纸面板的数据（上传 / URL / 清除）。
	Wallpaper wallpaperView
}

func (h *Hub) handleAdminThemes(w http.ResponseWriter, r *http.Request, authed bool) {
	if !authed {
		h.render(w, "login.html", map[string]any{"SiteName": h.cfg.SiteName}, r)
		return
	}
	msg := strings.TrimSpace(r.URL.Query().Get("msg"))
	data := themePageData{
		adminData: h.adminBase(r),
		Msg:       msg,
		ActiveID:  h.ActiveThemeID(),
		Wallpaper: h.wallpaperFor(strings.TrimSpace(r.URL.Query().Get("wperr"))),
	}
	if h.themes != nil {
		active := data.ActiveID
		for _, m := range h.themes.List() {
			data.Themes = append(data.Themes, themeCard{
				ID:      m.ID,
				Name:    m.Name,
				Desc:    m.Description,
				Author:  m.Author,
				Version: m.Version,
				License: m.License,
				Tags:    m.Tags,
				Swatch:  m.Swatches,
				Active:  m.ID == active,
				Builtin: m.Builtin,
				Source:  m.SourceURL,
				Summary: themeSummary(m),
			})
		}
	}
	data.TokenNames = theme.AllowedTokenNames()
	h.render(w, "admin_themes.html", &data, r)
}

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
	return adminData{
		SiteName:    h.cfg.SiteName,
		HubURL:      h.siteBaseURL(r),
		CommentOn:   h.commentEnabled(),
		AutoApprove: h.autoApprove(),
		OwnerName:   h.owner().Name,
		OwnerBio:    h.owner().Bio,
		OwnerAvatar: h.owner().Avatar,
		AdminUser:   h.adminUsername(),
		CC:          countryOptions(),
	}
}
