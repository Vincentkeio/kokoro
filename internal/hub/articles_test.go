package hub

// 详情页文章区：列表只显示标题+摘要，全文在弹窗里，最多 5 条高。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// TestArticlesListShowsTitleAndSummaryOnly 列表里只有标题和摘要，不能有全文。
//
// 理由：一篇几千字、五篇就是几万字 —— 全铺在页面上，详情页会又长又难滚。
func TestArticlesListShowsTitleAndSummaryOnly(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

	long := strings.Repeat("这是正文里很长很长的一段话。", 50)
	if err := st.CreateArticle(&model.Article{
		NodeID: n.ID, Title: "上手体验", Summary: "一句话摘要",
		ContentMD: long,
	}); err != nil {
		t.Fatal(err)
	}

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/n/"+n.Slug, nil))
	if !strings.Contains(body, "上手体验") {
		t.Error("列表里应有标题")
	}
	if !strings.Contains(body, "一句话摘要") {
		t.Error("列表里应有摘要")
	}
	// 列表区（<ul class="art-list"> 到 </ul>）里不能出现正文
	i := strings.Index(body, `class="art-list"`)
	j := strings.Index(body[i:], "</ul>")
	if i < 0 || j < 0 {
		t.Fatal("没找到文章列表")
	}
	listHTML := body[i : i+j]
	if strings.Contains(listHTML, "这是正文里很长很长的一段话") {
		t.Error("列表里不该出现文章全文 —— 全文只在弹窗里")
	}
	// 但全文必须真的在页面上（在 <dialog> 里），否则点开是空的
	if !strings.Contains(body, "这是正文里很长很长的一段话") {
		t.Error("弹窗里应该有全文")
	}
	if !strings.Contains(body, "<dialog") {
		t.Error("应该有弹窗")
	}
}

// TestArticlesMultiplePerNode 一台机器可以有多篇（"一台一篇"的限制已去掉）。
func TestArticlesMultiplePerNode(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	for _, title := range []string{"第一篇", "第二篇", "第三篇"} {
		if err := st.CreateArticle(&model.Article{
			NodeID: n.ID, Title: title, Summary: title + "的摘要", ContentMD: "正文",
		}); err != nil {
			t.Fatal(err)
		}
	}
	arts, err := st.ListArticles(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 3 {
		t.Fatalf("应有 3 篇，实际 %d", len(arts))
	}
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/n/"+n.Slug, nil))
	for _, title := range []string{"第一篇", "第二篇", "第三篇"} {
		if !strings.Contains(body, title) {
			t.Errorf("列表里缺少 %q", title)
		}
	}
	if !strings.Contains(body, "3 篇") {
		t.Error("标题旁应显示篇数")
	}
}

// TestArticleAutoSummary 没填摘要时从正文自动截一段。
//
// 列表只显示摘要，要求每篇都手填太麻烦。
func TestArticleAutoSummary(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
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
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
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
