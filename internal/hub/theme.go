package hub

// 主题接线：把 internal/theme 的结果落到 HTTP 上。
//
// 三件事：
//  1. 站点级主题（Hub 内存 + DB settings），影响所有人；
//  2. 访客级深浅色（cookie），只影响一个人；
//  3. 「一键获取别人站点的主题」：从远程 /theme.json 拉取、校验、登记。
//
// 设计取舍：
//   - 主题选择放内存 + DB 而不是每次请求查库：主题读取在每个页面的 <head>，
//     一次请求要读一次，用锁读缓存最省。
//   - tokens CSS 渲染一次缓存在内存（内容与请求无关），只有 layout 部分随
//     访客的列表模式变。
//   - 主题 CSS 里不引用任何远程资源，所以不需要 nonce 之外的额外 CSP 处理；
//     但仍然走 <style nonce>，保持与其他内联样式一致。

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Vincentkeio/kokoro/internal/theme"
)

// settingTheme 是站点级主题的 settings 键。
const settingTheme = "site_theme"

// themeCache 缓存渲染产物，避免每个请求都拼一遍上百个 CSS 变量。
type themeCache struct {
	mu        sync.RWMutex
	activeID  string
	m         *theme.Manifest
	tokens    string
	layoutCSS string
}

// themes 暴露主题注册表。
func (h *Hub) Themes() *theme.Registry { return h.themes }

// themeFetchPolicy 汇总「允许抓回环」的主机名白名单。
//
// 两个来源：
//  1. 启动参数 --domain：本站自己的域名。反代架构下（Nginx 在前、Go 在后）
//     抓自己的站就是走回环，这是最常见的正当用途，不该让站长额外配置一遍；
//  2. settings 里的 theme_fetch_hosts：管理员显式填的额外名字。
//
// 除此之外的一切回环目标都拒绝——包括本机 Docker 映射出来的数据库、
// 其他监听在本地端口上的面板等等。
func (h *Hub) themeFetchPolicy() themeLoopbackPolicy {
	hosts := make(map[string]bool)
	if h.cfg != nil && strings.TrimSpace(h.cfg.Domain) != "" {
		hosts[strings.ToLower(strings.TrimSpace(h.cfg.Domain))] = true
	}
	if h.store != nil {
		if raw, err := h.store.GetSetting(settingThemeFetchHosts); err == nil {
			for k := range parseThemeFetchHosts(raw) {
				hosts[k] = true
			}
		}
	}
	return themeLoopbackPolicy{hosts: hosts}
}

// ActiveTheme 返回当前生效的主题。
func (h *Hub) ActiveTheme() *theme.Manifest { return h.theme.m }

// loadTheme 在启动时把内置主题灌进注册表，并从 DB 读回上次选的主题。
func (h *Hub) loadTheme() {
	reg, err := theme.NewRegistry()
	if err != nil {
		// 内置主题解析失败是编译期问题，但不该让 Hub 起不来——退回最朴素的一层。
		log.Printf("[theme] 内置主题加载失败，本次以默认样式启动: %v", err)
		return
	}
	h.themes = reg
	// 先把数据库里的自定义主题装进注册表，再决定启用哪一套——
	// 顺序反了的话，settings 里指向自定义主题的记录会解析不到。
	h.loadStoredThemes()

	active := ""
	if s, err := h.store.GetSetting(settingTheme); err == nil {
		active = strings.TrimSpace(s)
	}
	h.setActiveTheme(active)
}

// setActiveTheme 切换站点主题并重建缓存。id 为空表示默认主题。
func (h *Hub) setActiveTheme(id string) {
	if h.themes == nil {
		return
	}
	m := h.themes.Resolve(id)
	h.theme.mu.Lock()
	h.theme.activeID = m.ID
	h.theme.m = m
	h.theme.tokens = theme.RenderTokens(m)
	h.theme.layoutCSS = theme.RenderLayoutCSS(m)
	h.theme.mu.Unlock()
	if m.ID != id && id != "" {
		// 用户选了一个不存在的 id（比如卸载了主题），静默回落即可，不必惊动管理员。
		log.Printf("[theme] 主题 %q 不存在，已回落到 %s", id, m.ID)
	}
}

// ActiveThemeID 返回当前主题 ID。
func (h *Hub) ActiveThemeID() string {
	h.theme.mu.RLock()
	defer h.theme.mu.RUnlock()
	return h.theme.activeID
}

// ---- 模板数据 ----

