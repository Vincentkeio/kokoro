package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// Persist before executing. A crash leaves an interrupted record, never a
// reason to silently run the benchmark again. Results survive network outages.
type taskRecord struct {
	Command model.Command        `json:"command"`
	Result  *model.CommandResult `json:"result,omitempty"`
}

func (a *Agent) taskJournalDir() string {
	scope := sha256.Sum256([]byte(a.hub + "|" + a.token()))
	return filepath.Join(filepath.Dir(a.configPath), "task-results", hex.EncodeToString(scope[:8]))
}

func (a *Agent) taskRecordPath(id string) string {
	digest := sha256.Sum256([]byte(id))
	return filepath.Join(a.taskJournalDir(), hex.EncodeToString(digest[:])+".json")
}

func (a *Agent) writeTaskRecord(record *taskRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	path := a.taskRecordPath(record.Command.ID)
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

func (a *Agent) initTaskJournal() error {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	a.taskRecords = map[string]*taskRecord{}
	a.taskCompleted = map[string]bool{}
	a.taskResultWake = make(chan struct{}, 1)
	dir := a.taskJournalDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var record taskRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return fmt.Errorf("任务记录损坏：%w", err)
		}
		if record.Command.ID == "" || (record.Command.Type != "test" && record.Command.Type != "swap") {
			continue
		}
		if record.Result == nil {
			record.Result = &model.CommandResult{ID: record.Command.ID, ExitCode: -1,
				Stderr: "Agent 重启时任务尚未完成，本次操作已中断；请检查状态后重试"}
			if err := a.writeTaskRecord(&record); err != nil {
				return err
			}
		}
		a.taskRecords[record.Command.ID] = &record
	}
	return nil
}

func (a *Agent) acceptTask(cmd model.Command) bool {
	if cmd.Type != "test" && cmd.Type != "swap" {
		return false
	}
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	if a.taskRecords[cmd.ID] != nil || a.taskCompleted[cmd.ID] {
		return false
	}
	record := &taskRecord{Command: cmd}
	if err := a.writeTaskRecord(record); err != nil {
		record.Result = &model.CommandResult{ID: cmd.ID, Stderr: "任务无法保存到本机，未执行：" + err.Error()}
		a.taskRecords[cmd.ID] = record
		a.wakeTaskResults()
		return false
	}
	a.taskRecords[cmd.ID] = record
	return true
}

func (a *Agent) saveTaskResult(result model.CommandResult) {
	a.taskMu.Lock()
	record := a.taskRecords[result.ID]
	if record != nil {
		record.Result = &result
		if err := a.writeTaskRecord(record); err != nil {
			logger.Printf("保存任务 %s 结果失败，保留内存结果待回传：%v", result.ID, err)
		}
	}
	a.taskMu.Unlock()
	a.wakeTaskResults()
}

func (a *Agent) wakeTaskResults() {
	select {
	case a.taskResultWake <- struct{}{}:
	default:
	}
}

func (a *Agent) taskHeartbeat() *model.TaskHeartbeat {
	a.taskMu.Lock()
	defer a.taskMu.Unlock()
	ids := make([]string, 0, len(a.taskRecords))
	var results []string
	for id, record := range a.taskRecords {
		ids = append(ids, id)
		if record.Result != nil {
			results = append(results, id)
		}
	}
	sort.Strings(ids)
	sort.Strings(results)
	return &model.TaskHeartbeat{IDs: ids, ResultIDs: results}
}

func (a *Agent) taskResultLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	a.wakeTaskResults()
	for {
		select {
		case <-ctx.Done():
			return
		case <-a.cmdDone:
			return
		case <-a.taskResultWake:
		case <-ticker.C:
		}
		a.flushTaskResults()
	}
}

func (a *Agent) flushTaskResults() {
	a.taskMu.Lock()
	var results []model.CommandResult
	for _, record := range a.taskRecords {
		if record.Result != nil {
			results = append(results, *record.Result)
		}
	}
	a.taskMu.Unlock()
	for _, result := range results {
		if err := a.postResult(result); err != nil {
			logger.Printf("回传任务 %s 结果失败，已保留并将重试：%v", result.ID, err)
			continue
		}
		a.taskMu.Lock()
		if err := os.Remove(a.taskRecordPath(result.ID)); err != nil && !os.IsNotExist(err) {
			logger.Printf("清理已回传测试 %s 记录失败：%v", result.ID, err)
		} else {
			delete(a.taskRecords, result.ID)
			if len(a.taskCompleted) >= 256 {
				a.taskCompleted = map[string]bool{}
			}
			a.taskCompleted[result.ID] = true
			logger.Printf("任务 %s 结果已确认保存到 Hub", result.ID)
		}
		a.taskMu.Unlock()
		a.mu.Lock()
		delete(a.cancelledTasks, result.ID)
		a.mu.Unlock()
	}
}
