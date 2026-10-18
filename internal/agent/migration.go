package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Vincentkeio/kokoro/internal/migration"
	"github.com/Vincentkeio/kokoro/internal/model"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var ErrMigrationRestart = errors.New("agent: reconnect after migration")

// SetConfigPath must be called before Run/RegisterOnce when --config is used.
func (a *Agent) SetConfigPath(path string) { a.configPath = path }

func verifyMigrationTarget(ctx context.Context, client *http.Client, hub, token, nodeID, backupID string) error {
	req, e := http.NewRequestWithContext(ctx, "POST", hub+"/api/v1/migration/node", nil)
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("新面板连接或 HTTPS 验证失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("新面板不接受原节点令牌，继续使用旧面板")
	}
	var out migration.NodeResponse
	if e = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&out); e != nil || out.NodeID == "" || (nodeID != "" && out.NodeID != nodeID) || out.BackupID != backupID {
		return errors.New("新面板节点身份或备份身份不一致")
	}
	return nil
}
func (a *Agent) migrate(cmd model.Command) model.CommandResult {
	res := model.CommandResult{ID: cmd.ID}
	target, e := migration.Target(payloadString(cmd.Payload, "hub"))
	if e != nil {
		res.Stderr = e.Error()
		return res
	}
	id := payloadString(cmd.Payload, "backup_id")
	if len(id) != 64 {
		res.Stderr = "备份身份无效"
		return res
	}
	a.mu.Lock()
	cfg := *a.cfg
	busy := len(a.activeTaskCancels) > 0
	a.mu.Unlock()
	if busy {
		res.Stderr = "有测试任务进行中，稍后自动重试"
		return res
	}
	cfg.Hub = target
	cfg.CAFingerprint = ""
	cfg.Insecure = false
	candidate, e := New(&cfg, a.collector)
	if e != nil {
		res.Stderr = e.Error()
		return res
	}
	defer candidate.client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e = verifyMigrationTarget(ctx, candidate.client, target, cfg.Token, cfg.NodeID, id); e != nil {
		res.Stderr = e.Error()
		return res
	}
	e = saveConfig(a.configPath, func(raw map[string]string) {
		raw["migration_previous_hub"] = a.hub
		raw["migration_previous_fingerprint"] = a.cfg.CAFingerprint
		raw["migration_previous_insecure"] = strconv.FormatBool(a.cfg.Insecure)
		raw["migration_backup_id"] = id
		raw["hub"] = target
		raw["ca_fingerprint"] = ""
		raw["insecure"] = "false"
	})
	if e != nil {
		res.Stderr = "保存迁移配置失败，继续使用旧面板"
		return res
	}
	res.OK = true
	res.Stdout = "新面板已验证，准备重连；首次连接失败将回退旧面板"
	return res
}

// Runs before any background goroutines start: the runtime hub/client remain
// immutable for their entire lifetime. Failed first contact restores old config.
func (a *Agent) resumeMigration(ctx context.Context) error {
	raw, e := loadRaw(a.configPath)
	if e != nil || raw["migration_previous_hub"] == "" {
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	e = verifyMigrationTarget(probeCtx, a.client, a.hub, a.token(), a.cfg.NodeID, raw["migration_backup_id"])
	if e == nil {
		a.migrationPending = true
		return nil
	}
	logger.Printf("迁移首次连接失败，回退旧面板：%v", e)
	old := raw["migration_previous_hub"]
	u, pe := url.Parse(old)
	if pe != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("旧面板回退地址无效")
	}
	if e = saveConfig(a.configPath, func(m map[string]string) {
		m["hub"] = old
		m["ca_fingerprint"] = raw["migration_previous_fingerprint"]
		m["insecure"] = raw["migration_previous_insecure"]
		clearMigrationKeys(m)
	}); e != nil {
		return e
	}
	a.client.CloseIdleConnections()
	a.cfg.Hub = old
	a.cfg.CAFingerprint = raw["migration_previous_fingerprint"]
	a.cfg.Insecure = parseBool(raw["migration_previous_insecure"])
	a.hub = old
	a.hubHost = u.Hostname()
	return a.buildClient()
}
func clearMigrationKeys(raw map[string]string) {
	for _, k := range []string{"migration_previous_hub", "migration_previous_fingerprint", "migration_previous_insecure", "migration_backup_id"} {
		delete(raw, k)
	}
}
func (a *Agent) finishMigration() {
	if !a.migrationPending {
		return
	}
	if e := saveConfig(a.configPath, clearMigrationKeys); e != nil {
		logger.Printf("清理迁移回退标记失败：%v", e)
		return
	}
	a.migrationPending = false
}
