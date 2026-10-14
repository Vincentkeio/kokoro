package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// broker 负责把最新快照推给所有在线浏览器（SSE）。
// 节点数在几百台以内时，全量推送最简单也最可靠，不做增量。
type broker struct {
	mu   sync.RWMutex
	subs map[chan []byte]struct{}
	// adminSubs carries private rendered task-history fragments only to the
	// authenticated admin stream. Never send task output over the public stream.
	adminSubs map[string]map[chan []byte]struct{}

	// latest 只缓存指标快照给新订阅者。任务卡片等事件是增量更新，不能
	// 回放给新打开的页面，否则旧的“测试中”状态会覆盖服务端当前状态。
	latest   []byte
	statuses map[string]bool
}

func newBroker() *broker {
	return &broker{
		subs:      make(map[chan []byte]struct{}),
		adminSubs: make(map[string]map[chan []byte]struct{}),
		statuses:  make(map[string]bool),
	}
}

func (b *broker) subscribe() chan []byte {
	ch := make(chan []byte, 8)
	b.mu.Lock()
	// 在同一把锁里先放入缓存事件和当前状态，再登记订阅者；这样并发
	// 状态变化只会排在快照后，不会让旧事件覆盖重连时的真实状态。
	if len(b.latest) > 0 {
		select {
		case ch <- b.latest:
		default:
		}
	}
	if len(b.statuses) > 0 {
		if data, err := json.Marshal(b.statuses); err == nil {
			ch <- []byte(fmt.Sprintf("event: node_status\ndata: %s\n\n", data))
		}
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *broker) unsubscribe(ch chan []byte) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
	// Broadcast takes a subscriber snapshot before sending. Closing here races
	// with that snapshot and can panic when any public client disconnects.
}

func (b *broker) subscribeAdmin(nodeID string) chan []byte {
	ch := make(chan []byte, 8)
	b.mu.Lock()
	if b.adminSubs[nodeID] == nil {
		b.adminSubs[nodeID] = make(map[chan []byte]struct{})
	}
	b.adminSubs[nodeID][ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *broker) unsubscribeAdmin(nodeID string, ch chan []byte) {
	b.mu.Lock()
	if subs := b.adminSubs[nodeID]; subs != nil {
		delete(subs, ch)
		if len(subs) == 0 {
			delete(b.adminSubs, nodeID)
		}
	}
	b.mu.Unlock()
}

func (b *broker) broadcastAdmin(nodeID, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, data))

	b.mu.RLock()
	subs := make([]chan []byte, 0, len(b.adminSubs[nodeID]))
	for ch := range b.adminSubs[nodeID] {
		subs = append(subs, ch)
	}
	b.mu.RUnlock()

	for _, ch := range subs {
		select {
		case ch <- msg:
		case <-time.After(time.Second):
			// A slow admin tab must not delay task processing or other clients.
		}
	}
}

// broadcast 推送一条事件。
func (b *broker) broadcast(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(data)))

	b.mu.Lock()
	switch event {
	case "node_status":
		var statuses map[string]bool
		if json.Unmarshal(data, &statuses) == nil {
			b.statuses = statuses
		}
	case "node_online", "node_offline":
		var status struct {
			ID     string `json:"id"`
			Online bool   `json:"online"`
		}
		if json.Unmarshal(data, &status) == nil && status.ID != "" {
			b.statuses[status.ID] = status.Online
		}
	}
	if event == "metrics" {
		b.latest = msg
	}
	subs := make([]chan []byte, 0, len(b.subs))
	for ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- msg:
		default:
			// The next metrics snapshot replaces this one; slow visitors cannot
			// hold up report processing or broadcasts to other clients.
		}
	}
}

// handleStream 是 SSE 端点：GET /api/v1/stream
func (h *Hub) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // 让 Nginx 不要缓冲

	ch := h.broker.subscribe()
	defer h.broker.unsubscribe(ch)

	// 先发一个注释帧，穿透可能的代理缓冲
	fmt.Fprint(w, ": kokoro stream\n\n")
	flusher.Flush()

	ctx := r.Context()
	ticker := time.NewTicker(15 * time.Second) // 心跳，防止中间层掐连接
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-h.done:
			// 服务端要关了。http.Server.Shutdown 不会取消请求的 ctx，
			// 所以必须靠这个信号主动退出——否则 Shutdown 每次都等到
			// deadline，正常重启被记成 status=1/FAILURE，还白等 5 秒。
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if !h.adminAuthed(r) {
				msg = h.publicStreamMessage(r, msg)
				if len(msg) == 0 {
					continue
				}
			}
			if _, err := w.Write(msg); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// Visibility is enforced on the wire, including cached snapshots and later