// themeView 是模板里用的主题上下文。
type themeView struct {
	ID          string
	Name        string
	Desc        string
	Author      string
	Version     string
	Tokens      template.CSS      // <style> 内的 CSS 变量
	LayoutCSS   template.CSS      // 由 layout 生成的结构规则
	Attrs       template.HTMLAttr // <body> 上的 data-k-* 属性
	Mode        string            // light | dark
	DefaultMode string
	All         []*theme.Manifest
	Builtin     bool
	// ShowSwitch 报告是否给访客看深浅色切换按钮。
	ShowSwitch bool
	// SwitchID 是「换到下一套主题」的跳转地址（内置主题轮换）。
	SwitchID string
	// ListMode 是本次请求生效的列表模式。
	ListMode string
	// BackURL 是"切换完回到哪一页"，存原始值（未转义）。
	// 模板里用 urlquery 转义一次即可；Go 侧先 QueryEscape 会被转两次，
	// / 变成 %252F，服务端解出来不是站内路径，只能被 safeBackURL 丢掉。
	BackURL string
	// Pickable 是访客可自选的主题列表。
	//
	// 与 All 的区别：All 是"站点装了哪些"（含后台导入的社区主题），
	// Pickable 是"访客能自己切到哪些"。默认只放开内置主题——
	// 否则访客一键就能把整站换成社区主题，那不是"看看风格"而是换掉站点的脸。
	// 但站点当前正在用的主题一定要在列表里，否则访客切走后就没有回来的路。
	Pickable []*theme.Manifest
	// VisitorSwitch 报告是否给访客看外观切换入口。
	//
	// 只要站点装了至少一套主题就为真——**哪怕只有一套**。
	// 单主题时切片里只有一个选项，看起来"切了等于没切"，但它同时承担两件事：
	//  1. 让访客一眼看到"这个站支持换外观"，否则功能藏起来等于没有；
	//  2. 它是唯一能发现"我正用着哪套皮肤"的地方（当前项高亮）。
	// 所以不要因为选项少就把整条藏掉，那是把可发现性换成了整洁。
	VisitorSwitch bool
	// SiteThemeID 是站点默认主题，用于在切换器里标注"当前默认"。
	SiteThemeID string
	// VisitorOverride 报告本次请求是否用了访客自己的选择（而非站点默认）。
	VisitorOverride bool
}

// resolvePickable 返回访客可自选的主题列表。
//
// 就是站点已安装的全部主题，没有额外白名单。理由很直接：
// 访客的选择只落在自己的 k_theme cookie 里，**对其他任何人的页面零影响**，
// 所以不存在"被访客换掉站点的脸"这种风险——那是 /theme/ 那种站点级
// 切换才会有的问题，这里不是。
//
// 主题本身也不含机密：能装上这个站，说明管理员已经审过（导入时过了
// theme.Parse 的全部校验），访客能看的就是所有人都能看的那份清单。
//
// 反过来，如果只放开内置主题，站点导入的主题就成了只有管理员能预览的
// 东西——「看看别人小鸡用的什么皮肤」这个需求直接废掉。
//
// 列表为空（注册表没加载起来）时切换器不出现——那是真没得选。
// 只有一套时**仍然出现**：它是访客唯一能确认"本站支持换外观"
// 以及"我正在用哪套"的地方。
func (h *Hub) resolvePickable() []*theme.Manifest {
	if h.themes == nil {
		return nil
	}
	return h.themes.List()
}

// resolveMode 决定这次请求用浅色还是深色。
//
// 优先级：访客 cookie > 主题默认（auto 时看系统偏好，服务端无从得知，
// 交给前端内联脚本同步执行）。
func (h *Hub) resolveMode(r *http.Request, m *theme.Manifest) (mode, defaultMode string) {
	if !m.SupportsDark() {
		return "light", "light"
	}
	defaultMode = m.Mode.Default
	if defaultMode != "light" && defaultMode != "dark" {
		defaultMode = "light"
	}
	if c, err := r.Cookie(theme.ModeCookie); err == nil {
		switch c.Value {
		case "light", "dark":
			return c.Value, defaultMode
		}
	}
	return defaultMode, defaultMode
}

// resolveListMode 决定首页列表模式。访客可切换，存 cookie。
func (h *Hub) resolveListMode(r *http.Request, m *theme.Manifest) string {
	themeDefault := m.Layout.Home.List.Mode
	if themeDefault == "" {
		themeDefault = "card"
	}
	if !pickBoolDefault(m.Layout.Home.List.AllowUserSwitch, true) {
		return themeDefault
	}
	if c, err := r.Cookie(listModeCookie); err == nil {
		switch c.Value {
		case "card", "table", "compact":
			return c.Value
		}
	}
	return themeDefault
}

