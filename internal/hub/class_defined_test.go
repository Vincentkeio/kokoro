package hub

// 模板里写的 class，必须在某处有样式定义。
//
// 为什么值得单独钉一条：漏写样式**不会报任何错**，那段样式只是静默失效，
// 页面看着"有点怪"但说不出哪里怪。2026-10-11 一次扫描就抓到 5 个：
//
//	.col       告警面板的 Telegram 表单 / 首页购买表单 —— 行与行紧贴，
//	           boss 报的"TG 告警面板太挤"就是它
//	.i-hub     地球图例里的"主机"点，没有颜色（透明），而且和 .i-cur 重复
//	.map-foot  地球卡片脚注 —— 两段信息挤在一起
//	.dlg-title 弹窗标题 —— 只靠 <b> 默认加粗，和正文一样大
//	.skin-cur  皮肤下拉上的当前皮肤名
//
// 这些散落在不同页面、不同时间写的，靠肉眼是发现不了的。
//
// 判定范围要包含模板里的内联 <style>（netq / admin_post / admin_tasks /
// admin_themes 这些页面都是自带样式的），否则会满屏误报。

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// cssNotNeeded 是"故意不写样式"的例外。
//
// 加进来必须在注释里写清理由 —— 这个白名单一旦变成"懒得查就塞进来"，
// 这条测试就失去意义了。
var cssNotNeeded = map[string]bool{
	// 火花线的 SVG 分组，里面的 path 靠 SMIL 动画自己跑，分组本身不需要样式。
	"spark-flow": true,
	// netq 页面里"更多地区"那几行，纯粹给 JS 当查询钩子用的
	// （document.querySelectorAll('.nq-extra')），展开/收起靠兄弟类 nq-hidden。
	"nq-extra": true,
}

var (
	// ⚠️ 剥离顺序有讲究，必须先剥模板注释再剥动作：
	// `{{/* … */}}` 里的内容可能含 `}`（注释里写 CSS 片段就会），
	// 那样 `\{\{[^}]*\}\}` 就匹配不到，注释文字会漏进扫描范围，
	// 于是"注释里提到的类名"被当成真实用法报出来。
	tmplCommentRe = regexp.MustCompile(`(?s)\{\{/\*.*?\*/\}\}`)
	htmlCommentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	tmplActionRe  = regexp.MustCompile(`\{\{[^}]*\}\}`)
	classAttrRe   = regexp.MustCompile(`class="([^"]*)"`)
	styleTagRe    = regexp.MustCompile(`(?s)<style>(.*?)</style>`)
	identRe       = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)
)

// classTokens 从一段 class 属性值里取出类名。
//
// 两个必须的处理：
//  1. 先把模板动作 `{{...}}` 抹掉 —— `/admin` 的选项卡写的是
//     `class="mode-btn{{if eq .Theme.ListMode "card"}} on{{end}}"`，
//     动作里带引号，会把 `class="([^"]*)"` 提前截断，剩半截动作文本，
//     里面的 `eq` 就被当成类名报出来；
//  2. 丢掉以 `-` / `_` 结尾的片段 —— 那是拼出来的前缀
//     （`class="lv-{{.Level}}"` 会留下 `lv-`）。
func classTokens(s string) []string {
	s = tmplActionRe.ReplaceAllString(s, " ")
	var out []string
	for _, tok := range strings.Fields(s) {
		if !identRe.MatchString(tok) {
			continue
		}
		if strings.HasSuffix(tok, "-") || strings.HasSuffix(tok, "_") {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// hasRule 判断样式表里是否存在 `.name` 这条选择器。
//
// 用 `($|[^-a-zA-Z0-9_])` 而不是 `\b`：Go 的正则不支持 lookahead，而 `\b`
// 会把 `.col-x` 里的 `.col` 也判成命中（`-` 是非单词字符，那里也算边界）。
func hasRule(css, name string) bool {
	re := regexp.MustCompile(`\.` + regexp.QuoteMeta(name) + `($|[^-a-zA-Z0-9_])`)
	return re.MatchString(css)
}

func TestTemplateClassesHaveStyles(t *testing.T) {
	entries, err := fs.ReadDir(templatesFS, "templates")
	if err != nil {
		t.Fatalf("读模板目录失败: %v", err)
	}

	// 样式池 = style.css + 每个模板的内联 <style>
	var cssPool strings.Builder
	cssFile, err := fs.ReadFile(staticFS, "static/style.css")
	if err != nil {
		t.Fatalf("读样式表失败: %v", err)
	}
	cssPool.Write(cssFile)

	type usage struct{ file string }
	used := map[string]usage{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".html") {
			continue
		}
		raw, err := fs.ReadFile(templatesFS, "templates/"+e.Name())
		if err != nil {
			t.Fatalf("读 %s 失败: %v", e.Name(), err)
		}
		// 先剥两类注释、再剥模板动作，之后才做提取 ——
		// 这样"注释里提到的类名"和"动作里带引号"都不会干扰判断。
		clean := htmlCommentRe.ReplaceAllString(
			tmplCommentRe.ReplaceAllString(string(raw), " "), " ")
		clean = tmplActionRe.ReplaceAllString(clean, " ")

		for _, m := range styleTagRe.FindAllStringSubmatch(clean, -1) {
			cssPool.WriteString("\n")
			cssPool.WriteString(m[1])
		}
		for _, m := range classAttrRe.FindAllStringSubmatch(clean, -1) {
			for _, tok := range classTokens(m[1]) {
				if _, ok := used[tok]; !ok {
					used[tok] = usage{file: e.Name()}
				}
			}
		}
	}

	css := cssPool.String()
	var missing []string
	for name, u := range used {
		if cssNotNeeded[name] {
			continue
		}
		if !hasRule(css, name) {
			missing = append(missing, name+"（"+u.file+"）")
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("这些 class 在模板里用了、但没有任何样式定义（静默失效）：\n  %s\n"+
			"要么补样式，要么确认它确实不需要样式后加进 cssNotNeeded 并写明理由。",
			strings.Join(missing, "\n  "))
	}
}
