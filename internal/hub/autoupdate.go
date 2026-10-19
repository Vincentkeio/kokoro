package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/panelupdate"
)

const (
	autoUpdateEnabledKey    = "auto_update_enabled"
	autoUpdateLastCheckKey  = "auto_update_last_check"
	autoUpdateNextCheckKey  = "auto_update_next_check"
	autoUpdateLastResultKey = "auto_update_last_result"
	autoUpdateLastErrorKey  = "auto_update_last_error"
	autoUpdatePendingHubKey = "auto_update_pending_hub"
	autoUpdatePendingMaxAge = 24 * time.Hour
)

var autoUpdateTimezone = time.FixedZone("Asia/Shanghai", 8*60*60)

func (h *Hub) autoUpdateEnabled() bool {
	value, err := h.store.GetSetting(autoUpdateEnabledKey)
	return err == nil && (value == "" || value == "1")
}

func (h *Hub) wakeAutoUpdateLoop() {
	select {
	case h.autoUpdateWake <- struct{}{}:
	default:
	}
}

func (h *Hub) autoUpdateSnapshot() map[string]any {
	enabled := h.autoUpdateEnabled()
	lastRaw, _ := h.store.GetSetting(autoUpdateLastCheckKey)
	lastCheck, _ := strconv.ParseInt(lastRaw, 10, 64)
	result, _ := h.store.GetSetting(autoUpdateLastResultKey)
	lastError, _ := h.store.GetSetting(autoUpdateLastErrorKey)
	var nextCheck int64
	if enabled {
		nextRaw, _ := h.store.GetSetting(autoUpdateNextCheckKey)
		nextCheck, _ = strconv.ParseInt(nextRaw, 10, 64)
		if nextCheck <= time.Now().UnixMilli() {
			nextCheck = nextAutoUpdateCheck(time.Now()).UnixMilli()
		}
	}
	return map[string]any{
		"auto_update_enabled":     enabled,
		"auto_update_last_check":  lastCheck,
		"auto_update_next_check":  nextCheck,
		"auto_update_last_result": result,
		"auto_update_last_error":  lastError,
	}
}

func (h *Hub) saveAutoUpdateResult(checkedAt time.Time, result, updateErr string) {
	if err := h.store.SetSettings(map[string]string{
		autoUpdateLastCheckKey:  strconv.FormatInt(checkedAt.UnixMilli(), 10),
		autoUpdateLastResultKey: result,
		autoUpdateLastErrorKey:  updateErr,
	}); err != nil {
		log.Printf("[update] 无法保存自动更新结果: %v", err)
	}
}

// Schedule daily at 01:00 UTC+8, independent of the host/browser timezone.
// A process starting after a missed schedule waits for the next 01:00 instead
// of unexpectedly installing updates during the day. Pending Hub handoffs from
// an already started nightly update are still resumed after a process restart.
func (h *Hub) autoUpdateLoop(ctx context.Context) {
	var scheduled time.Time
	for {
		if ctx.Err() != nil {
			return
		}
		if !h.autoUpdateEnabled() {
			scheduled = time.Time{}
			select {
			case <-ctx.Done():
				return
			case <-h.autoUpdateWake:
				continue
			}
		}
		if scheduled.IsZero() {
			nextRaw, _ := h.store.GetSetting(autoUpdateNextCheckKey)
			nextMS, _ := strconv.ParseInt(nextRaw, 10, 64)
			scheduled = time.UnixMilli(nextMS)
			now := time.Now()
			if !scheduled.After(now) || scheduled.After(now.Add(24*time.Hour)) {
				scheduled = nextAutoUpdateCheck(now)
				if err := h.store.SetSetting(autoUpdateNextCheckKey, strconv.FormatInt(scheduled.UnixMilli(), 10)); err != nil {
					log.Printf("[update] 无法保存下次自动检查时间: %v", err)
				}
			}
		}
		pendingHub := h.resumeAutoHubUpdate(ctx)
		wait := time.Until(scheduled)
		if wait > 0 {
			if pendingHub && wait > 5*time.Second {
				wait = 5 * time.Second
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-h.autoUpdateWake:
				timer.Stop()
				scheduled = time.Time{}
				continue
			case <-timer.C:
			}
			continue
		}
		if !h.autoUpdateEnabled() {
			continue
		}
		if err := h.runAutoUpdateCheck(ctx); err != nil {
			log.Printf("[update] 自动更新失败: %v", err)
		}
		// Keep an in-memory deadline too, so even a failed database write
		// cannot turn one scheduled attempt into a tight retry loop.
		scheduled = nextAutoUpdateCheck(time.Now())
	}
}

