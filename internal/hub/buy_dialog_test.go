package hub

// 「我想买它」弹窗必须和 #dlg-scrim **平级**，不能嵌在那个遮罩里面。
//
// 2026-10-11 boss 报"点了没效果"。根因不在 JS：买它弹窗被嵌进了
// #dlg-scrim（文章/评论共用的遮罩，默认 hidden）内部，于是 JS 里
// `scrim.hidden = false` 确实执行了、DOM 属性也变了，**但父元素还藏着** ——
// 整棵子树都不渲染，表现就是"点了没反应"，而且**一个错都不报**。
//
// 这类"嵌套错了"的问题没有报错、也没有视觉线索（截图上看就是页面没变化），
// 只能靠结构断言钉住。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuyDialogNotNestedInSharedScrim(t *testing.T) {
	h, st := newTestHub(t)
	mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))

	// ⚠️ 两头都要**含 `<div` 本身**，否则配平算不准：
	// 拿 `id="buy-scrim"` 当终点的话，买它弹窗自己的 `<div` 落在区间内、
	// 它的 `</div>` 落在区间外，永远差 1 —— 第一版就是这么误报的。
	i := strings.Index(body, `<div class="dlg-scrim" id="dlg-scrim"`)
	j := strings.Index(body, `<div class="dlg-scrim" id="buy-scrim"`)
	if i < 0 {
		t.Fatal("首页找不到 #dlg-scrim 遮罩")
	}
	if j < 0 {
		t.Fatal("首页找不到 #buy-scrim 弹窗")
	}
	if j < i {
		t.Fatal("#buy-scrim 出现在 #dlg-scrim 之前，结构不对")
	}

	// 数这段里 <div 与 </div> 的个数：
	//   - 买它弹窗在遮罩**外面**时，这一段正好是 #dlg-scrim 的完整块，
	//     开合配平（i 处开一个、末尾关一个，内部自平衡）；
	//   - 被嵌进去时，#dlg-scrim 还没闭合，开比关多 1。
	seg := body[i:j]
	opens := strings.Count(seg, "<div")
	closes := strings.Count(seg, "</div>")
	if opens != closes {
		t.Errorf("买它弹窗被嵌在 #dlg-scrim 里面了（这段有 %d 个 <div、%d 个 </div>）："+
			"父遮罩默认 hidden，弹窗永远显示不出来 —— 点击看着像没反应，且不报错", opens, closes)
	}

	// 顺带确认它自带遮罩（不然移出去之后没有居中和灰底）。
	if !strings.Contains(body, `<div class="dlg-scrim" id="buy-scrim"`) {
		t.Error("#buy-scrim 必须自己带 .dlg-scrim，否则移出共用遮罩后不会居中")
	}
}