// listModeCookie 是访客选择的列表模式。
const listModeCookie = "k_list_mode"

// pickBoolDefault 是 theme 包里 pickBool 的本地副本，避免导出一个通用工具。
func pickBoolDefault(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// buildThemeView 组装模板要用的主题数据。
//
// m 应当是 requestManifest(r) 的结果（已含访客覆盖）。这里不再自己解析
// 访客 cookie——否则调用方传进来的 m 和页面实际生效的主题可能不是同一套，
// 就会出现「配色是A、Hero 布局是B」这种错位。
func (h *Hub) buildThemeView(r *http.Request, m *theme.Manifest) themeView {
	siteID := h.ActiveThemeID()
	mode, defMode := h.resolveMode(r, m)
	listMode := h.resolveListMode(r, m)

	h.theme.mu.RLock()
	tokens, layoutCSS := h.theme.tokens, h.theme.layoutCSS
	h.theme.mu.RUnlock()

	v := themeView{
		ID:          m.ID,
		Name:        m.Name,
		Desc:        m.Description,
		Author:      m.Author,
		Version:     m.Version,
		Tokens:      template.CSS(tokens),
		LayoutCSS:   template.CSS(layoutCSS),
		Mode:        mode,
		DefaultMode: defMode,
		Builtin:     m.Builtin,
		Attrs:       template.HTMLAttr(theme.AttrsString(theme.BodyAttrs(m, mode, listMode))),
		ShowSwitch:  m.AllowModeSwitch(),
		ListMode:    listMode,
		BackURL:     r.URL.RequestURI(),
		SiteThemeID: siteID,
	}
	// 访客选的不是站点默认 → 他需要一条"回到默认"的出路。
	v.VisitorOverride = m.ID != siteID

	if h.themes != nil {
		v.All = h.themes.List()
		pickable := h.resolvePickable()
		v.Pickable = pickable
		// 有一套就显示。单主题时只有一个选项——这是有意的，
		// 见 VisitorSwitch 字段上的说明：可发现性比"整洁"重要。
		v.VisitorSwitch = len(pickable) >= 1
		ids := h.themes.ListIDs()
		if len(ids) > 1 {
			for i, id := range ids {
				if id == m.ID {
					v.SwitchID = ids[(i+1)%len(ids)]
					break
				}
			}
		}
	}

	// 关键：访客选的主题不是站点当前那套时，缓存里的 CSS 不能用。
	// 缓存只存站点主题的产物；访客的选择必须现渲染。
	if m.ID != siteID {
		v.Tokens = template.CSS(theme.RenderTokens(m))
		v.LayoutCSS = template.CSS(theme.RenderLayoutCSS(m))
	}
	return v
}

// requestManifest 返回本次请求真正该用的主题：站点默认 + 访客覆盖。
//
// 页面上所有"受主题影响"的东西都必须走它——tokens、layout、body 属性、
// Hero 显隐、列表默认形态。少走一处就会出现局部错位。
func (h *Hub) requestManifest(r *http.Request) *theme.Manifest {
	if h.themes == nil {
		return nil
	}
	site := h.themes.Resolve(h.ActiveThemeID())
	if pick := h.visitorTheme(r, site); pick != nil {
		return pick
	}
	return site
}

// visitorTheme 读访客的主题偏好，返回 nil 表示"沿用站点默认"。
//
// 三道闸：
//  1. cookie 为空 → 直接沿用站点默认；
//  2. 主题必须真实存在（Get 非 nil）；
//  3. id 合法性交给 theme.ValidID 之外的整体兜底——
//     Get 查不到就是查不到，不会凭空造一个来。
//
// 少了第 2 条，别人就能靠手改 cookie 把 id 指向一份已删除但仍残留在
// 内存里的主题，或者塞进超长字符串让每页都白查一遍。
func (h *Hub) visitorTheme(r *http.Request, siteActive *theme.Manifest) *theme.Manifest {
	if h.themes == nil {
		return nil
	}
	c, err := r.Cookie(theme.ThemeCookie)
	if err != nil || strings.TrimSpace(c.Value) == "" {
		return nil
	}
	id := strings.TrimSpace(c.Value)
	if siteActive != nil && id == siteActive.ID {
		return nil
	}
	return h.themes.Get(id)
}

// ---- 路由 ----

// handleThemeSwitch 处理 GET /theme/<id>：切换站点主题（仅管理员）并跳转回来源页。
func (h *Hub) handleThemeSwitch(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		denyUnauthed(w, r)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/theme/")
	if id == "" {
		id = theme.DefaultID
	}
	if h.themes != nil && h.themes.Get(id) == nil {
		// 再给一次明确反馈，别让管理员对着空白页猜。
		http.Redirect(w, r, "/admin/themes?msg="+
			url.QueryEscape("没有这套主题："+id), http.StatusSeeOther)
		return
	}
	if err := h.store.SetSetting(settingTheme, id); err != nil {
		log.Printf("[theme] 保存主题失败: %v", err)
	}
	h.setActiveTheme(id)
	_ = h.store.AddAudit("admin", "theme.enable", id, "")
	// 只允许站内跳转，防止开放重定向。
	if back := r.FormValue("back"); safeBackURL(back) {
		http.Redirect(w, r, back, http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// handleThemePick 处理 GET /pick/<id>：访客自选主题（写 cookie，不改站点设置）。
//
// 与 /theme/<id> 的区别是权限语义：
//   - /theme/<id>   改站点默认主题，影响所有人，需要管理员；
//   - /pick/<id>    只改当前访客的浏览器外观，无权限要求。
//
// 因为访客的选择对别人无影响，id 的校验只需要"主题得存在"，
// 不需要额外白名单——站点装了哪套，访客就能试哪套。
func (h *Hub) handleThemePick(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/pick/")
	if h.themes == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	// 空 id 或者显式的 "default"/"site" = 回到站点默认（删掉 cookie）。
	if id == "" || id == "default" || id == "site" {
		h.clearVisitorTheme(w)
		h.redirectBack(w, r)
		return
	}
	pick := h.themes.Get(id)
	if pick == nil {
		http.NotFound(w, r)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     theme.ThemeCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   365 * 24 * 3600,
		SameSite: http.SameSiteLaxMode,
	})
	// 访客换皮肤后，之前记住的列表形态可能在新主题下不合适，清掉让它走新默认。
	h.clearListMode(w)
	h.redirectBack(w, r)
}

// clearVisitorTheme 清掉访客的主题选择，回到站点默认。
func (h *Hub) clearVisitorTheme(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: theme.ThemeCookie, Value: "", Path: "/",
		MaxAge: -1, SameSite: http.SameSiteLaxMode,
	})
}

// clearListMode 清掉访客的列表形态选择。
func (h *Hub) clearListMode(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: listModeCookie, Value: "", Path: "/",
		MaxAge: -1, SameSite: http.SameSiteLaxMode,
	})
}

