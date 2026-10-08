package hub

// 优雅关闭的回归测试。
//
// 背景：`http.Server.Shutdown` **不会取消请求的 context**，它只停止接受
// 新连接、然后等已有 handler 返回。而 SSE 的循环等的是
// `r.Context().Done()`——那个只在**客户端断开**时才触发。
//
// 结果就是：每次重启都必然等到 Shutdown 自己的 deadline，
// 一次完全正常的重启在 systemd 里被记成 `status=1/FAILURE`，
// 而且每次都白等满 5 秒。线上每部署一次就看见一次 FAILURE。
//
// 修法是 Hub 上一个 done channel：开始关闭时先 close 它，
// 让 SSE handler 主动 return，Shutdown 就能立刻干净收尾。
//
// 这条测试守的就是「SSE 会被服务端主动断开」这个行为本身。
// 如果以后有人把 `case <-h.done:` 那个分支删掉，这里会挂。

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestShutdownSignalEndsSSE 确认服务端关闭信号能让 SSE 流主动结束。
func TestShutdownSignalEndsSSE(t *testing.T) {
	h, _ := newTestHub(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/stream", nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("连 SSE 失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("SSE 状态码 = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}

	br := bufio.NewReader(resp.Body)
	// 先确认流真的建立了（握手注释帧）。
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("读首个帧失败: %v", err)
	}
	if !strings.Contains(line, "kokoro stream") {
		t.Fatalf("首个帧 = %q", line)
	}

	// 流建立了，现在模拟服务端开始关闭。
	start := time.Now()
	h.signalDone()

	// 等流自己结束。若关闭信号没接上，这里会一直阻塞到读超时。
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := br.ReadString('\n'); err != nil {
				done <- err
				return
			}
		}
	}()

	select {
	case <-done:
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("关闭信号后 %v 才断开，太慢（可能没接上信号）", d)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("发出关闭信号 4 秒后 SSE 仍未断开——" +
			"h.done 分支丢了？这会让 Shutdown 每次都超时，重启被记成 FAILURE")
	}
}

// TestSignalDoneIsIdempotent 重复发关闭信号不能 panic（close 两次会 panic）。
func TestSignalDoneIsIdempotent(t *testing.T) {
	h, _ := newTestHub(t)
	h.signalDone()
	h.signalDone()
	h.signalDone()
	select {
	case <-h.done:
	default:
		t.Error("signalDone 之后 done 应处于已关闭状态")
	}
}
