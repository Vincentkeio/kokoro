package hub

// 后台的「测试」页：选一台小鸡，下发一条测试脚本，看历史结果。

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Vincentkeio/kokoro/internal/model"
)

type adminTasksData struct {
	adminData
	Node    *model.Node
	Scripts []testScript
	Tasks   []model.NodeTask
	Msg     string
	Err     string
}

func (h *Hub) handleAdminTasks(w http.ResponseWriter, r *http.Request, authed bool) {
	if !authed {
		h.render(w, "login.html", map[string]any{"SiteName": h.cfg.SiteName}, r)
		return
	}
	nodeID := strings.TrimSpace(r.URL.Query().Get("node"))

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "表单解析失败", http.StatusBadRequest)
			return
		}
		// 下发只接受 POST：GET 触发会变成"点一下链接就跑测试"，
		// 浏览器预取或爬虫碰到就会误跑，跑分还特别吃 CPU。
		id := strings.TrimSpace(r.FormValue("node"))
		kind := strings.TrimSpace(r.FormValue("kind"))
		if id == "" || kind == "" {
			http.Redirect(w, r, "/admin/tasks", http.StatusSeeOther)
			return
		}
		to := "/admin/tasks?node=" + id
		if kind == "all" {
			n, err := h.DispatchAll(id)
			if err != nil && n == 0 {
				to += "&err=" + urlQueryEscape(err.Error())
			} else {
				to += "&msg=" + urlQueryEscape(fmt.Sprintf(
					"已排入 %d 项测试，会按顺序一台一台跑（前面跑完才轮到下一个）", n))
			}
		} else {
			_, err := h.DispatchTask(id, kind)
			if err != nil {
				to += "&err=" + urlQueryEscape(err.Error())
			} else {
				to += "&msg=已下发，等小鸡下一次上报就会开始执行"
			}
		}
		http.Redirect(w, r, to, http.StatusSeeOther)
		return
	}

	node, err := h.store.GetNode(nodeID)
	if err != nil || node == nil {
		// 没指定节点就跳回后台列表
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	tasks, _ := h.store.ListTasks(node.ID, 20)
	h.render(w, "admin_tasks.html", &adminTasksData{
		adminData: h.adminBase(r),
		Node:      node,
		Scripts:   testScripts,
		Tasks:     tasks,
		Msg:       strings.TrimSpace(r.URL.Query().Get("msg")),
		Err:       strings.TrimSpace(r.URL.Query().Get("err")),
	}, r)
}

// urlQueryEscape 是 url.QueryEscape 的小包装，避免在这个文件再导一次 net/url。
func urlQueryEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			const hex = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&15])
		}
	}
	return b.String()
}
