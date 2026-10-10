package hub

// 「获取本站皮肤」的测试。
//
// 这条入口是主题功能的反方向：后台那个是「我去抄别人」，
// 这个是「别人怎么抄我」。没有它，访客看到喜欢的皮肤只能自己猜
// 有没有 /theme.json 这种东西。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/theme"
)

func TestSiteBaseURL(t *testing.T) {
	// 配了 --domain：优先用它（反代后面 r.Host 可能是内网名）。
	h, _ := newThemeTestHubWithDomain(t, "vps.example.com")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "127.0.0.1:8799"
	if got := h.siteBaseURL(req); got != "https://vps.example.com" {
		t.Errorf("配了 --domain 时应优先用它，得到 %q", got)
	}

	// 没配 --domain：退回请求的 Host。
	h2, _ := newThemeTestHubWithDomain(t, "")
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Host = "panel.example.net"
	if got := h2.siteBaseURL(req2); got != "https://panel.example.net" {
		t.Errorf("没配 --domain 时应退回 r.Host，得到 %q", got)
	}

	// 已经带 scheme 的不能被拼成 https://https://…，末尾斜杠要去掉。
	h3, _ := newThemeTestHubWithDomain(t, "https://a.example.com/")
	if got := h3.siteBaseURL(nil); got != "https://a.example.com" {
		t.Errorf("带 scheme + 末尾斜杠时得到 %q", got)
	}
}

// 弹窗必须在访客能进的页面上都存在，并且地址是完整的对外地址。
func TestSkinGetDialogOnPublicPages(t *testing.T) {
	h, st := newThemeTestHubWithDomain(t, "vps.example.com")
	node := &model.Node{Name: "东京 zouter", Visibility: model.VisibilityPublic}
	if err := st.CreateNode(node); err != nil {
		t.Fatalf("造节点失败: %v", err)
	}

	for _, path := range []string{"/", "/n/" + node.Slug} {
		body := renderBody(t, h, httptest.NewRequest(http.MethodGet, path, nil))

		if !strings.Contains(body, "data-skinget") {
			t.Errorf("%s 缺少「获取本站皮肤」入口", path)
		}
		if !strings.Contains(body, `id="skinget-scrim"`) {
			t.Errorf("%s 缺少获取皮肤弹窗的 DOM", path)
		}
		// 复制框里必须是完整对外地址：相对路径复制到别人的输入框里
		// 什么都不算，这是这个功能能不能用的关键。
		if !strings.Contains(body, "https://vps.example.com") {
			t.Errorf("%s 的地址框里不是完整对外地址", path)
		}
		// 当前这套皮肤的下载入口（JSON 与完整包）。
		if !strings.Contains(body, "/theme-export/"+theme.DefaultID) {
			t.Errorf("%s 缺少导出 JSON 的链接", path)
		}
		if !strings.Contains(body, "/theme-bundle/"+theme.DefaultID) {
			t.Errorf("%s 缺少下载完整包的链接", path)
		}
		// 弹窗不能嵌在 <details> 里 —— details 一收起就把弹窗一起藏了，
		// 表现成"点了按钮没反应"。所以它必须在 </details> 之后。
		de := strings.Index(body, "</details>")
		si := strings.Index(body, `id="skinget-scrim"`)
		if de < 0 || si < 0 {
			t.Errorf("%s 找不到 </details> 或弹窗节点", path)
		} else if si < de {
			t.Errorf("%s 的弹窗被放进了 <details> 内部，收起时会被一起藏掉", path)
		}
	}
}
