package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/migration"
	"github.com/Vincentkeio/kokoro/internal/model"
)

type migrationNode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Error    string `json:"error,omitempty"`
	LastSent int64  `json:"last_sent"`
}
type panelMigrationPlan struct {
	ID        string          `json:"id"`
	Target    string          `json:"target"`
	BackupID  string          `json:"backup_id"`
	StartedAt int64           `json:"started_at"`
	Active    bool            `json:"active"`
	Nodes     []migrationNode `json:"nodes"`
	SyncError string          `json:"sync_error,omitempty"`
}

const migrationPlanKey = "panel_migration_plan"

func (h *Hub) loadMigrationLocked() {
	if h.migrationLoaded {
		return
	}
	h.migrationLoaded = true
	raw, _ := h.store.GetSetting(migrationPlanKey)
	if raw != "" {
		var p panelMigrationPlan
		if json.Unmarshal([]byte(raw), &p) == nil {
			h.migrationPlan = &p
		}
	}
}
func (h *Hub) saveMigrationLocked() error {
	b, e := json.Marshal(h.migrationPlan)
	if e != nil {
		return e
	}
	return h.store.SetSetting(migrationPlanKey, string(b))
}

// Only the dedicated signed probe may be requested from the destination. DNS
// answers are checked again at dial time; redirects and ambient proxies are off.
func (h *Hub) probeMigration(ctx context.Context, target string) (migration.Response, error) {
	var out migration.Response
	secret, e := h.store.GetSetting("panel_migration_key")
	if e != nil || secret == "" {
		return out, errors.New("请先导出备份并在新面板导入")
	}
	nonce := randomMigrationID()
	body, _ := json.Marshal(migration.Proof{Nonce: nonce, MAC: migration.MAC(secret, "request", nonce)})
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = safeThemeDialer(8*time.Second, themeLoopbackPolicy{})
	defer tr.CloseIdleConnections()
	client := &http.Client{Timeout: 10 * time.Second, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("迁移验证不接受重定向") }}
	req, e := http.NewRequestWithContext(ctx, "POST", target+"/api/v1/migration/check", bytes.NewReader(body))
	if e != nil {
		return out, e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := client.Do(req)
	if e != nil {
		return out, errors.New("无法通过 HTTPS 连接新面板，请检查地址、证书和网络")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return out, errors.New("新面板尚未导入同一份备份，或版本不支持迁移")
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); e != nil || !migration.Verify(secret, "response", nonce, out.MAC) {
		return out, errors.New("新面板身份验证失败")
	}
	return out, nil
}
func (h *Hub) handleMigrationCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "只接受 POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var p migration.Proof
	if json.NewDecoder(r.Body).Decode(&p) != nil {
		writeErr(w, 400, "请求无效")
		return
	}
	secret, _ := h.store.GetSetting("panel_migration_key")
	id, _ := h.store.GetSetting("panel_restored_backup_id")
	at, _ := h.store.GetSetting("panel_restored_at")
	if secret == "" || id == "" || !migration.Verify(secret, "request", p.Nonce, p.MAC) {
		writeErr(w, 403, "迁移身份不匹配")
		return
	}
	nodes, e := h.store.ListNodes(true)
	if e != nil {
		writeErr(w, 500, "无法读取节点")
		return
	}
	out := migration.Response{BackupID: id, MAC: migration.MAC(secret, "response", p.Nonce)}
	out.RestoredAt, _ = strconv.ParseInt(at, 10, 64)
	for _, n := range nodes {
		if n.LastSeen >= out.RestoredAt && out.RestoredAt > 0 {
			out.Arrivals = append(out.Arrivals, migration.Arrival{NodeID: n.ID, LastSeen: n.LastSeen})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
func (h *Hub) handleMigrationNode(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		writeErr(w, 405, "只接受 POST")
		return
	}
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	n, e := h.store.GetNodeByTokenHash(hashToken(tok))
	if tok == "" || e != nil || n == nil {
		writeErr(w, 401, "节点身份不匹配")
		return
	}
	id, _ := h.store.GetSetting("panel_restored_backup_id")
	if id == "" {
		writeErr(w, 409, "此面板尚未导入迁移备份")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, migration.NodeResponse{NodeID: n.ID, BackupID: id})
}

func (h *Hub) handleAdminMigration(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == "GET" {
		h.syncMigration(r.Context())
		h.migrationMu.Lock()
		defer h.migrationMu.Unlock()
		h.loadMigrationLocked()
		writeJSON(w, 200, map[string]any{"ok": true, "plan": h.migrationPlan})
		return
	}
	if r.Method != "POST" {
		writeErr(w, 405, "只接受 GET 或 POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil {
		writeErr(w, 400, "表单无效")
		return
	}
	if !h.transferMu.TryLock() {
		writeErr(w, 409, "备份或恢复操作进行中")
		return
	}
	defer h.transferMu.Unlock()
	if h.restoreQueued {
		writeErr(w, 409, "面板正在恢复")
		return
	}
	if r.FormValue("action") == "cancel" {
		h.migrationMu.Lock()
		defer h.migrationMu.Unlock()
		h.loadMigrationLocked()
		if h.migrationPlan != nil {
			h.migrationPlan.Active = false
			if e := h.saveMigrationLocked(); e != nil {
				writeErr(w, 500, "保存失败")
				return
			}
		}
		writeJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if r.FormValue("action") != "start" {
		writeErr(w, 400, "操作无效")
		return
	}
	target, e := migration.Target(r.FormValue("target"))
	if e != nil {
		writeErr(w, 400, e.Error())
		return
	}
	if strings.EqualFold(strings.TrimPrefix(target, "https://"), h.cfg.Domain) || strings.EqualFold(strings.TrimPrefix(target, "https://"), r.Host) {
		writeErr(w, 400, "新面板地址必须与旧面板不同；同域名迁移只需切换 DNS")
		return
	}
	id, e := h.store.GetSetting("panel_last_export_id")
	if e != nil || id == "" {
		writeErr(w, 400, "请先导出备份并在新面板导入")
		return
	}
	check, e := h.probeMigration(r.Context(), target)
	if e != nil {
		writeErr(w, 400, e.Error())
		return
	}
	if check.BackupID != id {
		writeErr(w, 400, "新面板没有恢复最近导出的备份，请重新导入后再迁移")
		return
	}
	nodes, e := h.store.ListNodes(true)
	if e != nil || len(nodes) == 0 {
		writeErr(w, 400, "没有可迁移的节点")
		return
	}
	p := &panelMigrationPlan{ID: randomMigrationID(), Target: target, BackupID: id, StartedAt: time.Now().UnixMilli(), Active: true}
	for _, n := range nodes {
		p.Nodes = append(p.Nodes, migrationNode{ID: n.ID, Name: n.Name, State: "waiting"})
	}
	h.migrationMu.Lock()
	defer h.migrationMu.Unlock()
	h.loadMigrationLocked()
	if h.migrationPlan != nil && h.migrationPlan.Active {
		writeErr(w, 409, "已有迁移进行中，请先停止下发")
		return
	}
	previous := h.migrationPlan
	h.migrationPlan = p
	if e = h.saveMigrationLocked(); e != nil {
		h.migrationPlan = previous
		writeErr(w, 500, "保存迁移计划失败")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "plan": p})
}

func (h *Hub) migrationCommand(nodeID string, capability int) *model.Command {
	h.migrationMu.Lock()
	defer h.migrationMu.Unlock()
	h.loadMigrationLocked()
	p := h.migrationPlan
	if p == nil || !p.Active {
		return nil
	}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.ID != nodeID || n.State == "arrived" {
			continue
		}
		if capability < migration.Capability {
			if n.State != "upgrade_required" {
				n.State = "upgrade_required"
				_ = h.saveMigrationLocked()
			}
			return nil
		}
		now := time.Now().UnixMilli()
		if now-n.LastSent < 60000 {
			return nil
		}
		n.LastSent = now
		n.State = "sent"
		n.Error = ""
		if h.saveMigrationLocked() != nil {
			return nil
		}
		return &model.Command{ID: "mig_" + p.ID, Type: "migrate", Payload: map[string]any{"hub": p.Target, "backup_id": p.BackupID}}
	}
	return nil
}
func (h *Hub) migrationResult(nodeID string, res model.CommandResult) (bool, error) {
	if !strings.HasPrefix(res.ID, "mig_") {
		return false, nil
	}
	h.migrationMu.Lock()
	defer h.migrationMu.Unlock()
	h.loadMigrationLocked()
	p := h.migrationPlan
	if p == nil || res.ID != "mig_"+p.ID {
		return true, nil
	}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if n.ID != nodeID {
			continue
		}
		if n.State != "arrived" {
			if res.OK {
				n.State = "awaiting"
				n.Error = ""
			} else {
				n.State = "failed"
				n.Error = res.Stderr
				if len(n.Error) > 300 {
					n.Error = n.Error[:300]
				}
			}
		}
		return true, h.saveMigrationLocked()
	}
	return true, errors.New("迁移计划不包含此节点")
}
func (h *Hub) syncMigration(ctx context.Context) {
	h.migrationMu.Lock()
	h.loadMigrationLocked()
	p := h.migrationPlan
	if p == nil || time.Since(h.migrationSyncAt) < 10*time.Second {
		h.migrationMu.Unlock()
		return
	}
	h.migrationSyncAt = time.Now()
	target, id := p.Target, p.ID
	h.migrationMu.Unlock()
	out, e := h.probeMigration(ctx, target)
	h.migrationMu.Lock()
	defer h.migrationMu.Unlock()
	p = h.migrationPlan
	if p == nil || p.ID != id {
		return
	}
	if e != nil {
		p.SyncError = e.Error()
		return
	}
	if out.BackupID != p.BackupID {
		p.SyncError = "目标面板的备份身份已改变，已停止下发"
		p.Active = false
		_ = h.saveMigrationLocked()
		return
	}
	p.SyncError = ""
	arrived := map[string]bool{}
	for _, n := range out.Arrivals {
		arrived[n.NodeID] = true
	}
	all := true
	changed := false
	for i := range p.Nodes {
		if arrived[p.Nodes[i].ID] && p.Nodes[i].State != "arrived" {
			p.Nodes[i].State = "arrived"
			p.Nodes[i].Error = ""
			changed = true
		}
		if p.Nodes[i].State != "arrived" {
			all = false
		}
	}
	if all && p.Active {
		p.Active = false
		changed = true
	}
	if changed {
		_ = h.saveMigrationLocked()
	}
}
