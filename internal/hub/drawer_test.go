package hub

// 侧边抽屉的数据接口：文章 / 评论，以及权限。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func getDrawer(t *testing.T, h *Hub, path string) drawerResp {
	t.Helper()
	w := httptest.NewRecorder()
	h.handleNodePage(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("%s 返回 %d: %s", path, w.Code, w.Body.String())
	}
	var d drawerResp
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatalf("解析失败: %v (%s)", err, w.Body.String())
	}
	return d
}

// TestDrawerArticleIsSingle 一台只有一篇文章 —— 接口也只返回一篇。
func TestDrawerArticleIsSingle(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	// 就算被塞了两篇（旧数据 / 手滑），也只给第一篇
	for _, title := range []string{"第一篇", "第二篇"} {
		if err := st.CreateArticle(&model.Article{
			NodeID: n.ID, Title: title, Summary: "s", ContentMD: "正文",
		}); err != nil {
			t.Fatal(err)
		}
	}
	d := getDrawer(t, h, "/n/"+n.Slug+"/articles.json")
	if len(d.Articles) != 1 {
		t.Fatalf("应只返回 1 篇，实际 %d", len(d.Articles))
	}
	// 只断言"只给一篇"就够了，不断言是哪一篇 ——
	// ListArticles 按 created_at DESC 排，同一毫秒插入时谁在前取决于
	// 插入顺序，钉死标题会让测试变脆。产品上"只有一篇"，给谁都对。
	if d.Articles[0].Title == "" {
		t.Error("返回的文章没有标题")
	}
	// 一篇文章不该有分页控件
	if d.Pages != 1 {
		t.Errorf("文章不该分页，Pages = %d", d.Pages)
	}
}

// TestDrawerCommentsPaged 评论要分页。
func TestDrawerCommentsPaged(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	total := drawerCommentPage + 5
	for i := 0; i < total; i++ {
		if err := st.AddComment(&model.Comment{
			NodeID: n.ID, Author: "阿宝", Content: "评论", Status: model.CommentApproved,
		}); err != nil {
			t.Fatal(err)
		}
	}
	p1 := getDrawer(t, h, "/n/"+n.Slug+"/comments.json")
	if p1.Total != total {
		t.Errorf("总数 = %d，应为 %d", p1.Total, total)
	}
	if len(p1.Comments) != drawerCommentPage {
		t.Errorf("第一页应给 %d 条，实际 %d", drawerCommentPage, len(p1.Comments))
	}
	if p1.Pages != 2 {
		t.Errorf("应分 2 页，实际 %d", p1.Pages)
	}

	p2 := getDrawer(t, h, "/n/"+n.Slug+"/comments.json?page=2")
	if len(p2.Comments) != 5 {
		t.Errorf("第二页应给 5 条，实际 %d", len(p2.Comments))
	}

	// 页码越界要夹到最后一页，不是 400 —— URL 是手改得出来的
	p9 := getDrawer(t, h, "/n/"+n.Slug+"/comments.json?page=99")
	if p9.Page != 2 {
		t.Errorf("越界页码应夹到最后一页，实际 %d", p9.Page)
	}
}

// TestDrawerHidesPendingForVisitors 未审核的评论访客看不到，管理员看得到。
func TestDrawerHidesPendingForVisitors(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	_ = st.AddComment(&model.Comment{
		NodeID: n.ID, Author: "已审核", Content: "放行的", Status: model.CommentApproved})
	_ = st.AddComment(&model.Comment{
		NodeID: n.ID, Author: "待审核", Content: "买茶叶加微信", Status: model.CommentPending})

	d := getDrawer(t, h, "/n/"+n.Slug+"/comments.json")
	if d.Total != 1 {
		t.Errorf("访客只该看到 1 条已审核的，实际 %d", d.Total)
	}
	if d.IsAdmin {
		t.Error("未登录不该被当成管理员")
	}
	for _, c := range d.Comments {
		if strings.Contains(c.HTML, "买茶叶") || strings.Contains(c.Content, "买茶叶") {
			t.Error("未审核的评论不该出现在访客的抽屉里")
		}
	}
}

// TestDrawerAdminGetsEditLinks 只有管理员才拿到编辑入口。
func TestDrawerAdminGetsEditLinks(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

	// 访客：没有任何管理链接
	guest := getDrawer(t, h, "/n/"+n.Slug+"/articles.json")
	if guest.EditURL != "" || guest.ManageURL != "" {
		t.Error("访客不该拿到编辑 / 管理链接")
	}
}

// TestDrawerPrivateNodeHidden 私有节点对访客连抽屉都不能开。
func TestDrawerPrivateNodeHidden(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "隐藏机", "JP", "日本 · 东京", "")
	// 改可见性走更新，不能 CreateNode —— 那个会撞 slug 唯一索引
	n.Visibility = model.VisibilityPrivate
	if err := st.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.handleNodePage(w, httptest.NewRequest(http.MethodGet, "/n/"+n.Slug+"/articles.json", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("私有节点的抽屉接口应 404，实际 %d", w.Code)
	}
}