// redirectBack 回到来源页，只允许站内地址（防开放重定向）。
func (h *Hub) redirectBack(w http.ResponseWriter, r *http.Request) {
	back := r.FormValue("back")
	if !safeBackURL(back) {
		back = "/"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// safeBackURL 判断跳转目标是否安全（必须站内、不能是协议相对 URL）。
func safeBackURL(back string) bool {
	if back == "" || !strings.HasPrefix(back, "/") {
		return false
	}
	if strings.HasPrefix(back, "//") {
		return false
	}
	if strings.Contains(back, "\\") || strings.Contains(back, ":") {
		return false
	}
	return true
}

// handleThemeImport 处理 POST /admin/themes/import：导入一份主题。
//
// 两种来源：
//   - 粘贴 theme.json 原文（最容易，也最安全——纯文本，没有包结构可攻击）；
//   - 上传 .kokoro-theme 包文件（能带字体、背景图）。
//
// 两者都要过 theme.Parse 的同一套校验，最后都落库。
func (h *Hub) handleThemeImport(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		denyUnauthed(w, r)
		return
	}
	// 表单可能带文件（multipart）或纯文本（urlencoded）。体积上限放宽到能容纳整个包。
	// 顺序：ParseForm 先跑（它对 multipart 不消费 body），再用 FormFile 取文件。
	r.Body = http.MaxBytesReader(w, r.Body, theme.MaxPackageBytes+(256<<10))
	if err := r.ParseForm(); err != nil {
		h.themeErr(w, "内容太大或格式错误", r)
		return
	}
	// 上传包优先：文件内容比粘贴框更"重"，同时存在时以文件为准。
	// FormFile 的第二个返回值是 *multipart.FileHeader（不是 error），
	// 所以「这次有没有带文件」只能靠 fh != nil 判断。
	if fh, _, _ := r.FormFile("bundle"); fh != nil {
		defer fh.Close()
		raw, rerr := io.ReadAll(io.LimitReader(fh, theme.MaxPackageBytes+1))
		if rerr != nil {
			h.themeErr(w, "读取上传文件失败", r)
			return
		}
		m, err := h.installBundle(raw, "")
		if err != nil {
			h.themeErr(w, err.Error(), r)
			return
		}
		_ = h.store.AddAudit("admin", "theme.import.bundle", m.ID, m.Name)
		h.themeErr(w, "", r, fmt.Sprintf("已导入主题包《%s》%s", m.Name, m.Version))
		return
	}

	raw := strings.TrimSpace(r.PostFormValue("manifest"))
	if raw == "" {
		h.themeErr(w, "内容为空（可以粘贴 theme.json，或上传 .kokoro-theme 包）", r)
		return
	}
	// 粘贴的也可能是包内容被直接粘进来（有人会把 .kokoro-theme 二进制贴成 base64 之外的东西），
	// 所以先看是不是 zip，是就走包路径。
	if strings.HasPrefix(raw, "PK") {
		decoded, derr := decodeMaybeBase64(raw)
		if derr == nil {
			m, err := h.installBundle(decoded, "")
			if err != nil {
				h.themeErr(w, err.Error(), r)
				return
			}
			_ = h.store.AddAudit("admin", "theme.import.bundle", m.ID, m.Name)
			h.themeErr(w, "", r, fmt.Sprintf("已导入主题包《%s》%s", m.Name, m.Version))
			return
		}
	}
	m, err := h.importManifest([]byte(raw), "")
	if err != nil {
		h.themeErr(w, err.Error(), r)
		return
	}
	_ = h.store.AddAudit("admin", "theme.import", m.ID, m.Name)
	h.themeErr(w, "", r, fmt.Sprintf("已导入《%s》%s，共 %d 套主题", m.Name, m.Version, len(h.themes.ListIDs())))
}

// installBundle 校验并安装一份 .kokoro-theme 包。
//
// 顺序：解包（限流 + zip-slip 防护）→ 校验清单 → 登记注册表 → 落库。
// 任何一步失败都不会留下半成品：注册表和数据库要么都成功，要么都不动。
func (h *Hub) installBundle(raw []byte, sourceURL string) (*theme.Manifest, error) {
	b, err := theme.ExtractPackage(raw)
	if err != nil {
		return nil, err
	}
	manifestRaw, err := b.ReadManifest()
	if err != nil {
		return nil, err
	}
	m, err := theme.Parse(manifestRaw)
	if err != nil {
		return nil, err
	}
	// 把「按实际内容算出」的包元数据并回清单，让后台能展示校验和与资源清单。
	m.Package = b.Meta()
	// 作者没在清单里写来源时，用抓取地址补上（导入时 sourceURL 为空，不补）。
	if m.SourceURL == "" {
		m.SourceURL = sourceURL
	}
	// 资源先进缓存：/_theme-assets/ 路由要从这里取字节。
	h.themeAssets.set(m.ID, b.Assets)
	if err := h.themes.Register(m); err != nil {
		h.themeAssets.drop(m.ID)
		return nil, err
	}
	if err := h.persistTheme(m, raw, sourceURL); err != nil {
		// 落库失败要回滚注册表，否则下次重启这份主题就凭空消失，
		// 而当前进程里还留着它——比「导入失败」更难排查。
		if rerr := h.themes.Unregister(m.ID); rerr != nil {
			log.Printf("[theme] 导入后落库失败且回滚注册表也失败: %v", rerr)
		}
		return nil, err
	}
	return m, nil
}

// importManifest 校验并登记一份 theme.json，同时落库。
func (h *Hub) importManifest(raw []byte, sourceURL string) (*theme.Manifest, error) {
	m, err := theme.Parse(raw)
	if err != nil {
		return nil, err
	}
	if err := h.themes.Register(m); err != nil {
		return nil, err
	}
	if sourceURL == "" {
		sourceURL = h.storedSourceURL(m.ID)
	}
	if m.SourceURL == "" {
		m.SourceURL = sourceURL
	}
	if err := h.persistTheme(m, nil, sourceURL); err != nil {
		// 注册表已登记但没落库 = 重启即丢，主动撤掉。
		if rerr := h.themes.Unregister(m.ID); rerr != nil {
			log.Printf("[theme] 登记后落库失败且回滚失败: %v", rerr)
		}
		return nil, err
	}
	return m, nil
}

// decodeMaybeBase64 尝试把一段文本当 base64 解码，用于支持「把包粘成 base64」的场景。
func decodeMaybeBase64(s string) ([]byte, error) {
	clean := strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, s)
	if len(clean) < 4 || len(clean)%4 != 0 {
		return nil, fmt.Errorf("不是合法的 base64")
	}
	return base64.StdEncoding.DecodeString(clean)
}

