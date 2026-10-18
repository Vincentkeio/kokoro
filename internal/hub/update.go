package hub

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/panelupdate"
)

type agentUpdateNode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Message  string `json:"message"`
	LastSent int64  `json:"last_sent"`
}
type agentUpdatePlan struct {
	ID        string            `json:"id"`
	Target    string            `json:"target"`
	StartedAt int64             `json:"started_at"`
	Nodes     []agentUpdateNode `json:"nodes"`
}

const agentUpdateKey = "panel_agent_update_plan"

// nodeOnlineWindow 与界面上的在线判定一致：最近这段时间内上报过才算在线。
const nodeOnlineWindow = 20 * time.Second

// nodeConsideredOnline 报告节点此刻是否在线。探针更新只下发到在线节点：
// 离线节点收不到指令，把它们算进去只会留下永远不清零的"待更新"数字。
func nodeConsideredOnline(n model.Node, nowMS int64) bool {
	if !n.Online || n.LastSeen <= 0 {
		return false
	}
	age := nowMS - n.LastSeen
	return age <= nodeOnlineWindow.Milliseconds() && age >= -nodeOnlineWindow.Milliseconds()
}

func (h *Hub) loadUpdateLocked() {
	if h.updateLoaded {
		return
	}
	h.updateLoaded = true
	s, _ := h.store.GetSetting(agentUpdateKey)
	if s != "" {
		var p agentUpdatePlan
		if json.Unmarshal([]byte(s), &p) == nil {
			h.updatePlan = &p
		}
	}
}
func (h *Hub) saveUpdateLocked() error {
	b, e := json.Marshal(h.updatePlan)
	if e != nil {
		return e
	}
	return h.store.SetSetting(agentUpdateKey, string(b))
}
func (h *Hub) hubUpdatePath() string {
	p, _ := filepath.Abs(filepath.Join(h.cfg.DataDir, "web-update-hub.json"))
	return p
}
func (h *Hub) hubUpdateState() *panelupdate.State {
	s, e := panelupdate.Load(h.hubUpdatePath())
	if e != nil {
		return nil
	}
	if s.Busy() && time.Now().UnixMilli()-s.UpdatedAt > 20*60*1000 {
		s.Status = "failed"
		s.Message = "更新进程未能按时完成，请检查服务后重试"
	}
	return &s
}
func (h *Hub) updateBusy() bool { s := h.hubUpdateState(); return s != nil && s.Busy() }
func (h *Hub) agentUpdatesBusy() bool {
	h.updateMu.Lock()
	defer h.updateMu.Unlock()
	h.loadUpdateLocked()
	if h.updatePlan == nil || time.Now().UnixMilli()-h.updatePlan.StartedAt > 20*60*1000 {
		return false
	}
	for _, n := range h.updatePlan.Nodes {
		if n.State == "waiting" || n.State == "running" {
			return true
		}
	}
	return false
}
func (h *Hub) updateStatus(w http.ResponseWriter, r *http.Request) {
	h.updateMu.Lock()
	defer h.updateMu.Unlock()
	h.loadUpdateLocked()
	changed := false
	if h.updatePlan != nil {
		for i := range h.updatePlan.Nodes {
			n := &h.updatePlan.Nodes[i]
			if (n.State == "waiting" || n.State == "running") && time.Now().UnixMilli()-h.updatePlan.StartedAt > 20*60*1000 {
				n.State = "failed"
				n.Message = "等待更新超时，可检查节点后重试"
				changed = true
			}
		}
	}
	if changed {
		_ = h.saveUpdateLocked()
	}
	writeJSON(w, 200, map[string]any{"ok": true, "hub": h.hubUpdateState(), "plan": h.updatePlan, "supported": panelupdate.HubSupported(h.cfg.DataDir)})
}
func (h *Hub) handleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" {
		h.updateStatus(w, r)
		return
	}
	if r.Method != "POST" {
		writeErr(w, 405, "只接受 GET 或 POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if r.ParseForm() != nil {
		writeErr(w, 400, "请求无效")
		return
	}
	action := r.FormValue("action")
	if action != "check" && action != "hub" && action != "agents" {
		writeErr(w, 400, "更新操作无效")
		return
	}
	if !h.limiter.allow("web-update-"+action, 2*time.Second) {
		writeErr(w, 429, "请稍后再试")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()
	h.updateMu.Lock()
	tag := h.updateReleaseTag
	at := h.updateReleaseAt
	publishedAt := h.updateReleasePublishedAt
	prerelease := h.updateReleasePrerelease
	h.updateMu.Unlock()
	if tag == "" || time.Since(at) > time.Minute {
		rel, e := panelupdate.Latest(ctx)
		if e != nil {
			writeErr(w, 502, e.Error())
			return
		}
		tag = rel.Tag
		publishedAt = rel.PublishedAt
		prerelease = rel.Prerelease
		h.updateMu.Lock()
		h.updateReleaseTag = tag
		h.updateReleaseAt = time.Now()
		h.updateReleasePublishedAt = publishedAt
		h.updateReleasePrerelease = prerelease
		h.updateMu.Unlock()
	}
	release := panelupdate.Release{Tag: tag, PublishedAt: publishedAt, Prerelease: prerelease}
	nodes, e := h.store.ListNodes(true)
	if e != nil {
		writeErr(w, 500, "无法读取节点")
		return
	}
	// 只统计在线的探针。离线节点收不到指令，把它们算进来只会让"有 N 台待更新"
	// 变成永远清不掉的假数字（迁移到新面板后旧面板还挂着全部节点时尤其明显）。
	now := time.Now().UnixMilli()
	online := 0
	count := 0
	newerByVersion := map[string]bool{tag: false}
	for _, n := range nodes {
		if !nodeConsideredOnline(n, now) {
			continue
		}
		online++
		newer, ok := newerByVersion[n.AgentVer]
		if !ok {
			newer, e = panelupdate.IsNewerRelease(ctx, release, n.AgentVer)
			if e != nil {
				writeErr(w, 502, "无法读取当前探针版本的发布时间，请稍后重试")
				return
			}
			newerByVersion[n.AgentVer] = newer
		}
		if newer {
			count++
		}
	}
	hubNewer, e := panelupdate.IsNewerRelease(ctx, release, Version)
	if e != nil {
		writeErr(w, 502, "无法读取当前面板版本的发布时间，请稍后重试")
		return
	}
	if action == "check" {
		writeJSON(w, 200, map[string]any{"ok": true, "target": tag, "prerelease": prerelease, "published_at": publishedAt, "hub_available": hubNewer, "agent_count": count, "online_count": online, "supported": panelupdate.HubSupported(h.cfg.DataDir)})
		return
	}
	if r.FormValue("target") != tag {
		writeErr(w, 409, "发布信息已变化，请重新检查更新")
		return
	}
	if !h.transferMu.TryLock() {
		writeErr(w, 409, "备份或恢复正在进行，请稍后再更新")
		return
	}
	defer h.transferMu.Unlock()
	if h.restoreQueued || h.updateBusy() {
		writeErr(w, 409, "面板正在恢复或更新，请稍后重试")
		return
	}
	if action == "hub" {
		if !hubNewer {
			writeErr(w, 409, "当前面板没有可用更新")
			return
		}
		data, _ := filepath.Abs(h.cfg.DataDir)
		_, port, e := net.SplitHostPort(h.cfg.Listen)
		if e != nil {
			writeErr(w, 503, "无法确认面板监听端口")
			return
		}
		e = panelupdate.Launch(panelupdate.Spec{Role: "hub", Target: tag, ID: randomMigrationID(), StatePath: h.hubUpdatePath(), DataDir: data, HealthURL: "http://127.0.0.1:" + port + "/"})
		if e != nil {
			writeErr(w, 503, e.Error())
			return
		}
	} else {
		h.migrationMu.Lock()
		h.loadMigrationLocked()
		active := h.migrationPlan != nil && h.migrationPlan.Active
		h.migrationMu.Unlock()
		if active {
			writeErr(w, 409, "请先完成或停止探针迁移")
			return
		}
		h.updateMu.Lock()
		defer h.updateMu.Unlock()
		h.loadUpdateLocked()
		if h.updatePlan != nil {
			for _, n := range h.updatePlan.Nodes {
				if (n.State == "waiting" || n.State == "running") && time.Now().UnixMilli()-h.updatePlan.StartedAt < 20*60*1000 {
					writeErr(w, 409, "已有探针更新正在进行")
					return
				}
			}
		}
		p := &agentUpdatePlan{ID: randomMigrationID(), Target: tag, StartedAt: time.Now().UnixMilli()}
		for _, n := range nodes {
			if !nodeConsideredOnline(n, now) {
				continue
			}
			state, msg := "waiting", "等待节点接收更新"
			if !newerByVersion[n.AgentVer] {
				state = "current"
				msg = "已是最新版本"
			}
			p.Nodes = append(p.Nodes, agentUpdateNode{ID: n.ID, Name: n.Name, State: state, Message: msg})
		}
		if count == 0 {
			if online == 0 {
				writeErr(w, 409, "没有在线的探针，未下发更新")
			} else {
				writeErr(w, 409, "在线的探针都已是最新版本")
			}
			return
		}
		h.updatePlan = p
		if e = h.saveUpdateLocked(); e != nil {
			writeErr(w, 500, "保存更新计划失败")
			return
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "message": "更新任务已启动"})
}

func (h *Hub) updateCommand(n *model.Node, m *model.Metrics) *model.Command {
	if panelupdate.ValidTag(m.AgentVersion) && n.AgentVer != m.AgentVersion {
		_ = h.store.UpdateAgentVersion(n.ID, m.AgentVersion)
	}
	h.updateMu.Lock()
	defer h.updateMu.Unlock()
	h.loadUpdateLocked()
	p := h.updatePlan
	if p == nil {
		return nil
	}
	for i := range p.Nodes {
		u := &p.Nodes[i]
		if u.ID != n.ID {
			continue
		}
		if u.State == "completed" || u.State == "current" || u.State == "failed" || u.State == "unsupported" {
			return nil
		}
		set := func(state, msg string) {
			if u.State != state || u.Message != msg {
				u.State = state
				u.Message = msg
				_ = h.saveUpdateLocked()
			}
		}
		if panelupdate.ValidTag(m.AgentVersion) && m.AgentVersion == p.Target {
			set("completed", "已更新并恢复上报")
			return nil
		}
		if time.Now().UnixMilli()-p.StartedAt > 20*60*1000 {
			set("failed", "等待更新超时")
			return nil
		}
		if m.UpdateVersion < 1 {
			set("unsupported", "此探针需先通过服务器命令更新一次")
			return nil
		}
		if s := m.UpdateState; s != nil && s.ID == p.ID {
			if s.Status == "failed" {
				set("failed", strings.TrimSpace(s.Message))
				return nil
			}
			if s.Busy() {
				set("running", s.Message)
				return nil
			}
		}
		if time.Now().UnixMilli()-u.LastSent < 60000 {
			return nil
		}
		u.LastSent = time.Now().UnixMilli()
		u.State = "running"
		u.Message = "已下发，正在更新"
		_ = h.saveUpdateLocked()
		return &model.Command{ID: p.ID, Type: "upgrade", Payload: map[string]any{"version": p.Target}}
	}
	return nil
}

func (h *Hub) updateResult(nodeID string, res model.CommandResult) bool {
	h.updateMu.Lock()
	defer h.updateMu.Unlock()
	h.loadUpdateLocked()
	if h.updatePlan == nil || h.updatePlan.ID != res.ID {
		return false
	}
	for i := range h.updatePlan.Nodes {
		n := &h.updatePlan.Nodes[i]
		if n.ID == nodeID {
			if !res.OK && (n.State == "waiting" || n.State == "running") {
				n.State = "failed"
				n.Message = res.Stderr
				if len(n.Message) > 1024 {
					n.Message = "无法启动探针更新，请检查节点的服务环境"
				}
				if n.Message == "" {
					n.Message = "无法启动探针更新"
				}
				_ = h.saveUpdateLocked()
			}
			return true
		}
	}
	return true
}
