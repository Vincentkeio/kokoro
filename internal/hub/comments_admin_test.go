package hub

// 评论审核：按小鸡分组、置顶、已上墙的也能管。

import (
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// TestCommentsGroupedByNode 评论要按小鸡分组，且**顺序跟着节点列表**。
//
// 跟着评论出现顺序的话，每刷新一次分组的先后可能就变了 ——
// 站长的肌肉记忆会失效。
func TestCommentsGroupedByNode(t *testing.T) {
	h, st := newTestHub(t)
	a := mkNode(t, st, "A 机", "JP", "日本", "")
	b := mkNode(t, st, "B 机", "US", "美国", "")
	// 故意让 B 的评论先建 —— 分组顺序不该跟着它走
	if err := st.AddComment(&model.Comment{
		NodeID: b.ID, Author: "b", Content: "B 的评论", Status: model.CommentApproved}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddComment(&model.Comment{
		NodeID: a.ID, Author: "a", Content: "A 的评论", Status: model.CommentApproved}); err != nil {
		t.Fatal(err)
	}

	body := adminBody(t, h, st)
	ia, ib := strings.Index(body, "A 机"), strings.Index(body, "B 机")
	if ia < 0 || ib < 0 {
		t.Fatal("分组标题没渲染出来")
	}
	if ia > ib {
		t.Error("分组顺序应该跟着节点列表（A 在前），不该跟着评论出现顺序")
	}
	// 每台一块，各自带自己的评论
	if !strings.Contains(body, "A 的评论") || !strings.Contains(body, "B 的评论") {
		t.Error("评论内容没渲染出来")
	}
}

// TestPinnedCommentsComeFirst 置顶的评论要排在同节点的最前面。
func TestPinnedCommentsComeFirst(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")
	// ⚠️ 显式给不同的 created_at。同一毫秒插入时 created_at 相同，
	// 排序会退化成按 id（随机），测试会随机红。
	// 真实场景里评论都隔了秒级，但测试跑得太快了。
	base := int64(1700000000000)
	early := &model.Comment{NodeID: n.ID, Author: "早", Content: "先发的",
		Status: model.CommentApproved, CreatedAt: base}
	if err := st.AddComment(early); err != nil {
		t.Fatal(err)
	}
	if err := st.AddComment(&model.Comment{NodeID: n.ID, Author: "晚", Content: "后发的",
		Status: model.CommentApproved, CreatedAt: base + 1000}); err != nil {
		t.Fatal(err)
	}
	// 把早的那条置顶
	if err := st.PinComment(early.ID, true); err != nil {
		t.Fatal(err)
	}

	got, err := st.ListComments(n.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应有 2 条，实际 %d", len(got))
	}
	if !got[0].Pinned {
		t.Error("置顶的评论应该排在第一条")
	}
	if got[0].Content != "先发的" {
		t.Errorf("第一条应是置顶那条，实际 %q", got[0].Content)
	}

	// 取消置顶要能回到原顺序
	if err := st.PinComment(early.ID, false); err != nil {
		t.Fatal(err)
	}
	got, _ = st.ListComments(n.ID, true)
	if got[0].Pinned || got[0].Content != "先发的" {
		t.Error("取消置顶后应按时间排")
	}
}

// TestPinDoesNotChangeStatus 置顶/取消置顶**不能**动审核状态。
//
// 两个动作合在一起写的话，"取消置顶"会顺手把评论退回待审 ——
// 表现成"点了下取消置顶，评论从首页消失了"，非常难查。
func TestPinDoesNotChangeStatus(t *testing.T) {
	_, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")
	c := &model.Comment{NodeID: n.ID, Author: "a", Content: "内容",
		Status: model.CommentApproved}
	if err := st.AddComment(c); err != nil {
		t.Fatal(err)
	}
	for _, pin := range []bool{true, false} {
		if err := st.PinComment(c.ID, pin); err != nil {
			t.Fatal(err)
		}
		got, _ := st.ListComments(n.ID, true)
		if len(got) != 1 {
			t.Fatalf("置顶=%v 之后评论查不到了（状态被改了？）", pin)
		}
		if got[0].Status != model.CommentApproved {
			t.Errorf("置顶=%v 之后状态变成了 %q —— 置顶不该动审核状态",
				pin, got[0].Status)
		}
	}
}

// TestApprovedCommentCanBeManaged 已上墙的评论也要有管理入口。
//
// 以前只有「退回待审 / 垃圾 / 删除」，没法置顶。
func TestApprovedCommentCanBeManaged(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")
	if err := st.AddComment(&model.Comment{NodeID: n.ID, Author: "a", Content: "已上墙的",
		Status: model.CommentApproved}); err != nil {
		t.Fatal(err)
	}
	body := adminBody(t, h, st)
	for _, want := range []string{"置顶", "下墙", "删除"} {
		if !strings.Contains(body, want) {
			t.Errorf("已上墙的评论缺少「%s」操作", want)
		}
	}
	// 上墙的不该再出现「通过」（它已经通过了）
	if strings.Contains(body, `value="approve"`) {
		t.Error("已上墙的评论不该还显示「通过」按钮")
	}
}
