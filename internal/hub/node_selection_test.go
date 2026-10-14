package hub

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDispatchSelectedNodesOnlyQueuesSelection(t *testing.T) {
	h, st := newTestHub(t)
	a := mkNode(t, st, "Selected A", "JP", "Tokyo", "")
	b := mkNode(t, st, "Unselected B", "US", "LA", "")
	c := mkNode(t, st, "Selected offline C", "HK", "Hong Kong", "")
	nodes, queued, err := h.DispatchSelectedNodes([]string{a.ID, c.ID, a.ID})
	if err != nil || nodes != 2 || queued != 6 {
		t.Fatalf("selection result: %d/%d %v", nodes, queued, err)
	}
	for _, item := range []struct {
		id    string
		count int
	}{{a.ID, 3}, {b.ID, 0}, {c.ID, 3}} {
		tasks, err := st.ListTasks(item.id, 10)
		if err != nil || len(tasks) != item.count {
			t.Fatalf("node %s: %d tasks, %v", item.id, len(tasks), err)
		}
	}
	_, queued, err = h.DispatchSelectedNodes([]string{a.ID, c.ID})
	if err != nil || queued != 0 {
		t.Fatalf("existing tasks duplicated: %d %v", queued, err)
	}
}

func TestDispatchSelectedNodesRejectsInvalidSelectionBeforeQueueing(t *testing.T) {
	for _, mode := range []string{"empty", "unknown", "blank"} {
		t.Run(mode, func(t *testing.T) {
			h, st := newTestHub(t)
			n := mkNode(t, st, "No unexpected tasks", "JP", "Tokyo", "")
			var ids []string
			if mode == "unknown" {
				ids = []string{n.ID, "deleted-node"}
			}
			if mode == "blank" {
				ids = []string{n.ID, " "}
			}
			count, queued, err := h.DispatchSelectedNodes(ids)
			if err == nil || count != 0 || queued != 0 {
				t.Fatalf("invalid selection accepted: %d/%d %v", count, queued, err)
			}
			tasks, _ := st.ListTasks(n.ID, 10)
			if len(tasks) != 0 {
				t.Fatal("validation created a partial task queue")
			}
		})
	}
}

func TestSelectedNodesPostIgnoresQueryAndRejectsEmptyLegacyRequest(t *testing.T) {
	h, st := newTestHub(t)
	a := mkNode(t, st, "Selected", "JP", "Tokyo", "")
	b := mkNode(t, st, "Query-only node", "US", "LA", "")
	values := url.Values{"kind": {"selected-nodes"}, "node_ids": {a.ID}}
	r := httptest.NewRequest(http.MethodPost, "/admin/tasks?node_ids="+b.ID, strings.NewReader(values.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.handleAdminTasks(w, r, true)
	if w.Code != http.StatusSeeOther {
		t.Fatal(w.Code)
	}
	tasks, _ := st.ListTasks(a.ID, 10)
	untouched, _ := st.ListTasks(b.ID, 10)
	if len(tasks) != 3 || len(untouched) != 0 {
		t.Fatal("query parameters expanded the selection")
	}
	r = httptest.NewRequest(http.MethodPost, "/admin/tasks", strings.NewReader("kind=all-nodes"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	h.handleAdminTasks(w, r, true)
	target, _ := url.Parse(w.Header().Get("Location"))
	if !strings.Contains(target.Query().Get("taskmsg"), "请先选择") {
		t.Fatal("empty legacy submission ran all nodes")
	}
	untouched, _ = st.ListTasks(b.ID, 10)
	if len(untouched) != 0 {
		t.Fatal("unselected node received tasks")
	}
}

func TestAdminNodeSelectionControlsUseIndependentForm(t *testing.T) {
	h, st := newTestHub(t)
	mkNode(t, st, "A", "JP", "Tokyo", "")
	mkNode(t, st, "B", "US", "LA", "")
	body := adminBody(t, h, st)
	for _, text := range []string{"一键测试选中的服务器", "取消全部", "data-node-select-all", "data-node-selection-count", `form="node-selected-tests"`} {
		if !strings.Contains(body, text) {
			t.Fatalf("missing control: %s", text)
		}
	}
	if strings.Count(body, `name="node_ids"`) != 2 || strings.Contains(body, "一键测试全部服务器") {
		t.Fatal("node selection controls are incomplete")
	}
}
