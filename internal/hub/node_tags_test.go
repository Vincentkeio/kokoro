package hub

// 节点自定义标签的回归测试。
//
// 标签是站长的自由文本输入，所以这里守的两件事：
//   1. 别把「中文逗号分隔的一串」当成一个标签（最常见的粘贴事故）；
//   2. 别让脏输入把卡片撑爆（数量、长度都要有上限）。

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/kokoro-probe/kokoro/internal/model"
)

func TestParseTags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"英文逗号", "香港,CN2,原生IP", []string{"香港", "CN2", "原生IP"}},
		{"中文逗号", "香港，CN2，原生IP", []string{"香港", "CN2", "原生IP"}},
		{"顿号", "香港、CN2、原生IP", []string{"香港", "CN2", "原生IP"}},
		{"空格", "香港 CN2 原生IP", []string{"香港", "CN2", "原生IP"}},
		{"混合分隔", " 香港 ,CN2、 原生IP;支持退款 ", []string{"香港", "CN2", "原生IP", "支持退款"}},
		{"去重（大小写不敏感）", "vps,VPS,Vps", []string{"vps"}},
		{"剥掉井号", "#香港 #CN2", []string{"香港", "CN2"}},
		{"空串", "", nil},
		{"只有分隔符", " , ， 、 ", nil},
	}
	for _, c := range cases {
		got := parseTags(c.in, 8, 16)
		if len(got) != len(c.want) {
			t.Errorf("%s: parseTags(%q) = %v，应为 %v", c.name, c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: parseTags(%q) = %v，应为 %v", c.name, c.in, got, c.want)
				break
			}
		}
	}
}

// TestParseTagsLimits 确认上限真的生效——脏输入不能把卡片撑爆。
func TestParseTagsLimits(t *testing.T) {
	// 数量上限
	many := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		many = append(many, "tag"+string(rune('a'+i)))
	}
	if got := parseTags(strings.Join(many, ","), 8, 16); len(got) != 8 {
		t.Errorf("数量应被截到 8，实际 %d", len(got))
	}
	// 长度上限（按字符数，不能把中文截坏）
	got := parseTags("这是一个非常非常非常长的标签名字超了", 8, 6)
	if len(got) != 1 {
		t.Fatalf("应有 1 个标签，实际 %v", got)
	}
	if n := len([]rune(got[0])); n != 6 {
		t.Errorf("应按字符截到 6，实际 %d（%q）", n, got[0])
	}
	if !strings.HasPrefix("这是一个非常非常非常长的标签名字超了", got[0]) {
		t.Errorf("截断结果 %q 不是原串前缀", got[0])
	}
}

// TestAdminNodeTagsSaved 端到端：后台表单能存下标签，首页卡片能显示出来。
func TestAdminNodeTagsSaved(t *testing.T) {
	h, st := newTestHub(t)
	node := &model.Node{Name: "东京 zouter", Visibility: model.VisibilityPublic}
	if err := st.CreateNode(node); err != nil {
		t.Fatalf("造节点失败: %v", err)
	}

	cookies := loginAsAdmin(t, h)
	form := url.Values{
		"action": {"rename"},
		"id":     {node.ID},
		"name":   {node.Name},
		"tags":   {"香港，CN2、原生IP vps"},
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/nodes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("保存状态码 = %d，应为 303", rec.Code)
	}

	saved, err := st.GetNode(node.ID)
	if err != nil {
		t.Fatalf("读回节点失败: %v", err)
	}
	want := []string{"香港", "CN2", "原生IP", "vps"}
	if len(saved.Tags) != len(want) {
		t.Fatalf("存下的标签 = %v，应为 %v", saved.Tags, want)
	}
	for i := range want {
		if saved.Tags[i] != want[i] {
			t.Fatalf("存下的标签 = %v，应为 %v", saved.Tags, want)
		}
	}

	// 首页卡片要真的渲染出来
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, w := range want {
		if !strings.Contains(body, ">"+w+"<") {
			t.Errorf("首页卡片里没出现标签 %q", w)
		}
	}
}

// TestAdminRenameWithoutTagsKeepsThem 只改名字时不能顺手把标签清空。
//
// 这个坑很隐蔽：rename 分支里如果用 r.FormValue("tags") 无条件赋值，
// 那么任何不带 tags 字段的 rename 提交都会把标签抹掉。
func TestAdminRenameWithoutTagsKeepsThem(t *testing.T) {
	h, st := newTestHub(t)
	node := &model.Node{
		Name: "东京 zouter", Visibility: model.VisibilityPublic,
		Tags: []string{"香港", "CN2"},
	}
	if err := st.CreateNode(node); err != nil {
		t.Fatalf("造节点失败: %v", err)
	}

	cookies := loginAsAdmin(t, h)
	// 故意不带 tags 字段
	form := url.Values{"action": {"rename"}, "id": {node.ID}, "name": {"东京 zouter 2"}}
	req := httptest.NewRequest(http.MethodPost, "/admin/nodes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	h.mux.ServeHTTP(httptest.NewRecorder(), req)

	saved, err := st.GetNode(node.ID)
	if err != nil {
		t.Fatalf("读回节点失败: %v", err)
	}
	if len(saved.Tags) != 2 {
		t.Errorf("只改名不该动标签，实际 %v", saved.Tags)
	}
	if saved.Name != "东京 zouter 2" {
		t.Errorf("名字应已更新，实际 %q", saved.Name)
	}
}