// handleThemeGrab 处理 POST /admin/themes/grab：一键获取另一个站的主题。
//
// 对方站点上有两种可获取物：
//   - /theme.json                  —— 当前生效主题的清单（任何站都有）；
//   - /theme-bundle/<id>           —— 当前生效主题的完整包（含字体/背景等资源）。
//
// 优先抓包；对方没有包就退回清单。但「包存在却没过校验」是硬失败，
// 不退回——理由见 grabTheme 里的注释。
func (h *Hub) handleThemeGrab(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		denyUnauthed(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		h.themeErr(w, "内容太大或格式错误", r)
		return
	}
	src := strings.TrimSpace(r.PostFormValue("url"))
	if !strings.HasPrefix(src, "http://") && !strings.HasPrefix(src, "https://") {
		h.themeErr(w, "请填写 http(s) 开头的地址", r)
		return
	}
	m, kind, err := h.grabTheme(src)
	if err != nil {
		h.themeErr(w, err.Error(), r)
		return
	}
	_ = h.store.AddAudit("admin", "theme.grab", m.ID, src)
	if kind == "bundle" {
		h.themeErr(w, "", r, fmt.Sprintf("已从 %s 获取主题包《%s》%s", src, m.Name, m.Version))
		return
	}
	h.themeErr(w, "", r, fmt.Sprintf("已从 %s 获取《%s》%s（对方未提供资源包，仅清单）", src, m.Name, m.Version))
}

