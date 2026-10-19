package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
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
	s, err := h.store.GetSetting(agentUpdateKey)
	if err != nil {
		log.Printf("[update] 无法读取探针更新计划: %v", err)
		return
	}
	if s != "" {
		var p agentUpdatePlan
		if err := json.Unmarshal([]byte(s), &p); err != nil {
			log.Printf("[update] 探针更新计划无效: %v", err)
			return
		}
		h.updatePlan = &p
	}
	h.updateLoaded = true
}
func (h *Hub) saveUpdateLocked() error {
	b, e := json.Marshal(h.updatePlan)
	if e != nil {
		return e
	}
	return h.store.SetSetting(agentUpdateKey, string(b))
}

type updateAvailability struct {
	onlineCount    int
	agentCount     int
	hubNewer       bool
	newerByVersion map[string]bool
}

func (h *Hub) evaluateUpdateAvailability(ctx context.Context, release panelupdate.Release, nodes []model.Node, nowMS int64) (updateAvailability, error) {
	result := updateAvailability{newerByVersion: map[string]bool{release.Tag: false}}
	for _, node := range nodes {
		if !nodeConsideredOnline(node, nowMS) {
			continue
		}
		result.onlineCount++
		newer, ok := result.newerByVersion[node.AgentVer]
		if !ok {
			var err error
			newer, err = panelupdate.IsNewerRelease(ctx, release, node.AgentVer)
			if err != nil {
				return result, fmt.Errorf("无法读取当前探针版本的发布时间：%w", err)
			}
			result.newerByVersion[node.AgentVer] = newer
		}
		if newer {
			result.agentCount++
		}
	}
	newer, err := panelupdate.IsNewerRelease(ctx, release, Version)
	if err != nil {
		return result, fmt.Errorf("无法读取当前面板版本的发布时间：%w", err)
	}
	result.hubNewer = newer
	return result, nil
}

func (h *Hub) queueAgentUpdatePlan(target string, nodes []model.Node, newerByVersion map[string]bool, nowMS int64) (int, error) {
	h.migrationMu.Lock()
	h.loadMigrationLocked()
	active := h.migrationPlan != nil && h.migrationPlan.Active
	h.migrationMu.Unlock()
	if active {
		return 0, errors.New("请先完成或停止探针迁移")
	}
	h.updateMu.Lock()
	defer h.updateMu.Unlock()
	h.loadUpdateLocked()
	if !h.updateLoaded {
		return 0, errors.New("无法读取现有探针更新计划，请检查后再更新")
	}
	if h.updatePlan != nil {
		for _, node := range h.updatePlan.Nodes {
			if (node.State == "waiting" || node.State == "running") && time.Now().UnixMilli()-h.updatePlan.StartedAt < 20*60*1000 {
				return 0, errors.New("已有探针更新正在进行")
			}
		}
	}
	plan := &agentUpdatePlan{ID: randomMigrationID(), Target: target, StartedAt: nowMS}
	for _, node := range nodes {
		if !nodeConsideredOnline(node, nowMS) {
			continue
		}
		state, message := "waiting", "等待节点接收更新"
		if !newerByVersion[node.AgentVer] {
			state, message = "current", "已是最新版本"
		}
		plan.Nodes = append(plan.Nodes, agentUpdateNode{ID: node.ID, Name: node.Name, State: state, Message: message})
	}
	if len(plan.Nodes) == 0 {
		return 0, errors.New("没有在线的探针，未下发更新")
	}
	newerCount := 0
	for _, node := range plan.Nodes {
		if node.State == "waiting" {
			newerCount++
		}
	}
	if newerCount == 0 {
		return 0, nil
	}
	encoded, err := json.Marshal(plan)
	if err != nil {
		return 0, err
	}
	if err := h.store.SetSetting(agentUpdateKey, string(encoded)); err != nil {
		return 0, err
	}
	h.updatePlan = plan
	return newerCount, nil
}

