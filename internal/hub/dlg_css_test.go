package hub

// 弹窗默认必须**收起**。
//
// 踩过的坑：HTML 的 hidden 属性靠浏览器默认样式表的
// `[hidden] { display: none }` 生效，但作者样式里
// `.dlg { display: flex }` **优先级更高**，会把它盖掉 ——
// 表现成"弹窗一直显示、关不掉"，而**控制台一个错都不报**。
//
// 这类"CSS 静默失效"没法用端到端测试抓（要跑真浏览器），
// 所以在样式表上做个静态断言：必须显式写 .dlg[hidden]。

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDlgHiddenRuleExists(t *testing.T) {
	b, err := fs.ReadFile(staticFS, "static/style.css")
	if err != nil {
		t.Fatalf("读样式表失败: %v", err)
	}
	css := string(b)

	// 必须有显式的 [hidden] 覆盖规则
	if !strings.Contains(css, ".dlg[hidden]") ||
		!strings.Contains(css, ".dlg-scrim[hidden]") {
		t.Error("样式表里缺少 .dlg[hidden] / .dlg-scrim[hidden] 规则 —— " +
			"弹窗会一直显示，因为 .dlg 的 display:flex 会盖掉 hidden 属性")
	}

	// 顺带确认 .dlg 确实设了 display —— 如果哪天改成 block，
	// 这条断言会提醒我们回头看看 hidden 那条还需不需要
	if !strings.Contains(css, ".dlg {") {
		t.Error("样式表里找不到 .dlg 规则")
	}
}

// TestDlgMarkupStartsHidden 两个弹窗在 HTML 里必须是 hidden 的。
func TestDlgMarkupStartsHidden(t *testing.T) {
	h, st := newTestHub(t)
	mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))

	// 每个 aside 都要带 hidden 属性
	for _, id := range []string{"d-art", "d-cmt"} {
		i := strings.Index(body, `id="`+id+`"`)
		if i < 0 {
			t.Errorf("找不到弹窗 %s", id)
			continue
		}
		// 往前后各看一点，确认 hidden 在同一条标签里
		seg := body[i-120 : i+120]
		if !strings.Contains(seg, "hidden") {
			t.Errorf("弹窗 %s 没有 hidden 属性 —— 打开页面就会显示出来", id)
		}
	}
}
