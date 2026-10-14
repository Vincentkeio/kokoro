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
