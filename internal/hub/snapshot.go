package hub

import (
	"os"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// Reuse the same Agent snapshot as the public panel. A page request must never
// run a second collector or wait for a CPU sampling interval.
func hostSelfSnapshot(hostname string, nodes []model.Node, snap map[string]*model.Metrics) (string, *model.Metrics, string) {
	var id string
	for _, node := range nodes {
		if hostname == "" || !strings.EqualFold(node.Hostname, hostname) {
			continue
		}
		if id != "" {
			return "", nil, "存在同名主机，无法确定面板主机对应的节点"
		}
		id = node.ID
	}
	if id == "" {
		return "", nil, "面板主机尚未接入 Agent"
	}
	if snap[id] == nil {
		return id, nil, "等待面板主机的 Agent 上报"
	}
	return id, snap[id], ""
}

func (h *Hub) hostSelfSnapshot(nodes []model.Node) (string, *model.Metrics, string) {
	hostname, _ := os.Hostname()
	return hostSelfSnapshot(hostname, nodes, h.latestSnapshot())
}

// snapshotTTL 是"每节点最新指标"的内存缓存时长。
//
// 1 秒：对 1Hz 的前端推送来说，和实时没差别；对数据库来说，
// 把"每个 SSE 连接每秒一次 + 每次页面请求一次"压成"每秒最多一次"。
const snapshotTTL = time.Second

// latestSnapshot 返回每节点最新一条指标，带 1 秒内存缓存。
//
// 为什么缓存：SSE 广播每秒取一次快照，首页与仪表盘每次请求也要，
// 而 metrics_raw 是按秒增长的 —— 这类读不缓存就会一直被高频打到库上
// （Nezha / Komari 的做法都是把"最新态"留在内存里）。取库失败时
// 沿用上一次的快照，宁可旧一秒，也别让首页瞬间变空。
//
// 返回值只读：调用方不得修改 map 或其中 *Metrics 的字段（会被多个
// 请求/连接共享）。
func (h *Hub) latestSnapshot() map[string]*model.Metrics {
	h.snapMu.Lock()
	defer h.snapMu.Unlock()

	now := time.Now()
	if h.snapData != nil && now.Sub(h.snapAt) < snapshotTTL {
		return h.snapData
	}
	snap, err := h.store.LatestSnapshot()
	if err != nil {
		return h.snapData
	}
	h.snapData = snap
	h.snapAt = now
	return snap
}
