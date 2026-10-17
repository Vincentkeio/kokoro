package hub

// 后台的「测试」页：选一台小鸡，下发一条固定测试指令，看历史结果。

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// testScriptView 是模板用的测试项：原始定义 + 指令摘要。
//
// ⚠️ **命令必须在这里拼好放进视图**，不能让模板直接取 `.Cmd` ——
// `testScript` 上**没有** Cmd 这个字段，而 Go 模板遇到不存在的字段
// 会**中止整个渲染**：输出截断在那里，后面全没了。
//
// 这个坑在本站躺了很久：模板从第一个提交起就写着 `{{.Cmd}}`，
// 于是这个页面**永远只显示第一张卡、而且那张卡是断的**
// （只有标题和说明，没有命令、没有「下发」按钮）。
// 不报错、不标红，看起来就像"另外两个测试项没做"。
type testScriptView struct {
	testScript
	Cmd string
}

type adminTasksData struct {
	adminData
	Node    *model.Node
	Scripts []testScriptView
	Tasks   []model.NodeTask
	Msg     string
	Err     string
}

type adminTaskRow struct {
	Task     model.NodeTask
	NodeName string
}

type adminTaskListData struct {
	adminData
	Rows []adminTaskRow
	Msg  string
	Err  string
}

func (h *Hub) adminTaskRows() []adminTaskRow {
	tasks, err := h.store.ListTasks("", 50)
	if err != nil {
		return nil
	}
	nodes, err := h.store.ListNodes(true)
	if err != nil {
		return nil
	}
	names := make(map[string]string, len(nodes))
	for _, node := range nodes {
		names[node.ID] = node.Name
	}
	rows := make([]adminTaskRow, 0, len(tasks))
	for _, task := range tasks {
		rows = append(rows, adminTaskRow{Task: task, NodeName: names[task.NodeID]})
	}
	return rows
}

func (h *Hub) renderAdminTaskRows() string {
	return string(h.renderPartial("admin_task_list_rows.html", h.adminTaskRows()))
}

func (h *Hub) renderTaskHistory(nodeID string) (string, bool) {
	node, err := h.store.GetNode(nodeID)
	if err != nil || node == nil {
		return "", false
	}
	tasks, err := h.store.ListTasks(nodeID, 20)
	if err != nil {
		return "", false
	}
	html := h.renderPartial("task_history.html", map[string]any{
		"Node": node, "Tasks": tasks,
	})
	if html == "" {
		return "", false
	}
	return string(html), true
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
		if r.FormValue("action") == "cancel" {
			id := strings.TrimSpace(r.FormValue("node"))
			taskID := strings.TrimSpace(r.FormValue("task_id"))
			to := "/admin/tasks?node=" + id
			if r.FormValue("scope") == "all" {
				to = "/admin/tasks"
			}
			addQuery := func(key, value string) {
				separator := "&"
				if !strings.Contains(to, "?") {
					separator = "?"
				}
				to += separator + key + "=" + urlQueryEscape(value)
			}
			task, err := h.store.GetTask(taskID)
			if err != nil || task == nil || task.NodeID != id {
				addQuery("err", "任务不存在")
			} else {
				changed, cancelErr := h.store.CancelTask(taskID, id)
				if cancelErr != nil {
					addQuery("err", cancelErr.Error())
				} else if !changed {
					addQuery("err", "任务已结束，无法取消")
				} else {
					_ = h.store.AddEvent(id, "task", task.Title+"已取消", taskID)
					h.broadcastTaskProgress(taskID)
					h.broadcastTaskCard(id)
					addQuery("msg", task.Title+"已取消")
				}
			}
			http.Redirect(w, r, to, http.StatusSeeOther)
			return
		}
		// 下发只接受 POST：GET 触发会变成"点一下链接就跑测试"，
		// 浏览器预取或爬虫碰到就会误跑，跑分还特别吃 CPU。
		id := strings.TrimSpace(r.FormValue("node"))
		kind := strings.TrimSpace(r.FormValue("kind"))
		ipKind := "ipquality"
		if r.FormValue("ip_family_selection") == "1" {
			families := r.PostForm["ip_family"]
			v4, v6 := false, false
			for _, family := range families {
				if family == "4" {
					v4 = true
				} else if family == "6" {
					v6 = true
				} else {
					http.Error(w, "未知 IP 协议", http.StatusBadRequest)
					return
				}
			}
			if !v4 && !v6 {
				http.Redirect(w, r, "/admin/tasks?node="+urlQueryEscape(id)+"&err="+urlQueryEscape("请至少选择 IPv4 或 IPv6"), http.StatusSeeOther)
				return
			}
			if !v6 {
				ipKind = "ipquality4"
			}
			if !v4 {
				ipKind = "ipquality6"
			}
		}
		if kind == "netquality" && ipKind != "ipquality" {
			kind = "netquality" + strings.TrimPrefix(ipKind, "ipquality")
		}
		if kind == "ipquality" {
			kind = ipKind
		}
		if kind == "selected-nodes" || kind == "all-nodes" {
			nodes, queued, err := h.dispatchSelectedIP(r.PostForm["node_ids"], ipKind)
			msg := fmt.Sprintf("已为选中的 %d 台服务器排入 %d 项测试；每台服务器的三项测试会依次执行", nodes, queued)
			if err != nil {
				if nodes == 0 {
					msg = err.Error()
				} else {
					msg += "；部分任务未能排入：" + err.Error()
				}
			}
			if nodes == 0 && err == nil {
				msg = "当前没有可测试的服务器"
			}
			http.Redirect(w, r, "/admin?taskmsg="+urlQueryEscape(msg)+"#nodes", http.StatusSeeOther)
			return
		}
		if id == "" || kind == "" {
			http.Redirect(w, r, "/admin/tasks", http.StatusSeeOther)
			return
		}
		to := "/admin/tasks?node=" + id
		if kind == "all" {
			n, err := h.dispatchAllIP(id, ipKind)
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

	if nodeID == "" {
		h.render(w, "admin_task_list.html", &adminTaskListData{
			adminData: h.adminBase(r),
			Rows:      h.adminTaskRows(),
			Msg:       strings.TrimSpace(r.URL.Query().Get("msg")),
			Err:       strings.TrimSpace(r.URL.Query().Get("err")),
		}, r)
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
		Scripts:   h.scriptViews(),
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

// scriptViews 把内置测试项转成模板用的视图（带上指令摘要）。
//
// 抽成方法是为了让**测试也能拿到同一份数据** —— 之前测试手拼，
// 和线上跑的不是一回事，就漏掉了"命令为空"这种情况。
func (h *Hub) scriptViews() []testScriptView {
	out := make([]testScriptView, 0, len(testScripts))
	for _, sc := range testScripts {
		out = append(out, testScriptView{testScript: sc, Cmd: "test:" + sc.Kind})
	}
	return out
}
