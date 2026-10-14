package hub

// 「我想买它」：公开接口，落库 + 推 TG。

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/store"
)

func postBuy(t *testing.T, h *Hub, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/buy", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.handleBuy(w, r)
	return w
}

func TestBuySavesIntent(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")

	w := postBuy(t, h, url.Values{"slug": {n.Slug}, "contact": {"wx: abc"}})
	if w.Code != http.StatusOK {
		t.Fatalf("应成功，实际 %d: %s", w.Code, w.Body.String())
	}
	// 必须真的落库了 —— 这是"先落库再推送"的前提
	got, err := st.ListBuyIntents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("库里应有 1 条，实际 %d", len(got))
	}
	if got[0].Contact != "wx: abc" || got[0].NodeID != n.ID {
		t.Errorf("存的内容不对: %+v", got[0])
	}
	_ = store.BuyIntent{}
	_ = model.Node{}
}

func TestBuyRejectsEmptyContact(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")
	if w := postBuy(t, h, url.Values{"slug": {n.Slug}, "contact": {"  "}}); w.Code == http.StatusOK {
		t.Fatal("没填联系方式却成功了")
	}
	got, _ := st.ListBuyIntents(10)
	if len(got) != 0 {
		t.Error("失败的单子不该进库")
	}
}

// TestBuyRateLimits 限流：不然一个人能把站长的 TG 刷爆。
func TestBuyRateLimits(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")
	var last int
	for i := 0; i < buyMaxPerHour+2; i++ {
		last = postBuy(t, h, url.Values{"slug": {n.Slug}, "contact": {"x"}}).Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("第 %d 次应被限流（429），实际 %d", buyMaxPerHour+2, last)
	}
}

func TestBuyRejectsPrivateNode(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "隐藏", "JP", "日本", "")
	n.Visibility = model.VisibilityPrivate
	if err := st.UpdateNode(n); err != nil {
		t.Fatal(err)
	}
	if w := postBuy(t, h, url.Values{"slug": {n.Slug}, "contact": {"x"}}); w.Code != http.StatusNotFound {
		t.Errorf("私有机器应 404，实际 %d", w.Code)
	}
}

func TestBuyUnknownSlug(t *testing.T) {
	h, _ := newTestHub(t)
	if w := postBuy(t, h, url.Values{"slug": {"nope"}, "contact": {"x"}}); w.Code != http.StatusNotFound {
		t.Errorf("不存在的机器应 404，实际 %d", w.Code)
	}
}
