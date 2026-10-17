package hub

// 正文 Markdown 的渲染，含字体 / 字号那套白名单标签。

import (
	"strings"
	"testing"
)

func TestMarkdownBasic(t *testing.T) {
	got := string(RenderMarkdown("# 标题\n\n这是**粗体**和`代码`。"))
	for _, want := range []string{"<h3", "<strong>", "<code>"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少 %s（实际: %s）", want, got)
		}
	}
}

// TestMarkdownFontSpan 字体 / 字号用 `{lg}…{/lg}` 这种白名单标签。
func TestMarkdownFontSpan(t *testing.T) {
	cases := map[string]string{
		"{lg}大{/lg}":       `<span class="md-lg">大</span>`,
		"{sm}小{/sm}":       `<span class="md-sm">小</span>`,
		"{serif}宋{/serif}": `<span class="md-serif">宋</span>`,
		"{mono}码{/mono}":   `<span class="md-mono">码</span>`,
	}
	for in, want := range cases {
		if got := string(RenderMarkdown(in)); !strings.Contains(got, want) {
			t.Errorf("%q 应渲染出 %q，实际 %q", in, want, got)
		}
	}
}

// TestMarkdownSpanWhitelistOnly 白名单**之外**的标签必须原样当文字。
//
// 这是安全底线：本渲染器不放行内联 HTML，字体功能也不能成为
// "从文本里造出任意标签"的口子。未知标签一律不解释。
func TestMarkdownSpanWhitelistOnly(t *testing.T) {
	for _, in := range []string{
		"{evil}x{/evil}",
		"{script}x{/script}",
		"{onerror}x{/onerror}",
		"{style}x{/style}",
	} {
		got := string(RenderMarkdown(in))
		if strings.Contains(got, "<span") {
			t.Errorf("%q 不该被解释成 span，实际 %q", in, got)
		}
		if !strings.Contains(got, "x") {
			t.Errorf("%q 原样文字也丢了，实际 %q", in, got)
		}
	}
}

// TestMarkdownSpanTagsMustMatch 开闭标签对不上就不解释。
func TestMarkdownSpanTagsMustMatch(t *testing.T) {
	got := string(RenderMarkdown("{lg}x{/mono}"))
	if strings.Contains(got, "<span") {
		t.Errorf("开闭标签不一致却渲染成了 span: %q", got)
	}
}

// TestMarkdownEscapesHTML 渲染器整体不放行 HTML（它的安全底线）。
func TestMarkdownEscapesHTML(t *testing.T) {
	got := string(RenderMarkdown("<script>alert(1)</script>"))
	if strings.Contains(got, "<script") {
		t.Errorf("HTML 被放行了: %q", got)
	}
	if !strings.Contains(got, "&lt;script") {
		t.Errorf("HTML 没被转义: %q", got)
	}
}

func TestMarkdownRichArticle(t *testing.T) {
	input := "## 配置\n\n| 项目 | 内容 |\n| --- | --- |\n| 系统 | Debian |\n\n- 一级\n  - 二级\n\n- [x] 已完成\n\n~~旧内容~~\n\n![截图](https://example.com/a_(1).png \"配图\")\n\n![上传](/images/test.png)\n\n```go\nfunc main() {\n  println(\"**原样代码**\")\n}\n```\n\n`**不是加粗**`\n\n{lg}**大号强调**{/lg}"
	got := string(RenderMarkdown(input))
	for _, want := range []string{"<table>", "<th>项目</th>", "<del>旧内容</del>", `type="checkbox"`, `src="https://example.com/a_(1).png"`, `src="/images/test.png"`, `alt="截图"`, `title="配图"`, `class="md-codeblock"`, `data-copy-code`, `class="kd">func`, `**原样代码**`, `<code>**不是加粗**</code>`, `<span class="md-lg"><strong>大号强调</strong></span>`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if strings.Count(got, "<ul>") < 2 {
		t.Errorf("nested lists lost: %s", got)
	}
}

func TestMarkdownDangerousDestinations(t *testing.T) {
	for _, src := range []string{
		`![x](javascript:alert(1))`, `![x](data:image/svg+xml;base64,PHN2Zz4=)`,
		`![x](//evil.example/x.png)`, `![x](mailto:a@example.com)`,
		`[x](JaVaScRiPt:alert(1))`, `[x](java&#x73;cript:alert(1))`,
		`<img src=x onerror=alert(1)>`,
	} {
		got := string(RenderMarkdown(src))
		if strings.Contains(got, `<img `) || strings.Contains(got, `<a href=`) {
			t.Errorf("unsafe destination accepted: %q => %s", src, got)
		}
	}
	got := string(RenderMarkdown("```html\n<script>alert(1)</script>\n```"))
	if strings.Contains(got, "<script>") {
		t.Fatalf("code block escaped its boundary: %s", got)
	}
}

func TestMarkdownCodeFenceFallback(t *testing.T) {
	for _, src := range []string{"~~~\na < b\n~~~", "```unknown-language\na < b\n```", "```sh\necho ok"} {
		got := string(RenderMarkdown(src))
		if !strings.Contains(got, `class="md-codeblock"`) || !strings.Contains(got, `<pre class="chroma">`) {
			t.Errorf("unformatted code fence: %s", got)
		}
	}
}