// themeFetchTimeout 限制抓取别人站点的时间，别让后台卡住。
const themeFetchTimeout = 12 * time.Second

// themeMaxBytes 限制抓取内容的体积。包比清单大，所以按包的额度算。
const themeMaxBytes = theme.MaxPackageBytes + (64 << 10)

// newThemeGrabClient 造一个用于抓主题的 HTTP 客户端。
//
// 三道限制都装在这里，抽成函数是为了让「重定向目标是否被逐跳校验」
// 这条能在测试里直接断言，而不是只能靠间接观察最终结果。
func newThemeGrabClient(timeout time.Duration, policy themeLoopbackPolicy) *http.Client {
	if timeout <= 0 {
		timeout = themeFetchTimeout
	}
	return &http.Client{
		Timeout: timeout,
		// 自定义 Transport 而不是复用 DefaultTransport：只有走这个
		// Transport 的请求才会过 safeThemeDialer 的 IP 校验。
		Transport: &http.Transport{
			DialContext:       safeThemeDialer(timeout, policy),
			Proxy:             nil, // 抓主题不经代理，避免代理把请求改道到内网
			DisableKeepAlives: true,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return fmt.Errorf("重定向次数过多")
			}
			// 每一跳都要重新校验：对方可以先给一个正常响应，
			// 再用 302 把我们送去 169.254.169.254 读云元数据。
			if _, err := safeThemeURL(req.URL.String()); err != nil {
				return fmt.Errorf("重定向目标不安全: %w", err)
			}
			return nil
		},
	}
}

// themeFetcher 返回一个「取一个 URL 的字节」的函数。
//
// 抽出来是为了让通用抓取与 GitHub 抓取共用同一套出口约束：
// 超时、体积上限、拒绝空响应、且绝不带上管理员的 cookie。
func themeFetcher(client *http.Client) func(string) ([]byte, error) {
	return func(u string) ([]byte, error) {
		req, err := http.NewRequest(http.MethodGet, u, nil)
		if err != nil {
			return nil, fmt.Errorf("地址不合法: %w", err)
		}
		// 明确不要带着管理员的 cookie 去敲别人的站。
		req.Header.Set("User-Agent", "Kokoro-Theme-Grab")
		req.Header.Set("Accept", "application/json, application/zip, application/octet-stream")
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("抓取失败: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("对方返回 HTTP %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, themeMaxBytes))
		if err != nil {
			return nil, fmt.Errorf("读取失败: %w", err)
		}
		if len(body) == 0 {
			return nil, fmt.Errorf("对方返回了空内容")
		}
		return body, nil
	}
}