// updates. Knowing an unlisted node's slug grants the same read access as its
// detail URL, never access to private nodes or administrative notifications.
func (h *Hub) publicStreamMessage(r *http.Request, msg []byte) []byte {
	parts := strings.SplitN(strings.TrimSpace(string(msg)), "\ndata: ", 2)
	if len(parts) != 2 {
		return nil
	}
	event := strings.TrimPrefix(parts[0], "event: ")
	if event == "task_status" {
		return nil
	}
	nodes, err := h.store.ListNodes(false)
	if err != nil {
		return nil
	}
	allowed := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		allowed[node.ID] = true
	}
	if slug := r.URL.Query().Get("node"); slug != "" {
		if node, err := h.store.GetNodeBySlug(slug); err == nil && node != nil && node.Visibility == model.VisibilityUnlisted {
			allowed[node.ID] = true
		}
	}
	var data map[string]json.RawMessage
	if json.Unmarshal([]byte(parts[1]), &data) != nil {
		return nil
	}
	switch event {
	case "metrics", "node_status":
		for id := range data {
			if !allowed[id] {
				delete(data, id)
			}
		}
	case "task", "netq", "node_online", "node_offline":
		var id string
		key := "id"
		if event == "task" || event == "netq" {
			key = "node"
		}
		if json.Unmarshal(data[key], &id) != nil || !allowed[id] {
			return nil
		}
	default:
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	return []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, raw))
}

// handleAdminTaskStream streams task-history fragments to an authenticated
// admin viewing one node. The public metrics stream must never expose task
// stdout/stderr, which can contain host details.
func (h *Hub) handleAdminTaskStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	nodeID := strings.TrimSpace(r.URL.Query().Get("node"))
	global := r.URL.Query().Get("all") == "1"
	if nodeID == "" && !global {
		http.Error(w, "missing node", http.StatusBadRequest)
		return
	}
	if nodeID != "" {
		if _, err := h.store.GetNode(nodeID); err != nil {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	streamKey := nodeID
	if global {
		streamKey = "*"
	}
	ch := h.broker.subscribeAdmin(streamKey)
	defer h.broker.unsubscribeAdmin(streamKey, ch)
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	fmt.Fprint(w, ": kokoro admin task stream\n\n")
	if global {
		data, _ := json.Marshal(map[string]string{"html": h.renderAdminTaskRows()})
		fmt.Fprintf(w, "event: task_list\ndata: %s\n\n", data)
	} else if html, ok := h.renderTaskHistory(nodeID); ok {
		data, _ := json.Marshal(map[string]string{"node": nodeID, "html": html})
		fmt.Fprintf(w, "event: task_history\ndata: %s\n\n", data)
	}
	flusher.Flush()

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-h.done:
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, err := w.Write(msg); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// publishSnapshot 在收到上报后调用，向浏览器推送全量快照。
func (h *Hub) publishSnapshot(snap map[string]*model.Metrics) {
	h.broker.broadcast("metrics", snap)
}

func (h *Hub) publishStatus(nodeID string, online bool) {
	event := "node_offline"
	if online {
		event = "node_online"
	}
	h.broker.broadcast(event, map[string]any{"id": nodeID, "online": online})
}

// startBroadcastLoop 每秒向浏览器推一次全量快照。
// 与上报解耦：小鸡上报再频繁，浏览器侧也只有 1Hz，省带宽也省 CPU。
func (h *Hub) startBroadcastLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap, err := h.store.LatestSnapshot()
			if err != nil {
				log.Printf("[hub] 取快照失败: %v", err)
				continue
			}
			if len(snap) == 0 {
				continue
			}
			h.publishSnapshot(snap)
		}
	}
}

// startStatusWatch 定期检查离线节点并推送状态变化。
func (h *Hub) startStatusWatch(ctx context.Context) {
	const offlineAfter = 20 * time.Second
	check := func() {
		nodes, err := h.store.ListNodes(true)
		if err != nil {
			log.Printf("[hub] 检查离线状态失败: %v", err)
			return
		}
		now := time.Now().UnixMilli()
		statuses := make(map[string]bool, len(nodes))
		for _, n := range nodes {
			// 连续 20 秒没有收到 Hub 端确认的新上报才判离线，避免
			// 单次丢包或短暂抖动造成状态跳变。
			age := now - n.LastSeen
			// 兼容旧版本用 VPS 本地时间记录 LastSeen 的数据：未来时间
			// 超过宽限值也视为过期；新版本上报会改用 Hub 接收时间。
			offline := n.LastSeen <= 0 || age > offlineAfter.Milliseconds() || age < -offlineAfter.Milliseconds()
			if n.Online && offline {
				n.Online = false
				_ = h.store.UpdateNode(&n)
				h.publishStatus(n.ID, false)
				h.broadcastNodeTaskConnection(n.ID, false)
				h.broadcastTaskCard(n.ID)
			}
			statuses[n.ID] = n.Online && !offline
		}
		h.broker.broadcast("node_status", statuses)
	}
	// 在 HTTP 开始接收连接前先填好状态缓存，EventSource 重连时就能拿
	// 到当前状态，而不是只收到最后一份可能过期的指标快照。
	check()
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check()
			}
		}
	}()
}
