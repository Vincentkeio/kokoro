package hub

// 「下载 AI 提示词」这条路径。
//
// 这个功能的价值全在"下下来的东西能直接喂给 AI"：如果 embed 漏了文件、
// 或者发出去的是空文件/HTML 错误页，用户只会以为"AI 读不懂"，
// 不会想到是下载坏了 —— 所以两头都要钉住：内容对不对、入口在不在。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThemeAIPromptDownloadable(t *testing.T) {
	h, _ := newTestHub(t)

	req := httptest.NewRequest(http.MethodGet, "/theme-ai-prompt.md", nil)
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，应为 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "markdown") {
		t.Errorf("Content-Type = %q，应标记为 markdown", ct)
	}
	// 带 attachment 才算"下载"——不然浏览器会直接渲染 markdown 源码，
	// 用户还得自己全选复制，丢了格式。
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q，应触发下载", cd)
	}

	body := w.Body.String()
	if len(body) < 2000 {
		t.Fatalf("提示词只有 %d 字节，太短了 —— embed 是不是漏了文件？", len(body))
	}
	// 抽几条只有真提示词才有的内容，确认不是错误页/登录页。
	for _, want := range []string{
		"Kokoro",     // 说清了这是给什么写主题
		"--kokoro-",  // 变量前缀
		"theme.json", // 产出物
		"kokoro.",    // 官方保留前缀的警告
	} {
		if !strings.Contains(body, want) {
			t.Errorf("提示词里没有 %q，内容不对", want)
		}
	}
	if strings.Contains(body, "<html") {
		t.Error("返回的是 HTML，说明走到别的分支去了")
	}
}

func TestThemePageLinksToAIPrompt(t *testing.T) {
	h, _ := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)

	req := httptest.NewRequest(http.MethodGet, "/admin/themes", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("主题页状态码 = %d", w.Code)
	}
	// 没有入口的功能等于没有：用户不会去猜 /theme-ai-prompt.md 这个地址。
	if !strings.Contains(w.Body.String(), `href="/theme-ai-prompt.md"`) {
		t.Error("主题页缺少「下载 AI 提示词」的链接")
	}
}
