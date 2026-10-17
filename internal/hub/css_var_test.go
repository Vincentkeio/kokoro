package hub

// 样式表里 `var(--x)` 引用的自定义属性，必须在某处有定义。
//
// 为什么值得单独钉一条：`var()` 取不到值**不会报错**，那条声明直接失效
// （注意不是退回初始值，是整条声明作废），于是：
//
//	--accent-soft  首页「全网速率」图的上行面积没有填充色 → 回落成
//	               SVG 默认的**黑色**，一直像"深色填充的设计"
//	--s7           .foot-page 等三处的 padding 整条失效 → 用浏览器默认值
//	--radius-sm    netq 页卡片没有圆角（这处应该是笔误，本项目叫 --r-sm）
//
// 三个都是 2026-10-11 做毛玻璃主题时一次性扫出来的，肉眼完全看不出来。
//
// 判定口径：
//   - 只查**不带 fallback** 的 `var(--x)`；`var(--x, 12px)` 有兜底，不算问题；
//   - `--kokoro-*` 一律视为已定义 —— 它们由主题在运行时注入，
//     合法名单在 internal/theme 里；
//   - JS 里 setProperty 设的变量也算已定义（扫 app.js / globe.js 的字符串）。

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/theme"
)

var (
	cssVarRefRe = regexp.MustCompile(`var\(\s*(--[a-zA-Z0-9-]+)\s*\)`)
	cssVarDefRe = regexp.MustCompile(`(--[a-zA-Z0-9-]+)\s*:`)
)

func TestCSSVariablesAreDefined(t *testing.T) {
	// 先把所有 CSS 文本拼起来：style.css + 各模板的内联 <style>
	var css strings.Builder
	main, err := fs.ReadFile(staticFS, "static/style.css")
	if err != nil {
		t.Fatalf("读 style.css 失败: %v", err)
	}
	css.Write(main)

	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		t.Fatalf("读模板目录失败: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		raw, err := fs.ReadFile(templatesFS, "templates/"+e.Name())
		if err != nil {
			continue
		}
		for _, m := range regexp.MustCompile(`(?s)<style>(.*?)</style>`).
			FindAllStringSubmatch(string(raw), -1) {
			css.WriteString("\n")
			css.WriteString(m[1])
		}
	}
	text := css.String()

	defs := map[string]bool{}
	for _, m := range cssVarDefRe.FindAllStringSubmatch(text, -1) {
		defs[m[1]] = true
	}
	// 主题注入的变量：合法名单在 internal/theme，一律视为已定义
	for _, n := range theme.AllowedTokenNames() {
		defs[n] = true
	}
	// JS 动态设的（扫 JS 源码里的字面量，够用了）
	for _, js := range []string{"static/app.js", "static/globe.js"} {
		raw, err := fs.ReadFile(staticFS, js)
		if err != nil {
			continue
		}
		for _, m := range regexp.MustCompile(`--[a-zA-Z0-9-]+`).
			FindAllString(string(raw), -1) {
			defs[m] = true
		}
	}

	var missing []string
	for _, m := range cssVarRefRe.FindAllStringSubmatch(text, -1) {
		if !defs[m[1]] {
			missing = append(missing, m[1])
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("这些 CSS 变量被 var() 引用了、但从来没有定义过（那条声明会静默失效）：\n  %s\n"+
			"引用它们的地方拿不到值，整条 CSS 声明就作废了 —— 不报错、只是不生效。",
			strings.Join(missing, "\n  "))
	}
}

// TestStickyHeaderNeverTransparent 钉住「顶栏永远不许透」。
//
// .top 是 position:sticky 的，内容滚过去会从页眉**里面**穿出来。
// 2026-10-10~11 连翻三次车，全是同一个形状：
//  1. bordered 变体把 background 设成 transparent（霓虹主题，详情页
//     一滚，网络质量卡的字从页眉后面透出来）；
//  2. transparent 变体同理；
//  3. 主题引擎生成的裸属性规则把 background 简写落在 body 上，
//     顺带清掉壁纸。
//
// 所以立个不变量：凡是以 .top 为目标的规则，不许出现
// background(-color): transparent。半透明 + backdrop-filter（blur 变体）
// 是刻意设计，放行。
func TestStickyHeaderNeverTransparent(t *testing.T) {
	main, err := fs.ReadFile(staticFS, "static/style.css")
	if err != nil {
		t.Fatalf("读 style.css 失败: %v", err)
	}
	ruleRe := regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`)
	var bad []string
	for _, m := range ruleRe.FindAllStringSubmatch(string(main), -1) {
		sel, block := m[1], m[2]
		// 只盯瞄准 .top 自身的规则；.top-owner / .top-clock / .top-tools 不算。
		// 判据是每条选择器的**主语**（逗号分开、取最后一个复合选择器）：
		//  `.top .top-owner` 的主语是 .top-owner（名牌里的元素），
		// 把它当成"顶栏变透明"是误伤 —— 顶栏本体仍由主语为 .top 的规则守。
		hit := false
		for _, one := range strings.Split(sel, ",") {
			parts := strings.FieldsFunc(one, func(r rune) bool {
				return r == '>' || r == '+' || r == '~' || r == ' ' || r == '\t' || r == '\n'
			})
			if len(parts) == 0 {
				continue
			}
			subject := strings.TrimSpace(parts[len(parts)-1])
			if subject == ".top" || strings.HasPrefix(subject, ".top.") ||
				strings.HasPrefix(subject, ".top:") || strings.HasPrefix(subject, ".top[") {
				hit = true
			}
		}
		if !hit {
			continue
		}
		if strings.Contains(block, "background:transparent") ||
			strings.Contains(block, "background: transparent") ||
			strings.Contains(block, "background-color:transparent") ||
			strings.Contains(block, "background-color: transparent") {
			bad = append(bad, strings.Join(strings.Fields(sel), " ")+" { "+strings.Join(strings.Fields(block), " ")+" }")
		}
	}
	if len(bad) > 0 {
		t.Errorf("sticky 顶栏不许透明（内容会从页眉里透出来）：\n  %s\n"+
			"要换顶栏长相请改背景色/边框，别擦透明。",
			strings.Join(bad, "\n  "))
	}
}