// grabTheme 从一个站点抓取主题并登记。第二个返回值是 "bundle" 或 "manifest"，
// 用来告诉用户这次拿到的是完整包还是只有配色清单。
//
// 贴的是 GitHub 链接时走另一条路（见 theme_github.go）——那边要把仓库页面
// 地址翻译成 raw 地址，不能按「站点根 + /theme.json」去拼。
func (h *Hub) grabTheme(src string) (*theme.Manifest, string, error) {
	if target, ok := parseGitHubThemeURL(src); ok {
		return h.grabGitHubTheme(src, target)
	}
	base, err := normalizeThemeBase(src)
	if err != nil {
		return nil, "", err
	}
	fetch := themeFetcher(newThemeGrabClient(themeFetchTimeout, h.themeFetchPolicy()))

	// 第一步：探当前生效主题的清单与 ID。
	if raw, err := fetch(base + "/theme.json"); err == nil && len(raw) > 0 {
		var probe struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(raw, &probe) == nil && theme.ValidID(probe.ID) {
			pkg, perr := fetch(base + "/theme-bundle/" + probe.ID)
			if perr != nil {
				// 没有包端点是很常见的情况（对方只开放了清单），退回即可。
				log.Printf("[theme] %s 没有提供主题包，按清单方式获取: %v", base, perr)
			} else if m, ierr := h.installBundle(pkg, base); ierr != nil {
				// 包拿到了但没过校验：这是明确的安全信号，不能装作没这回事
				// 然后转头去装同一站点的清单——那样等于给对方一次免费重试机会，
				// 攻击者只要把包做坏、清单做干净就能绕过。
				return nil, "", fmt.Errorf("对方的主题包未通过安全校验，已中止导入：%w", ierr)
			} else {
				return m, "bundle", nil
			}
		}
		// 第二步：退回裸清单。
		m, err := h.importManifest(raw, base)
		if err != nil {
			return nil, "", err
		}
		return m, "manifest", nil
	}

	// 用户可能直接填了具体的文件地址，那就不用再拼。
	if strings.HasSuffix(base, ".json") {
		raw, err := fetch(base)
		if err != nil {
			return nil, "", err
		}
		m, err := h.importManifest(raw, src)
		if err != nil {
			return nil, "", err
		}
		return m, "manifest", nil
	}
	raw, err := fetch(base + "/theme.json")
	if err != nil {
		return nil, "", err
	}
	m, err := h.importManifest(raw, base)
	if err != nil {
		return nil, "", err
	}
	return m, "manifest", nil
}

// normalizeThemeURL 把用户填的地址规整成 theme.json 的完整 URL。
func normalizeThemeURL(src string) (string, error) {
	u, err := normalizeThemeBase(src)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(u, ".json") {
		u += "/theme.json"
	}
	return u, nil
}

// normalizeThemeBase 把用户填的地址规整成站点根地址（不带末尾斜杠）。
//
// 允许三种写法：站点根、/theme.json 全路径、.kokoro-theme 包全路径。
func normalizeThemeBase(src string) (string, error) {
	u := strings.TrimSpace(src)
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return "", fmt.Errorf("请填写 http(s) 开头的地址")
	}
	u = strings.TrimRight(u, "/")
	for _, suf := range []string{"/theme.json", theme.PackageExt} {
		u = strings.TrimSuffix(u, suf)
	}
	return strings.TrimRight(u, "/"), nil
}

// handleThemeManifest 公开当前生效主题的 theme.json。
//
// 这是「一键获取别人探针的主题」的抓取端点，所以它必须无鉴权、且内容
// 恰好是对方导出时会给的那份（不掺运行时字段）。
func (h *Hub) handleThemeManifest(w http.ResponseWriter, r *http.Request) {
	m := h.themes.Resolve(h.ActiveThemeID())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Cache-Control", "public, max-age=60")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(exportManifest(m))
}

