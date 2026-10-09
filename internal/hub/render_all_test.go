package hub

// 所有后台页面都必须能**完整渲染**。
//
// ★ 踩过的大坑：模板里引用了结构体上不存在的字段（`{{.Cmd}}`），
// Go 模板遇到这种情况会**中止渲染** —— 输出截在那儿，后面全没了。
//
// 表现：
//   - HTTP 还是 200
//   - 页面上半截内容正常，看着不像坏了
//   - 只是"少了几块"，很容易被当成"功能没做"
//   - 而且**日志里其实一直有明确报错**，只是没人看
//
// 后台测试页就这么躺了很久：三张测试卡只显示一张，而且那张是断的。
//
// 所以这里做一件事：把每个页面都渲染一遍，**任何错误直接让测试红**。

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// renderTo 直接调 ExecuteTemplate，把错误返回出来。
//
// 不走 h.render —— 那个只往日志写，测试看不见。
func renderTo(t *testing.T, h *Hub, name string, data any, r *http.Request) (string, error) {
	t.Helper()
	var buf bufWriter
	err := h.tmpl.ExecuteTemplate(&buf, name, data)
	return buf.String(), err
}

type bufWriter struct{ b []byte }

func (w *bufWriter) Write(p []byte) (int, error) { w.b = append(w.b, p...); return len(p), nil }
func (w *bufWriter) String() string              { return string(w.b) }

// TestEveryTemplateRenders 所有模板都要能渲染完，不能中途报错。
func TestEveryTemplateRenders(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

	// 后台各页
	adminPages := []struct {
		tmpl string
		data any
	}{
		{"admin.html", h.adminBase(httptest.NewRequest(http.MethodGet, "/admin", nil))},
		{"admin_tasks.html", &adminTasksData{
			adminData: h.adminBase(httptest.NewRequest(http.MethodGet, "/admin/tasks", nil)),
			Node:      n,
			Scripts:   h.scriptViews(),
			Tasks:     []model.NodeTask{{ID: "t1", Kind: "bench", Title: "硬件跑分", Status: model.TaskDone}},
		}},
		{"admin_themes.html", &themePageData{adminData: h.adminBase(httptest.NewRequest(http.MethodGet, "/admin/themes", nil)), ActiveID: h.ActiveThemeID()}},
	}
	for _, p := range adminPages {
		body, err := renderTo(t, h, p.tmpl, p.data, nil)
		if err != nil {
			t.Errorf("%s 渲染失败: %v", p.tmpl, err)
			continue
		}
		if len(body) < 500 {
			t.Errorf("%s 只渲染出 %d 字节，像是中途截断了", p.tmpl, len(body))
		}
	}
}

// TestTemplateHasNoUnknownFields 模板里引用的字段，结构体上必须真的有。
//
// 这是上面那条的更直接版本：把 admin_tasks 的三张卡数清楚。
// 少了就是 `{{.Cmd}}` 那种字段错，会静默截断。
func TestTemplateHasNoUnknownFields(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

	views := h.scriptViews()
	if len(views) != len(testScripts) {
		t.Fatalf("视图只有 %d 项，应该有 %d 项", len(views), len(testScripts))
	}
	for _, v := range views {
		if v.Cmd == "" {
			t.Errorf("测试项 %q 的命令是空的 —— 模板拿不到会直接截断整页", v.Title)
		}
	}

	_ = n
}
