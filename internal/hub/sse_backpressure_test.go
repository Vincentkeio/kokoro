package hub

import (
	"strings"
	"testing"
	"time"
)

func TestSlowAdminSubscribersDoNotBlockTaskReports(t *testing.T) {
	b := newBroker()
	var subs []chan []byte
	for i := 0; i < 3; i++ {
		ch := b.subscribeAdmin("node")
		for j := 0; j < cap(ch); j++ {
			ch <- []byte("old")
		}
		subs = append(subs, ch)
	}
	done := make(chan struct{})
	go func() { b.broadcastAdmin("node", "task_history", map[string]string{"html": "latest"}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("慢后台标签页阻塞任务处理")
	}
	for _, ch := range subs {
		found := false
		for len(ch) > 0 {
			if strings.Contains(string(<-ch), "latest") {
				found = true
			}
		}
		if !found {
			t.Fatal("后台应收到最新完整任务快照")
		}
	}
}
