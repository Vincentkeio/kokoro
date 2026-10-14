package hub

// 小鸡博客页的正文用的是 Markdown，但这里不引入任何第三方解析库：
// 一是体积（一个二进制要保持 10MB 量级），二是安全（正文由机器主人填写，
// 但 Kokoro 是公开站点，宁可少支持几个语法，也不能让 HTML 漏进来）。
//
// 支持的子集：标题、段落、无序/有序列表、引用、分隔线、围栏代码块，
// 以及行内的 `代码`、**粗体**、*斜体*、[链接](url)。
//
// 安全底线：所有文本先 html.EscapeString，再拼标签；链接只允许 http/https/mailto/相对路径。

import (
	"html"
	"html/template"
	"regexp"
	"strings"
)

var (
	reInlineCode = regexp.MustCompile("`([^`]+)`")
	reBold       = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	reItalic     = regexp.MustCompile(`(^|[^*])\*([^*\n]+)\*`)
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reListItem   = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)

	// reSpan 匹配 `{标签}文字{/标签}` —— 字体字号用的。
	//
	// Markdown 没有设置字体/字号的语法，而本渲染器**不放行内联 HTML**
	// （那是它的安全底线）。所以另开一套**白名单标签**：
	// 只有下面 mdSpans 里列出的几个名字会被翻译成 `<span class>`，
	// 其它一律原样输出成文字。
	// ⚠️ 不能用 `\{/\1\}` —— Go 的 regexp 是 RE2，**不支持反向引用**，
	// MustCompile 会直接 panic（整个包起不来）。改成把开闭标签各捕获一次，
	// 在代码里比对是否一致。
	reSpan = regexp.MustCompile(`\{([a-z]+)\}(.*?)\{/([a-z]+)\}`)
)

// mdSpans 是字体/字号的白名单：标签名 → CSS 类名。
//
// ⚠️ 这里是**封闭集合**：值写死成类名，用户没法塞进 style/onclick/url。
// 想加新样式只能在源码里加，这样"样式"永远不可能变成"脚本"。
var mdSpans = map[string]string{
	"lg":    "md-lg",    // 大号
	"sm":    "md-sm",    // 小号
	"serif": "md-serif", // 衬线体
	"mono":  "md-mono",  // 等宽体
}

// RenderMarkdown 把一小段 Markdown 渲染成安全的 HTML 片段。
func RenderMarkdown(src string) template.HTML {
	src = strings.ReplaceAll(src, "\r\n", "\n")
	src = strings.ReplaceAll(src, "\r", "\n")
	lines := strings.Split(src, "\n")

	var b strings.Builder
	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		switch {
		case trimmed == "":
			i++

		case strings.HasPrefix(trimmed, "```"):
			lang := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			i++
			var code []string
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				code = append(code, html.EscapeString(lines[i]))
				i++
			}
			i++ // 吃掉收尾的 fence
			b.WriteString(`<pre><code`)
			if lang != "" && len(lang) <= 16 {
				b.WriteString(` class="lang-`)
				b.WriteString(html.EscapeString(lang))
				b.WriteString(`"`)
			}
			b.WriteString(">")
			b.WriteString(strings.Join(code, "\n"))
			b.WriteString("</code></pre>\n")

		case strings.HasPrefix(trimmed, "---") && strings.Trim(trimmed, "- ") == "":
			b.WriteString("<hr>\n")
			i++

		case strings.HasPrefix(trimmed, "> "):
			var quote []string
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if !strings.HasPrefix(t, "> ") {
					break
				}
				quote = append(quote, inlineHTML(strings.TrimPrefix(t, "> ")))
				i++
			}
			b.WriteString("<blockquote>")
			b.WriteString(strings.Join(quote, "<br>"))
			b.WriteString("</blockquote>\n")

		case strings.HasPrefix(trimmed, "#"):
			lvl := 0
			for lvl < len(trimmed) && trimmed[lvl] == '#' && lvl < 6 {
				lvl++
			}
			txt := strings.TrimSpace(trimmed[lvl:])
			if txt == "" {
				i++
				continue
			}
			// 正文里的标题一律从 h3 起，页面层级留给 <h1>/<h2>
			tag := lvl + 2
			if tag > 6 {
				tag = 6
			}
			b.WriteString("<h")
			b.WriteByte(byte('0' + tag))
			b.WriteString(">")
			b.WriteString(inlineHTML(txt))
			b.WriteString("</h")
			b.WriteByte(byte('0' + tag))
			b.WriteString(">\n")
			i++

		case reListItem.MatchString(line):
			ordered := false
			if m := reListItem.FindStringSubmatch(line); len(m) > 2 {
				c := m[2][0]
				ordered = c >= '0' && c <= '9'
			}
			tag := "ul"
			if ordered {
				tag = "ol"
			}
			b.WriteString("<")
			b.WriteString(tag)
			b.WriteString(">")
			for i < len(lines) {
				m := reListItem.FindStringSubmatch(lines[i])
				if m == nil {
					break
				}
				b.WriteString("<li>")
				b.WriteString(inlineHTML(m[3]))
				b.WriteString("</li>")
				i++
			}
			b.WriteString("</")
			b.WriteString(tag)
			b.WriteString(">\n")

		default:
			var para []string
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if t == "" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, "```") ||
					strings.HasPrefix(t, "> ") || reListItem.MatchString(lines[i]) ||
					(strings.HasPrefix(t, "---") && strings.Trim(t, "- ") == "") {
					break
				}
				para = append(para, inlineHTML(t))
				i++
			}
			b.WriteString("<p>")
			b.WriteString(strings.Join(para, "<br>"))
			b.WriteString("</p>\n")
		}
	}
	return template.HTML(b.String())
}

// inlineHTML 处理行内语法。入参是原文，出参是已经转义过的安全 HTML。
func inlineHTML(s string) string {
	s = html.EscapeString(s)
	// 行内代码优先：代码块里的 * 不该被当成强调
	// 字体/字号：先处理，免得里面的 * 被后面的强调规则吃掉
	s = reSpan.ReplaceAllStringFunc(s, func(m string) string {
		sub := reSpan.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		// 开闭标签必须一致（RE2 没有反向引用，只能在这里比）
		if sub[1] != sub[3] {
			return m
		}
		cls, ok := mdSpans[sub[1]]
		if !ok {
			return m // 不在白名单里 → 原样当文字，绝不解释
		}
		return `<span class="` + cls + `">` + sub[2] + `</span>`
	})
	s = reInlineCode.ReplaceAllString(s, "<code>$1</code>")
	s = reBold.ReplaceAllString(s, "<strong>$1</strong>")
	s = reItalic.ReplaceAllString(s, "$1<em>$2</em>")
	s = reLink.ReplaceAllStringFunc(s, func(m string) string {
		sub := reLink.FindStringSubmatch(m)
		if len(sub) < 3 || !safeHref(sub[2]) {
			return sub[1]
		}
		return `<a href="` + sub[2] + `" rel="ugc nofollow noopener" target="_blank">` + sub[1] + `</a>`
	})
	return s
}

// safeHref 只放行 http/https/mailto 与站内相对链接，挡掉 javascript: 之类。
// 入参已经过 HTML 转义，比较的是转义后的字面量。
func safeHref(u string) bool {
	low := strings.ToLower(u)
	switch {
	case strings.HasPrefix(low, "http://"), strings.HasPrefix(low, "https://"),
		strings.HasPrefix(low, "mailto:"), strings.HasPrefix(low, "#"),
		strings.HasPrefix(low, "/"):
		return true
	}
	return false
}