// exportManifest 把一份清单还原成可分享的标准 JSON。
// 内部运行时字段（Source / Swatches / Builtin）不该出现在分享文件里。
func exportManifest(m *theme.Manifest) map[string]any {
	out := map[string]any{
		"schemaVersion":       m.SchemaVersion,
		"id":                  m.ID,
		"name":                m.Name,
		"version":             m.Version,
		"description":         m.Description,
		"author":              m.Author,
		"license":             m.License,
		"tags":                m.Tags,
		"tokensSchemaVersion": m.TokensSchemaVersion,
		"tokens":              m.Tokens,
		"tokensDark":          m.TokensDark,
		"mode":                m.Mode,
		"layoutSchemaVersion": m.LayoutSchemaVersion,
		"layout":              m.Layout,
	}
	if m.Homepage != "" {
		out["homepage"] = m.Homepage
	}
	// 来源随包走：别人抄走你的主题时也带走出处，既是署名也是追溯线索。
	if m.SourceURL != "" {
		out["sourceUrl"] = m.SourceURL
	}
	return out
}

// handleThemeDelete 删除一套自定义主题（内置主题删不掉）。
func (h *Hub) handleThemeDelete(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		denyUnauthed(w, r)
		return
	}
	id := r.FormValue("id")
	if id == "" {
		http.Redirect(w, r, "/admin/themes?msg="+url.QueryEscape("缺少主题 ID"),
			http.StatusSeeOther)
		return
	}
	if err := h.themes.Unregister(id); err != nil {
		http.Redirect(w, r, "/admin/themes?msg="+url.QueryEscape(err.Error()),
			http.StatusSeeOther)
		return
	}
	// 资源缓存也要一并丢掉，否则 /_theme-assets/ 还能吐出一套已删除主题的图。
	h.themeAssets.drop(id)
	// 注册表和数据库都要删。只删前者的话，重启后这份主题会从数据库里
	// 满血复活——对管理员来说就是「我明明删了」。
	if err := h.removeTheme(id); err != nil {
		log.Printf("[theme] 删除主题存档失败 %s: %v", id, err)
		http.Redirect(w, r, "/admin/themes?msg="+url.QueryEscape("已从内存移除，但删除存档失败："+err.Error()),
			http.StatusSeeOther)
		return
	}
	_ = h.store.AddAudit("admin", "theme.delete", id, "")
	// 删掉的正好是当前主题时，立刻切回默认，避免渲染时拿到已注销的主题。
	if h.ActiveThemeID() == id {
		if err := h.store.SetSetting(settingTheme, theme.DefaultID); err != nil {
			log.Printf("[theme] 删除后保存主题失败: %v", err)
		}
		h.setActiveTheme(theme.DefaultID)
	}
	http.Redirect(w, r, "/admin/themes?msg="+url.QueryEscape("已删除《"+id+"》"),
		http.StatusSeeOther)
}

// handleThemeExport 导出当前主题的 theme.json，方便分享给别人。
func (h *Hub) handleThemeExport(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/theme-export/")
	m := h.themes.Resolve(id)
	data, err := json.MarshalIndent(exportManifest(m), "", "  ")
	if err != nil {
		http.Error(w, "导出失败", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="kokoro-theme-%s.json"`, m.ID))
	w.Write(data)
}

// handleThemeBundle 导出当前主题的 .kokoro-theme 完整包。
//
// 这是「一键获取」的对端端点：对方站的 grab 会先打 /theme.json 问 ID，
// 再来这个地址要包。所以它必须公开、无鉴权。
//
// 内置主题没有存档包，就现场用 theme.BuildPackage 打一个——
// 这也顺带证明了「内置主题能被打成可分发包」这条路径是通的。
func (h *Hub) handleThemeBundle(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/theme-bundle/")
	m := h.themes.Resolve(id)
	// 已有原始包就原样发出去（哈希与作者声明完全一致）。
	if raw, ok := h.storedBundle(m.ID); ok {
		writeBundle(w, raw, m.ID)
		return
	}
	raw, err := theme.BuildPackage(m, nil, nil, m.License, "")
	if err != nil {
		http.Error(w, "生成主题包失败: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeBundle(w, raw, m.ID)
}

func writeBundle(w http.ResponseWriter, raw []byte, id string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="theme-%s%s"`, id, theme.PackageExt))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(raw)
}

// themeErr 统一的主题操作反馈：成功时 msg 非空，失败时走 err。
func (h *Hub) themeErr(w http.ResponseWriter, errMsg string, r *http.Request, okMsg ...string) {
	msg := "已保存"
	if errMsg != "" {
		msg = "导入失败：" + errMsg
	} else if len(okMsg) > 0 {
		msg = okMsg[0]
	}
	// 用 query 传反馈，不引 session/flash 中间件——省一个依赖，也少一处状态。
	http.Redirect(w, r, "/admin/themes?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}
