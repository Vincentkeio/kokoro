package hub

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThemeExampleDownloadable(t *testing.T) {
	h, _ := newTestHub(t)
	req := httptest.NewRequest(http.MethodGet, "/theme-example.md", nil)
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d，应为 200", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "markdown") ||
		!strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("主题范例应作为 Markdown 附件下载")
	}
	body := w.Body.String()
	for _, want := range []string{"设计变量", "--kokoro-", "theme.json", "layout", "tokensDark"} {
		if !strings.Contains(body, want) {
			t.Errorf("主题范例缺少 %q", want)
		}
	}
}

func TestThemePageLinksToExample(t *testing.T) {
	h, _ := newThemeTestHub(t)
	cookies := loginAsAdmin(t, h)
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("主题页状态码 = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `href="/theme-example.md"`) {
		t.Error("主题页缺少「下载主题范例」链接")
	}
}
