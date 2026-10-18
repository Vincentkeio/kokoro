package hub

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

const maxManagedSwapSizeMB = 512 * 1024
const maxSwapMetricsAge = 30 * time.Second

func swapGiBToMiB(raw string) (int, error) {
	gb, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(gb) || math.IsInf(gb, 0) {
		return 0, fmt.Errorf("SWAP 容量格式无效")
	}
	if gb < 0.5 || gb > 512 || math.Abs(gb*2-math.Round(gb*2)) > 1e-9 {
		return 0, fmt.Errorf("SWAP 容量须为 0.5 到 512 GiB 之间的 0.5 GiB 倍数")
	}
	return int(math.Round(gb * 1024)), nil
}

// DispatchSwap queues a fixed, bounded SWAP operation for one node.
func (h *Hub) DispatchSwap(nodeID string, sizeMB int) (*model.NodeTask, error) {
	if sizeMB < 128 || sizeMB > maxManagedSwapSizeMB {
		return nil, fmt.Errorf("SWAP 容量不在允许范围内")
	}
	node, err := h.store.GetNode(nodeID)
	if err != nil || node == nil {
		return nil, fmt.Errorf("节点不存在")
	}
	latest, err := h.store.LatestMetrics(node.ID)
	if err != nil {
		return nil, fmt.Errorf("读取当前 SWAP 容量失败：%w", err)
	}
	if latest == nil || latest.Mem.SwapTotal <= 0 || latest.Ts <= 0 || time.Since(time.UnixMilli(latest.Ts)) > maxSwapMetricsAge || time.Until(time.UnixMilli(latest.Ts)) > maxSwapMetricsAge {
		return nil, fmt.Errorf("无法确认节点当前 SWAP 容量：请等节点重新上报后再试，未创建任务")
	}
	if int64(sizeMB)*1024*1024 < latest.Mem.SwapTotal {
		return nil, fmt.Errorf("不能执行：目标 SWAP 容量小于当前容量（当前约 %.1f GiB）", float64(latest.Mem.SwapTotal)/(1024*1024*1024))
	}
	tasks, err := h.store.ListTasks(node.ID, 50)
	if err != nil {
		return nil, fmt.Errorf("读取节点任务失败：%w", err)
	}
	for _, old := range tasks {
		if old.Kind == "swap" && (old.Status == model.TaskQueued || old.Status == model.TaskRunning) {
			return nil, fmt.Errorf("该节点已有增加 SWAP 任务，请等它完成后再试")
		}
	}
	t := &model.NodeTask{
		NodeID: node.ID,
		Kind:   "swap",
		Title:  "增加 SWAP",
		Cmd:    fmt.Sprintf("swap:%d", sizeMB),
		Status: model.TaskQueued,
	}
	if err := h.store.CreateTask(t); err != nil {
		return nil, err
	}
	_ = h.store.AddEvent(node.ID, "task", fmt.Sprintf("已排入增加 SWAP：%s", swapSizeLabel(sizeMB)), t.ID)
	h.broadcastTaskProgress(t.ID)
	return t, nil
}

func swapSizeLabel(sizeMB int) string {
	return fmt.Sprintf("%.1f GiB", float64(sizeMB)/1024)
}
