package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestHostSelfSnapshotReusesAgent(t *testing.T) {
	m := &model.Metrics{CPU: model.CPUStat{Usage: 38.5}}
	nodes := []model.Node{{ID: "local", Hostname: "hub-host"}, {ID: "remote", Hostname: "other"}}
	snap := map[string]*model.Metrics{"local": m}
	id, got, note := hostSelfSnapshot("hub-host", nodes, snap)
	if id != "local" || got != m || note != "" {
		t.Fatalf("must reuse the exact Agent snapshot: id=%q metrics=%p note=%q", id, got, note)
	}
	_, got, note = hostSelfSnapshot("unknown", nodes, snap)
	if got != nil || note == "" {
		t.Fatal("an unrelated node must not be shown as the Hub host")
	}
	nodes = append(nodes, model.Node{ID: "duplicate", Hostname: "hub-host"})
	_, got, note = hostSelfSnapshot("hub-host", nodes, snap)
	if got != nil || note == "" {
		t.Fatal("ambiguous hostnames must not select a machine arbitrarily")
	}
}

func TestAdminVisibilityJSONIdempotent(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "visibility-performance", "US", "洛杉矶", "")
	cookies := loginAsAdmin(t, h)
	for i := 0; i < 2; i++ {
		req := withCookies(httptest.NewRequest(http.MethodPost, "/admin/nodes", strings.NewReader("action=toggle&id="+n.ID+"&visibility=private")), cookies)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		var response struct {
			OK         bool             `json:"ok"`
			Visibility model.Visibility `json:"visibility"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || !response.OK || response.Visibility != model.VisibilityPrivate {
			t.Fatalf("save must return JSON without a page redirect: %d %s", w.Code, w.Body.String())
		}
	}
	got, err := st.GetNode(n.ID)
	if err != nil || got.Visibility != model.VisibilityPrivate || got.Name != n.Name || got.Online != n.Online || got.LastSeen != n.LastSeen {
		t.Fatalf("visibility save altered unrelated Agent state: %+v %v", got, err)
	}
	req := httptest.NewRequest(http.MethodPost, "/admin/nodes", strings.NewReader("action=toggle&id="+n.ID))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expired sessions must receive 401, not a successful login page: %d", w.Code)
	}
}

func TestAdminSortOrderJSONUpdatesOrderOnly(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "sort-order-performance", "US", "洛杉矶", "")
	if err := st.SetNodeSortOrder(n.ID, 8); err != nil {
		t.Fatal(err)
	}
	cookies := loginAsAdmin(t, h)
	req := withCookies(httptest.NewRequest(http.MethodPost, "/admin/nodes", strings.NewReader("action=sort_order&id="+n.ID+"&sort_order=-3")), cookies)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	var response struct {
		OK        bool   `json:"ok"`
		ID        string `json:"id"`
		SortOrder int    `json:"sort_order"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &response) != nil || !response.OK || response.ID != n.ID || response.SortOrder != -3 {
		t.Fatalf("sort save must return the saved order: %d %s", w.Code, w.Body.String())
	}
	got, err := st.GetNode(n.ID)
	if err != nil || got.SortOrder != -3 || got.Name != n.Name || got.Online != n.Online || got.LastSeen != n.LastSeen {
		t.Fatalf("sort save altered unrelated node fields: %+v %v", got, err)
	}
	other := mkNode(t, st, "sort-order-later", "US", "纽约", "")
	if err := st.SetNodeSortOrder(other.ID, 5); err != nil {
		t.Fatal(err)
	}
	ordered, err := st.ListNodes(true)
	if err != nil || len(ordered) != 2 || ordered[0].ID != n.ID || ordered[1].ID != other.ID {
		t.Fatalf("node lists must follow the editable sort order: %+v %v", ordered, err)
	}
	bad := withCookies(httptest.NewRequest(http.MethodPost, "/admin/nodes", strings.NewReader("action=sort_order&id="+n.ID+"&sort_order=1.5")), cookies)
	bad.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	bad.Header.Set("Accept", "application/json")
	badW := httptest.NewRecorder()
	h.ServeHTTP(badW, bad)
	if badW.Code != http.StatusBadRequest {
		t.Fatalf("fractional order must be rejected: %d %s", badW.Code, badW.Body.String())
	}
}
