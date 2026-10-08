package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// broker 负责把最新快照推给所有在线浏览器（SSE）。
// 节点数在几百台以内时，全量推送最简单也最可靠，不做增量。
type broker struct {
	mu   sync.RWMutex
	subs map[chan []byte]struct{}

	// latest 缓存最近一次广播内容，新订阅者立刻收到一份，避免白屏等待。
	latest []byte
}

func newBroker() *broker {
	return &broker{subs: make(map[chan []byte]struct{})}
}

func (b *broker) subscribe() chan []byte {
	ch := make(chan []byte, 8)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	last := b.latest
	b.mu.Unlock()
	if len(last) > 0 {
		select {
		case ch <- last:
		default:
		}
	}
	return ch
}

func (b *broker) unsubscribe(ch chan []byte) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
	close(ch)
}

// broadcast 推送一条事件。
func (b *broker) broadcast(event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	msg := []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(data)))

	b.mu.Lock()
	b.latest = msg
	subs := make([]chan []byte, 0, len(b.subs))
	for ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- msg:
		case <-time.After(time.Second):
			// 慢客户端直接跳过，不阻塞其他人
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
	ticker := time.NewTicker(15 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				nodes, err := h.store.ListNodes(true)
				if err != nil {
					log.Printf("[hub] 检查离线状态失败: %v", err)
					continue
				}
				now := time.Now().UnixMilli()
				for _, n := range nodes {
					// 超过 3 个上报周期没消息视为离线
					offline := now-n.LastSeen > 30_000
					if n.Online && offline {
						n.Online = false
						_ = h.store.UpdateNode(&n)
						h.publishStatus(n.ID, false)
					}
				}
			}
		}
	}()
}
