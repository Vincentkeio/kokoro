package store

// 「一台一篇」必须在**数据层**成立，不靠调用方自觉。

import (
	"path/filepath"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func openTmp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestSaveArticleUpserts 反复 SaveArticle 只会有一篇。
func TestSaveArticleUpserts(t *testing.T) {
	s := openTmp(t)
	n := &model.Node{ID: "nd_x", Name: "x", Visibility: "public"}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"一", "二", "三"} {
		if err := s.SaveArticle(&model.Article{
			NodeID: n.ID, Title: title, ContentMD: "正文" + title,
		}); err != nil {
			t.Fatal(err)
		}
	}
	arts, err := s.ListArticles(n.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(arts) != 1 {
		t.Fatalf("SaveArticle 存了 3 次，应该有且只有 1 篇，实际 %d 篇", len(arts))
	}
	if arts[0].Title != "三" {
		t.Errorf("应该是最新那次的内容，实际 %q", arts[0].Title)
	}
}

// TestSaveArticleKeepsCreatedAt 改文章不该把它变成"新写的"。
//
// created_at 是"这篇什么时候建的"，改一次就刷新的话，
// 排序和"写了多久"这类判断全乱。
func TestSaveArticleKeepsCreatedAt(t *testing.T) {
	s := openTmp(t)
	n := &model.Node{ID: "nd_x", Name: "x", Visibility: "public"}
	if err := s.CreateNode(n); err != nil {
		t.Fatal(err)
	}
	a := &model.Article{NodeID: n.ID, Title: "一", ContentMD: "正文"}
	if err := s.SaveArticle(a); err != nil {
		t.Fatal(err)
	}
	first, _ := s.GetArticle(a.ID)
	if first == nil {
		t.Fatal("第一篇没存上")
	}

	// 再存一次（模拟"编辑"）
	if err := s.SaveArticle(&model.Article{
		NodeID: n.ID, Title: "改过", ContentMD: "新正文"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetArticle(first.ID)
	if got == nil {
		t.Fatal("编辑之后文章 ID 变了 —— 应该是改而不是新建")
	}
	if got.CreatedAt != first.CreatedAt {
		t.Errorf("created_at 被刷新了：%d -> %d", first.CreatedAt, got.CreatedAt)
	}
	if got.Title != "改过" {
		t.Errorf("标题没更新：%q", got.Title)
	}
}

// TestSaveArticleRejectsNoNode 缺 node_id 要报错，不能建出一篇孤儿文章。
func TestSaveArticleRejectsNoNode(t *testing.T) {
	s := openTmp(t)
	if err := s.SaveArticle(&model.Article{Title: "没主"}); err == nil {
		t.Error("缺 node_id 应该报错")
	}
}