func (h *Hub) launchHubUpdate(target string) error {
	data, err := filepath.Abs(h.cfg.DataDir)
	if err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(h.cfg.Listen)
	if err != nil {
		return errors.New("无法确认面板监听端口")
	}
	launch := h.updateLauncher
	if launch == nil {
		launch = panelupdate.Launch
	}
	return launch(panelupdate.Spec{Role: "hub", Target: target, ID: randomMigrationID(), StatePath: h.hubUpdatePath(), DataDir: data, HealthURL: "http://127.0.0.1:" + port + "/"})
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
	if !h.updateLoaded {
		return true
	}
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
	if h.updateLoaded && h.updatePlan != nil {
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
	response := h.autoUpdateSnapshot()
	response["ok"] = true
	response["hub"] = h.hubUpdateState()
	response["plan"] = h.updatePlan
	if !h.updateLoaded {
		// 探针更新计划损坏时，仍向前端提供自动更新设置，避免开关
		// 永远停留在“读取中”。真正启动更新仍由写操作 fail closed。
		response["plan_error"] = "无法读取探针更新计划，请检查服务日志"
	}
	response["supported"] = panelupdate.HubSupported(h.cfg.DataDir)
	writeJSON(w, 200, response)
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
	if action == "auto" {
		value := r.FormValue("enabled")
		if value != "0" && value != "1" {
			writeErr(w, 400, "自动更新设置无效")
			return
		}
		settings := map[string]string{autoUpdateEnabledKey: value}
		if value == "0" {
			settings[autoUpdatePendingHubKey] = ""
			settings[autoUpdateNextCheckKey] = ""
		} else if !h.autoUpdateEnabled() {
			settings[autoUpdateNextCheckKey] = strconv.FormatInt(nextAutoUpdateCheck(time.Now()).UnixMilli(), 10)
		}
		if err := h.store.SetSettings(settings); err != nil {
			writeErr(w, 500, "保存自动更新设置失败")
			return
		}
		h.wakeAutoUpdateLoop()
		response := h.autoUpdateSnapshot()
		response["ok"] = true
		writeJSON(w, 200, response)
		return
	}
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
	now := time.Now().UnixMilli()
	availability, e := h.evaluateUpdateAvailability(ctx, release, nodes, now)
	if e != nil {
		writeErr(w, 502, e.Error())
		return
	}
	if action == "check" {
		writeJSON(w, 200, map[string]any{"ok": true, "target": tag, "prerelease": prerelease, "published_at": publishedAt, "hub_available": availability.hubNewer, "agent_count": availability.agentCount, "online_count": availability.onlineCount, "supported": panelupdate.HubSupported(h.cfg.DataDir)})
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
		if h.agentUpdatesBusy() {
			writeErr(w, 409, "探针正在更新，请等待任务结束后再更新面板")
			return
		}
		if !availability.hubNewer {
			writeErr(w, 409, "当前面板没有可用更新")
			return
		}
		e = h.launchHubUpdate(tag)
		if e != nil {
			writeErr(w, 503, e.Error())
			return
		}
	} else {
		nodes, e = h.store.ListNodes(true)
		if e != nil {
			writeErr(w, 500, "无法读取节点")
			return
		}
		count, err := h.queueAgentUpdatePlan(tag, nodes, availability.newerByVersion, time.Now().UnixMilli())
		if err != nil {
			writeErr(w, 409, err.Error())
			return
		}
		if count == 0 {
			writeErr(w, 409, "在线的探针都已是最新版本")
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
		// An update command is dispatched at most once per plan, even across
		// Hub restarts. A failed/lost attempt waits for the next daily check
		// or an explicit manual update instead of repeatedly restarting agents.
		if u.LastSent > 0 {
			return nil
		}
		previous := *u
		u.LastSent = time.Now().UnixMilli()
		u.State = "running"
		u.Message = "已下发，正在更新"
		if err := h.saveUpdateLocked(); err != nil {
			*u = previous
			log.Printf("[update] 无法保存探针更新下发记录: %v", err)
			return nil
		}
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
