package hub

// 站长名片（用户名 / 头像 / 个人签名）的回归测试。
//
// 这条链路横跨「后台表单 → settings → 首页与详情页模板」，
// 任何一段断了，后台看着都是"保存成功"，但前台什么都没有。
// 所以这里既测 owner() 的判定语义，也真的渲染页面断言内容。

import (
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kokoro-probe/kokoro/internal/model"
	"github.com/kokoro-probe/kokoro/internal/theme"
)

// TestOwnerCardHasSemantics 钉住「填过就显示」这条判定。
//
// 曾经用的是 `name != cfg.SiteName`，于是把昵称填成和站点同名时
// 名片会突然消失——明明填了却看不到。
func TestOwnerCardHasSemantics(t *testing.T) {
	h, st := newTestHub(t)

	// 什么都没填：不显示（免得首页顶着一块只有站点名的空名片）
	if o := h.owner(); o.Has {
		t.Error("三项全空时不该显示站长名片")
	}

	// 只填昵称，且**和站点同名**——这正是旧逻辑会误判的情形
	if err := st.SetSetting("owner_name", h.cfg.SiteName); err != nil {
		t.Fatalf("写设置失败: %v", err)
	}
	if o := h.owner(); !o.Has {
		t.Error("昵称填了就该显示，哪怕和站点同名")
	}

	// 只填签名
	if err := st.SetSetting("owner_name", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("owner_bio", "只卖靠谱的小鸡"); err != nil {
		t.Fatal(err)
	}
	if o := h.owner(); !o.Has || o.Bio != "只卖靠谱的小鸡" {
		t.Errorf("只填签名也应显示: has=%v bio=%q", o.Has, o.Bio)
	}

	// 昵称留空时回退到站点名，但 Has 由"填没填"决定
	if o := h.owner(); o.Name != h.cfg.SiteName {
		t.Errorf("昵称留空应回退站点名，实际 %q", o.Name)
	}
}

// TestOwnerAvatarRejectsScriptScheme 头像字段只接受站内路径或 http(s)。
//
// 这个值会被直接塞进 <img src>，放任 javascript: / data: 进来
// 就是一个 XSS 面。
func TestOwnerAvatarRejectsScriptScheme(t *testing.T) {
	h, st := newTestHub(t)
	for _, bad := range []string{
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"vbscript:msgbox(1)",
		"//evil.com/x.png", // 协议相对，等于外站
	} {
		_ = st.SetSetting("owner_avatar", bad)
		if o := h.owner(); o.Avatar != "" || o.Has {
			t.Errorf("头像 %q 应被拒绝，实际 avatar=%q has=%v", bad, o.Avatar, o.Has)
		}
	}
	// 正常值要放行
	for _, good := range []string{"/avatar?v=1", "https://cdn.example.com/a.png", "http://x/y.png"} {
		_ = st.SetSetting("owner_avatar", good)
		if o := h.owner(); o.Avatar != good {
			t.Errorf("头像 %q 应放行，实际 %q", good, o.Avatar)
		}
	}
}

// TestSkinPickerOnPublicPages 皮肤切换器在所有访客可达的公开页都必须出现。
//
// 这个 bug 真的发生过：切换器原本挂在页尾的 `{{template "themepicker.html" .}}`，
// 改到页头时首页走了共享的 topbar.html，而**详情页是自己手写的 header**，
// 于是详情页整页没有切换入口。两个页面用了两套 header 是根因，
// 在统一之前，这条测试至少能保证漏掉一边时立刻红。
func TestSkinPickerOnPublicPages(t *testing.T) {
	h, st := newTestHub(t)
	node := &model.Node{Name: "东京 zouter", Visibility: model.VisibilityPublic}
	if err := st.CreateNode(node); err != nil {
		t.Fatalf("造节点失败: %v", err)
	}

	cases := []struct{ name, path string }{
		{"首页", "/"},
		{"详情页", "/n/" + node.Slug},
		{"仪表盘", "/dashboard"},
	}
	for _, c := range cases {
		body := renderBody(t, h, httptest.NewRequest(http.MethodGet, c.path, nil))
		if !strings.Contains(body, "data-skin-picker") {
			t.Errorf("%s（%s）缺少皮肤切换器", c.name, c.path)
			continue
		}
		if !strings.Contains(body, "/pick/"+theme.DefaultID) {
			t.Errorf("%s 的切换器缺少访客入口 /pick/<id>", c.name)
		}
		// 必须在页头内，不能又跑回页尾
		end := strings.Index(body, "</header>")
		at := strings.Index(body, "data-skin-picker")
		if end < 0 || at > end {
			t.Errorf("%s 的切换器不在页头内", c.name)
		}
	}
}

// TestOwnerCardRenderedOnHomeAndNode 两端都要真的渲染出来。
//
// 后台表单上写着"会显示在首页顶部与每台小鸡的页面底部"，
// 但详情页此前根本没渲染——这条就是来守那句承诺的。
func TestOwnerCardRenderedOnHomeAndNode(t *testing.T) {
	h, st := newTestHub(t)
	if err := st.SetSetting("owner_name", "阿宝"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("owner_bio", "只卖靠谱的小鸡"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("owner_avatar", "/avatar?v=42"); err != nil {
		t.Fatal(err)
	}

	// 首页
	home := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, want := range []string{"阿宝", "只卖靠谱的小鸡", `class="avatar"`, "/avatar?v=42"} {
		if !strings.Contains(home, want) {
			t.Errorf("首页缺少 %q", want)
		}
	}

	// 详情页：先造一台小鸡
	node := &model.Node{Name: "东京 zouter", Visibility: model.VisibilityPublic}
	if err := st.CreateNode(node); err != nil {
		t.Fatalf("造节点失败: %v", err)
	}
	page := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/n/"+node.Slug, nil))
	for _, want := range []string{"阿宝", "只卖靠谱的小鸡", `class="avatar"`, "/avatar?v=42"} {
		if !strings.Contains(page, want) {
			t.Errorf("详情页缺少 %q（后台提示承诺了这里也显示）", want)
		}
	}
	if !strings.Contains(page, `class="panel owner"`) {
		t.Error(`详情页缺少 .panel.owner 结构`)
	}
}

// TestOwnerProfileSaveReachesHomepage 跑通「后台表单 → settings → 首页」这条完整链路。
//
// 这条链路断在哪一段，表现都是"后台看着保存了，首页什么都没有"，
// 所以必须端到端走一遍，而不是只测 owner() 的判定。
func TestOwnerProfileSaveReachesHomepage(t *testing.T) {
	h, st := newTestHub(t)

	// 模拟后台那份 multipart 表单
	var buf strings.Builder
	mw := multipart.NewWriter(&buf)
	for k, v := range map[string]string{
		"owner_name": "小宝",
		"owner_bio":  "一阵有脑子的小旋风",
	} {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	cookies := loginAsAdmin(t, h) // 只登录这一次，后面复用
	req := httptest.NewRequest(http.MethodPost, "/admin/profile", strings.NewReader(buf.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("保存返回 %d，应为 303；body=%s", rec.Code, rec.Body.String()[:min(200, rec.Body.Len())])
	}

	// 落库
	if v, _ := st.GetSetting("owner_name"); v != "小宝" {
		t.Errorf("owner_name = %q，应为「小宝」", v)
	}
	if v, _ := st.GetSetting("owner_bio"); v != "一阵有脑子的小旋风" {
		t.Errorf("owner_bio = %q", v)
	}

	// 首页必须真的显示出来
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, want := range []string{"小宝", "一阵有脑子的小旋风", `class="panel owner"`} {
		if !strings.Contains(body, want) {
			t.Errorf("首页缺少 %s", want)
		}
	}

	// 后台表单要能回显（下次进来看到的是已保存的值）
	_, adm := getWithCookies(h, "/admin", cookies)
	if !strings.Contains(adm, `value="小宝"`) {
		t.Error("后台表单没有回显已保存的昵称")
	}
}

// TestOwnerEmptyHintOnlyForAdmin 名片没填时，只有管理员自己看得到引导；
// 访客看到的是一个干净页面，不该出现"这里缺东西"。
func TestOwnerEmptyHintOnlyForAdmin(t *testing.T) {
	h, _ := newTestHub(t)

	// 访客
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(body, "owner-empty") {
		t.Error("访客不该看到「还没填站长名片」的引导")
	}

	// 管理员
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range loginAsAdmin(t, h) {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	adminBody := rec.Body.String()
	if !strings.Contains(adminBody, "owner-empty") {
		t.Error("管理员自己看首页时，应该提示还没填名片")
	}
	if !strings.Contains(adminBody, "/admin#profile") {
		t.Error("引导里应该带上后台入口")
	}

	// 填了之后，引导消失、名片出现，且对访客也可见
	if err := h.store.SetSetting("owner_name", "小宝"); err != nil {
		t.Fatal(err)
	}
	guest := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(guest, "owner-empty") {
		t.Error("填了名片之后不该再显示引导")
	}
	if !strings.Contains(guest, `class="panel owner"`) || !strings.Contains(guest, "小宝") {
		t.Error("填了名片之后，访客应该能在首页看到")
	}
}
