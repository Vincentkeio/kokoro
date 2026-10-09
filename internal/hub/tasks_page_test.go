package hub

// 后台测试页：三个单独测试项都要列出来。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTasksPageListsAllScripts(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

	body := adminBody(t, h, st) // 先确认后台能登录
	_ = body

	r := httptest.NewRequest(http.MethodGet, "/admin/tasks?node="+n.ID, nil)
	w := httptest.NewRecorder()
	h.handleAdminTasks(w, r, true)
	page := w.Body.String()

	t.Logf("testScripts 有 %d 项", len(testScripts))
	if c := strings.Count(page, `class="script-card"`); c != len(testScripts) {
		t.Errorf("页面上有 %d 个测试卡片，应该是 %d 个", c, len(testScripts))
	}
	for _, s := range testScripts {
		if !strings.Contains(page, s.Title) {
			t.Errorf("测试项 %q 没出现在页面上", s.Title)
		}
		// 命令是渲染时用 benchCmd(Only) 拼的，不是结构体字段
		if !strings.Contains(page, "--only") {
			t.Errorf("测试项 %q 的命令没渲染出来", s.Title)
		}
		// 每张卡都要有「下发」按钮
		if c := strings.Count(page, ">下发<"); c < len(testScripts) {
			t.Errorf("「下发」按钮只有 %d 个，应该至少 %d 个", c, len(testScripts))
		}
	}
}
