package hub

// 文章在**首页卡片的弹窗**里看（2026-10-11 boss 定），详情页不再有文章区。
// 数据走 /n/<slug>/articles.json 按需拉，不预先塞进首页 ——
// 一篇几千字、五篇就是几万字，绝大多数访客根本不点开。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// TestArticleDialogAndJSON 卡片弹窗 + JSON 接口这条链路要通：
//  1. 首页有文章弹窗，按需拉数据；
//  2. articles.json 返回标题与**全文 HTML**（弹窗要显示全文）；
//  3. 首页 HTML 里**不能内联**全文（否则首页体积翻好几倍）。
func TestArticleDialogAndJSON(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	long := strings.Repeat("这是正文里很长很长的一段话。", 50)
	if err := st.CreateArticle(&model.Article{
		NodeID: n.ID, Title: "上手体验", Summary: "一句话摘要",
		ContentMD: long,
	}); err != nil {
		t.Fatal(err)
	}

	// 1) 首页必须有文章弹窗
	home := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(home, `id="d-art"`) {
		t.Error("首页缺少文章弹窗")
	}
	if strings.Contains(home, "这是正文里很长很长的一段话") {
		t.Error("首页不该内联文章全文 —— 那是点开才拉的")
	}

	// 2) JSON 接口要给出标题 + 摘要 + 渲染好的全文
	js := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/n/"+n.Slug+"/articles.json", nil))
	for _, want := range []string{"上手体验", "一句话摘要", "这是正文里很长很长的一段话"} {
		if !strings.Contains(js, want) {
			t.Errorf("articles.json 缺少 %q", want)
		}
	}
}

func TestNodeArticleActionsOpenSharedDialog(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "节点文章测试", "JP", "日本", "")
	bodyText := strings.Repeat("这是只允许在弹窗中读取的正文。", 50)
	if err := st.SaveArticle(&model.Article{NodeID: n.ID, Title: "使用记录", Summary: "配置与使用体验", ContentMD: bodyText}); err != nil {
		t.Fatal(err)
	}
	page := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/n/"+n.Slug, nil))
	for _, want := range []string{`class="node-content-actions"`, `data-dlg="` + n.Slug + `"`, `aria-controls="d-art"`, `id="d-art"`, "1 篇节点文章"} {
		if !strings.Contains(page, want) {
			t.Errorf("detail page missing %q", want)
		}
	}
	if strings.Contains(page, bodyText) {
		t.Error("full article should be fetched only after opening its dialog")
	}
}

// TestSaveArticleKeepsOnePerNode 产品规范是**一台鸡一篇文章**：
// 再存一次是同篇覆盖，不是新增。数据层由 SaveArticle 的 upsert 保证。
func TestSaveArticleKeepsOnePerNode(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	if err := st.SaveArticle(&model.Article{
		NodeID: n.ID, Title: "上手体验", Summary: "一句话摘要", ContentMD: "第一版正文",
	}); err != nil {
		t.Fatal(err)
	}
	// 再存一次：应该覆盖同一篇，而不是多出一篇
	if err := st.SaveArticle(&model.Article{
		NodeID: n.ID, Title: "上手体验（改）", Summary: "改过的摘要", ContentMD: "第二版正文",
	}); err != nil {
		t.Fatal(err)
	}
	arts, err := st.ListArticles(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 {
		t.Fatalf("一台鸡只能有一篇，实际 %d 篇", len(arts))
	}
	if arts[0].Title != "上手体验（改）" || arts[0].ContentMD != "第二版正文" {
		t.Errorf("第二次保存应覆盖第一篇，实际 %+v", arts[0])
	}

	// 弹窗只给这一篇
	js := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/n/"+n.Slug+"/articles.json", nil))
	if !strings.Contains(js, "上手体验（改）") || !strings.Contains(js, "第二版正文") {
		t.Error("articles.json 应给出这一篇的最新内容")
	}
}

// TestArticleAutoSummary 没填摘要时从正文自动截一段。
//
// 列表只显示摘要，要求每篇都手填太麻烦。
func TestArticleAutoSummary(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	a := &model.Article{
		NodeID: n.ID, Title: "无摘要",
		ContentMD: "## 标题啊\n\n**这是**第一段正文，包含 `代码` 记号。\n\n第二段。",
	}
	if err := st.CreateArticle(a); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetArticle(a.ID)
	if got.Summary == "" {
		t.Fatal("应自动生成摘要")
	}
	for _, bad := range []string{"##", "**", "`"} {
		if strings.Contains(got.Summary, bad) {
			t.Errorf("摘要里不该残留 Markdown 记号 %q：%s", bad, got.Summary)
		}
	}
	if !strings.Contains(got.Summary, "第一段正文") {
		t.Errorf("摘要应取自正文：%s", got.Summary)
	}
}

// TestArticleEditAndDelete 改和删都要能用。
func TestArticleEditAndDelete(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	a := &model.Article{NodeID: n.ID, Title: "旧标题", Summary: "旧摘要", ContentMD: "旧正文"}
	if err := st.CreateArticle(a); err != nil {
		t.Fatal(err)
	}
	a.Title, a.Summary, a.ContentMD = "新标题", "新摘要", "新正文"
	if err := st.UpdateArticle(a); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetArticle(a.ID)
	if got.Title != "新标题" || got.ContentMD != "新正文" {
		t.Errorf("修改没生效: %+v", got)
	}
	if err := st.DeleteArticle(a.ID); err != nil {
		t.Fatal(err)
	}
	gone, _ := st.GetArticle(a.ID)
	if gone != nil {
		t.Error("删除后不该还能查到")
	}
}