func nextAutoUpdateCheck(now time.Time) time.Time {
	local := now.In(autoUpdateTimezone)
	next := time.Date(local.Year(), local.Month(), local.Day(), 1, 0, 0, 0, autoUpdateTimezone)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (h *Hub) runAutoUpdateCheck(parent context.Context) error {
	if !h.autoUpdateMu.TryLock() {
		return nil
	}
	defer h.autoUpdateMu.Unlock()
	if !h.autoUpdateEnabled() {
		return nil
	}
	checkedAt := time.Now()
	// Reserve today's check before network or update work. It prevents repeated
	// attempts after an update restarts the Hub or GitHub rate-limits the host.
	if err := h.store.SetSettings(map[string]string{
		autoUpdateLastCheckKey:  strconv.FormatInt(checkedAt.UnixMilli(), 10),
		autoUpdateNextCheckKey:  strconv.FormatInt(nextAutoUpdateCheck(checkedAt).UnixMilli(), 10),
		autoUpdateLastResultKey: "正在检查更新",
		autoUpdateLastErrorKey:  "",
	}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	release, err := panelupdate.Latest(ctx)
	if err != nil {
		h.saveAutoUpdateResult(checkedAt, "", err.Error())
		return err
	}
	nodes, err := h.store.ListNodes(true)
	if err != nil {
		h.saveAutoUpdateResult(checkedAt, "", "无法读取节点")
		return err
	}
	availability, err := h.evaluateUpdateAvailability(ctx, release, nodes, time.Now().UnixMilli())
	if err != nil {
		h.saveAutoUpdateResult(checkedAt, "", err.Error())
		return err
	}
	if !h.transferMu.TryLock() {
		err = errors.New("备份或恢复正在进行，本次自动更新已跳过")
		h.saveAutoUpdateResult(checkedAt, "", err.Error())
		return err
	}
	defer h.transferMu.Unlock()
	if !h.autoUpdateEnabled() || ctx.Err() != nil {
		h.saveAutoUpdateResult(checkedAt, "已停止自动更新", "")
		return nil
	}
	if h.restoreQueued || h.updateBusy() {
		err = errors.New("面板正在恢复或更新，本次自动更新已跳过")
		h.saveAutoUpdateResult(checkedAt, "", err.Error())
		return err
	}
	// Refresh online status just before queueing. A slow release lookup must
	// never make an already offline node eligible for an automatic command.
	nodes, err = h.store.ListNodes(true)
	if err != nil {
		h.saveAutoUpdateResult(checkedAt, "", "无法读取节点")
		return err
	}
	var started, failures []string
	if availability.agentCount > 0 {
		count, err := h.queueAgentUpdatePlan(release.Tag, nodes, availability.newerByVersion, time.Now().UnixMilli())
		if err != nil {
			failures = append(failures, err.Error())
		} else if count > 0 {
			started = append(started, "在线探针")
		}
	}
	if availability.hubNewer {
		pending, _ := json.Marshal(autoHubUpdate{Target: release.Tag, PlannedAt: checkedAt.UnixMilli()})
		if err = h.store.SetSetting(autoUpdatePendingHubKey, string(pending)); err != nil {
			failures = append(failures, err.Error())
		} else {
			started = append(started, "面板（探针任务结束后更新）")
		}
	}
	result := "无可用更新"
	if len(started) > 0 {
		result = "已开始更新" + strings.Join(started, "和")
	} else if availability.onlineCount == 0 && !availability.hubNewer {
		result = "面板已是最新；没有在线探针"
	}
	if len(failures) > 0 {
		err = errors.New(strings.Join(failures, "；"))
		if len(started) == 0 {
			result = "自动更新未启动"
		}
		h.saveAutoUpdateResult(checkedAt, result, err.Error())
		return err
	}
	h.saveAutoUpdateResult(checkedAt, result, "")
	log.Printf("[update] 自动检查完成：目标 %s；%s", release.Tag, result)
	return nil
}

type autoHubUpdate struct {
	Target    string `json:"target"`
	PlannedAt int64  `json:"planned_at"`
}

// A pending Hub update is durable, and is only launched after all dispatched
// agent jobs have completed, failed, or timed out. Clearing the pending marker
// before launch also ensures a restart cannot submit the same Hub job again.
func (h *Hub) resumeAutoHubUpdate(ctx context.Context) bool {
	raw, err := h.store.GetSetting(autoUpdatePendingHubKey)
	if err != nil || raw == "" || ctx.Err() != nil || !h.autoUpdateEnabled() {
		return false
	}
	var pending autoHubUpdate
	if json.Unmarshal([]byte(raw), &pending) != nil || !panelupdate.ValidTag(pending.Target) || pending.Target == Version || time.Now().UnixMilli()-pending.PlannedAt > autoUpdatePendingMaxAge.Milliseconds() {
		_ = h.store.SetSetting(autoUpdatePendingHubKey, "")
		return false
	}
	if h.agentUpdatesBusy() || !h.transferMu.TryLock() {
		return true
	}
	defer h.transferMu.Unlock()
	if h.restoreQueued || h.updateBusy() || h.agentUpdatesBusy() {
		return true
	}
	if !h.autoUpdateEnabled() || ctx.Err() != nil {
		return false
	}
	if current, err := h.store.GetSetting(autoUpdatePendingHubKey); err != nil || current != raw {
		return false
	}
	if err := h.store.SetSettings(map[string]string{
		autoUpdatePendingHubKey: "",
		autoUpdateLastResultKey: "已提交面板自动更新",
	}); err != nil {
		return true
	}
	if err := h.launchHubUpdate(pending.Target); err != nil {
		_ = h.store.SetSettings(map[string]string{autoUpdateLastResultKey: "面板自动更新未启动", autoUpdateLastErrorKey: err.Error()})
		log.Printf("[update] 面板自动更新未能启动: %v", err)
	}
	return false
}
