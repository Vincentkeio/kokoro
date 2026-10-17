package hub

// 后台「文章」功能的回归测试。
//
// 每台小鸡一篇文章：数据在 node_profile.content_md，详情页渲染 Markdown。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// postToAdmin 以管理员身份提交一个后台表单。
func postToAdmin(t *testing.T, h *Hub, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/admin/nodes",
		strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range loginAsAdmin(t, h) {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

// getWithCookies 用已有会话 GET 一个页面。
//
// ⚠️ 别在这里面再登录：boot 口令是**一次性**的，第二次登录会停在登录页（200），
// 断言会莫名其妙地挂。需要多次请求的测试要登录一次、全程复用 cookie。
func getWithCookies(h *Hub, path string, cookies []*http.Cookie) (int, string) {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// getAsAdmin 登录一次再 GET（只发一次请求的场景用它）。
func getAsAdmin(t *testing.T, h *Hub, path string) (int, string) {
	t.Helper()
	return getWithCookies(h, path, loginAsAdmin(t, h))
}

// TestAdminPostPage 编辑器页面能打开，并且带上了已有内容。
func TestAdminPostPage(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	// 先存一篇
	if err := st.SaveProfile(&model.NodeProfile{
		NodeID: n.ID, Summary: "旧摘要", ContentMD: "# 旧标题",
	}); err != nil {
		t.Fatal(err)
	}

	code, body := getAsAdmin(t, h, "/admin/post?node="+url.QueryEscape(n.ID))
	if code != http.StatusOK {
		t.Fatalf("编辑器返回 %d，应为 200", code)
	}
	for _, want := range []string{`name="content_md"`, `name="summary"`, "旧摘要", "# 旧标题"} {
		if !strings.Contains(body, want) {
			t.Errorf("编辑器缺少 %s", want)
		}
	}
	// 不该出现"新建第二篇"的入口——每台只有一篇
	if strings.Contains(body, "新建文章") {
		t.Error("不该有「新建文章」，每台小鸡只有一篇文章")
	}
}

// Article editing never edits, clears or exposes renewal metadata.
func TestAdminPostPreservesRenewalSettings(t *testing.T) {
	for _, expiry := range []string{"2027-03-01", "永久", "2027年3月"} {
		t.Run(expiry, func(t *testing.T) {
			h, st := newTestHub(t)
			n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
			if err := st.SaveProfile(&model.NodeProfile{NodeID: n.ID, Price: "99 CNY / 年", ExpireAt: expiry}); err != nil {
				t.Fatal(err)
			}
			_, body := getAsAdmin(t, h, "/admin/post?node="+url.QueryEscape(n.ID))
			for _, field := range []string{`name="price"`, `name="expire_at"`, `id="p-expire"`} {
				if strings.Contains(body, field) {
					t.Errorf("article editor exposes %s", field)
				}
			}
			// Even a stale or crafted article form cannot change node renewal fields.
			h.savePost(n, url.Values{"content_md": {"新的正文"}, "price": {"0"}, "expire_at": {""}})
			profile, err := st.GetProfile(n.ID)
			if err != nil {
				t.Fatal(err)
			}
			if profile.Price != "99 CNY / 年" || profile.ExpireAt != expiry {
				t.Fatalf("renewal metadata changed: price=%q expiry=%q", profile.Price, profile.ExpireAt)
			}
		})
	}
}

// TestAdminPostSaveAndRender 保存后详情页能渲染出 Markdown。
func TestAdminPostSaveAndRender(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	rec := postToAdmin(t, h, url.Values{
		"action":     {"save_profile"},
		"id":         {n.ID},
		"back":       {"post"},
		"summary":    {"一台东京的小鸡"},
		"content_md": {"## 配置\n\n- 2 核 4G\n- **CN2 GIA**\n"},
		"expire_at":  {"2027/03/01"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("保存返回 %d，应为 303", rec.Code)
	}
	// 要回到编辑器，而不是后台首页
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/admin/post?node=") {
		t.Errorf("保存后应回到编辑器，实际跳到 %q", loc)
	}

	prof, err := st.GetProfile(n.ID)
	if err != nil {
		t.Fatalf("读 profile 失败: %v", err)
	}
	if prof.Summary != "一台东京的小鸡" {
		t.Errorf("摘要 = %q", prof.Summary)
	}
	if prof.ExpireAt != "" {
		t.Errorf("文章保存不应修改到期日，实际为 %q", prof.ExpireAt)
	}
	if !strings.Contains(prof.ContentMD, "CN2 GIA") {
		t.Errorf("正文没存上: %q", prof.ContentMD)
	}

	// 正文要渲染成 HTML（标题变 <hN>、列表变 <ul>、加粗变 <strong>）。
	// ⚠️ 2026-10-11 起文章不再渲染在详情页 —— 它在**首页卡片的文章弹窗**里，
	//    数据走 /n/<slug>/articles.json。所以这里断言 JSON 接口的渲染结果。
	//
	// ⚠️ 断言前必须**解码 JSON**：Go 的 json 编码默认把 < > 转成 \u003c/\u003e
	//    （前端 JSON.parse 会自动还原，所以不影响功能，但字符串里搜 "<li>" 会搜不到）。
	js := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/n/"+n.Slug+"/articles.json", nil))
	var got struct {
		Articles []struct {
			Summary string `json:"summary"`
			HTML    string `json:"html"`
		} `json:"articles"`
	}
	if err := json.Unmarshal([]byte(js), &got); err != nil {
		t.Fatalf("articles.json 不是合法 JSON: %v", err)
	}
	if len(got.Articles) != 1 {
		t.Fatalf("应返回一篇文章，实际 %d", len(got.Articles))
	}
	html := got.Articles[0].HTML
	// 渲染器刻意把正文里的标题**降两级**（# -> h3），把 h1/h2 留给页面层级。
	// 所以这里只断言「标题文字被包进了某个 hN 标签」，不写死级别。
	if !strings.Contains(html, ">配置</h") {
		t.Error("articles.json 没把 Markdown 标题渲染成标题标签")
	}
	if !strings.Contains(html, "<li>") {
		t.Error("articles.json 没把 Markdown 列表渲染出来")
	}
	if !strings.Contains(html, "<strong>CN2 GIA</strong>") {
		t.Error("articles.json 没渲染加粗")
	}
	if got.Articles[0].Summary != "一台东京的小鸡" {
		t.Errorf("articles.json 摘要 = %q", got.Articles[0].Summary)
	}
}

// TestAdminPostSaveKeepsStats 保存文章不能把封面、浏览量、UV 清掉。
//
// SaveProfile 是**整体 upsert**，只塞表单里那几个字段就会把其余列写成零值。
// 这个坑很隐蔽：文章保存看起来"成功了"，但详情页的 PV/UV 从此永远是 0。
func TestAdminPostSaveKeepsStats(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	if err := st.SaveProfile(&model.NodeProfile{
		NodeID: n.ID, Cover: "https://example.com/c.png",
		PV: 1234, UV: 567, ContentMD: "旧正文",
	}); err != nil {
		t.Fatal(err)
	}

	postToAdmin(t, h, url.Values{
		"action": {"save_profile"}, "id": {n.ID}, "back": {"post"},
		"summary": {"新摘要"}, "content_md": {"新正文"},
	})

	prof, err := st.GetProfile(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prof.Cover != "https://example.com/c.png" {
		t.Errorf("封面被清掉了: %q", prof.Cover)
	}
	if prof.PV != 1234 || prof.UV != 567 {
		t.Errorf("浏览量/UV 被清掉了: pv=%d uv=%d", prof.PV, prof.UV)
	}
	if prof.ContentMD != "新正文" {
		t.Errorf("正文没更新: %q", prof.ContentMD)
	}
}

// TestAdminPostAccessControl 未登录不能看别人的文章编辑器。
func TestAdminPostAccessControl(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	req := httptest.NewRequest(http.MethodGet, "/admin/post?node="+url.QueryEscape(n.ID), nil)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, `name="content_md"`) {
		t.Error("未登录不该看到文章编辑器")
	}
}

// TestAdminPostBodyTruncated 超长正文要被截断，不能把 DB 和渲染拖垮。
func TestAdminPostBodyTruncated(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	huge := strings.Repeat("啊", maxPostRunes+5000)
	postToAdmin(t, h, url.Values{
		"action": {"save_profile"}, "id": {n.ID}, "content_md": {huge},
	})
	prof, err := st.GetProfile(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(prof.ContentMD)); got != maxPostRunes {
		t.Errorf("正文长度 = %d，应被截到 %d", got, maxPostRunes)
	}
}
