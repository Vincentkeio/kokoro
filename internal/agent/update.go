package agent

import (
	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/panelupdate"
	"path/filepath"
)

func (a *Agent) updateStatePath() string {
	p, _ := filepath.Abs(filepath.Join(filepath.Dir(a.configPath), "web-update.json"))
	return p
}
func (a *Agent) updateHeartbeat() *panelupdate.State {
	s, e := panelupdate.Load(a.updateStatePath())
	if e != nil {
		return nil
	}
	return &s
}
func (a *Agent) upgrade(cmd model.Command) model.CommandResult {
	res := model.CommandResult{ID: cmd.ID}
	target := payloadString(cmd.Payload, "version")
	if !panelupdate.ValidID(cmd.ID) || !panelupdate.ValidTag(target) {
		res.Stderr = "更新请求无效"
		return res
	}
	if target == Version {
		res.OK = true
		res.Stdout = "探针已是最新版本"
		return res
	}
	config, e := filepath.Abs(a.configPath)
	if e != nil {
		res.Stderr = "配置路径无效"
		return res
	}
	e = panelupdate.Launch(panelupdate.Spec{Role: "agent", Target: target, ID: cmd.ID, StatePath: a.updateStatePath(), ConfigPath: config})
	if e != nil {
		res.Stderr = e.Error()
		return res
	}
	res.OK = true
	res.Stdout = "已启动独立更新进程"
	return res
}
