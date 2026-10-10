package hub

// HTML 必须每次回源，不要进浏览器的"启发式缓存"。
//
// 2026-10-10 踩过这个坑：地球的修复部署上去了、static 的 ?v=N 也 bump 了，
// 但 boss 刷新后看到的还是老样子 —— 因为 HTML 没有 Cache-Control，
// 浏览器自行做了启发式缓存，返回的还是**旧 HTML**，里面引用的自然是
// 旧版 CSS/JS。从服务端 curl 一切正常，完全看不出问题。
//
// 这类"改了却没变化"最难排查（会让人去怀疑代码本身），所以钉住：
// 任何走 render 的页面都必须带 no-cache / no-store。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTMLNotHeuristicallyCached(t *testing.T) {
	h, _ := newThemeTestHub(t)

	// 首页、登录页、后台页 —— 都走同一条 render 出口。
	for _, path := range []string{"/", "/admin", "/admin/themes"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)

		cc := w.Header().Get("Cache-Control")
		if cc == "" {
			t.Errorf("%s 缺 Cache-Control —— 浏览器会启发式缓存 HTML，"+
				"部署后用户刷新仍看到旧页面、引用旧版 CSS/JS", path)
			continue
		}
		if !strings.Contains(cc, "no-cache") && !strings.Contains(cc, "no-store") {
			t.Errorf("%s 的 Cache-Control = %q，应为 no-cache 或 no-store", path, cc)
		}
	}
}
