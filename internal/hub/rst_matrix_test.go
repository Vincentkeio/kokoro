package hub

// RST 触发条件的矩阵测试。
//
// 这份文件记录的是**实测结论**，用来防止以后有人「顺手清理」rawPost
// 里那些看起来多余的分步写入、sleep、CloseWrite，把回归测试改成恒绿。
//
// 实测矩阵（在**修复被回退**的构建上跑）：
//
//	┌─────────────┬─────────┬───────────┬────────────────────┐
//	│ 头/body写法 │ 中间等待│ CloseWrite│ 回退后的结果       │
//	├─────────────┼─────────┼───────────┼────────────────────┤
//	│ 一次 write  │    —    │     无│ 小 body 仍可读     │
//	│ 一次 write  │    —    │     有    │ 可读（大 body 会挂）│
//	│ 分两次 write│ 20ms     │     无    │ **可读（被掩盖！）**│
//	│ 分两次 write│    —    │     无    │ **无响应（RST）**  │
//	│ 分两次 write│    —    │     有    │ 可读（被掩盖！）    │
//	└─────────────┴─────────┴───────────┴────────────────────┘
//
// 结论：只有在「头与 body 分两次 write、期间不等待、不半关闭」时，
// 未读请求体才会留在内核接收缓冲区里，RST 才会稳定复现。
//
// 所以本测试反过来断言**修复之后所有变体都能读到 401**——
// 修复的价值恰恰在于让行为不再依赖这些时序细节。

import (
	"bufio"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type rstVariant struct {
	name      string
	split     bool          // 头与 body 是否分两次 write
	wait      time.Duration // 两次 write 之间是否等待
	closeWr   bool          // 发完是否半关闭
	bodyBytes int
}

// rstVariants 覆盖会触发或不触发 RST 的各种时序。
var rstVariants = []rstVariant{
	{"一次write/不等待/不半关闭/小body", false, 0, false, 2048},
	{"分两次write/等待20ms/不半关闭", true, 20 * time.Millisecond, false, 2048},
	{"分两次write/不等待/半关闭", true, 0, true, 2048},
	{"分两次write/不等待/不半关闭", true, 0, false, 2048},
	{"分两次write/不等待/不半关闭/40KB", true, 0, false, 40 << 10},
	{"分两次write/不等待/不半关闭/63KB", true, 0, false, 63 << 10},
}

func rstProbe(t *testing.T, host, path, body string, v rstVariant) (string, string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	head := "POST " + path + " HTTP/1.1\r\nHost: " + host + "\r\n" +
		"Content-Type: application/x-www-form-urlencoded\r\n" +
		"Content-Length: " + strconv.Itoa(len(body)) + "\r\n" +
		"Connection: close\r\n\r\n"

	if v.split {
		io.WriteString(conn, head)
		if v.wait > 0 {
			time.Sleep(v.wait)
		}
		io.WriteString(conn, body)
	} else {
		io.WriteString(conn, head+body)
	}
	if v.closeWr {
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
	}

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		return "", ""
	}
	rest, _ := io.ReadAll(br)
	return strings.TrimSpace(line), string(rest)
}

// TestRSTTriggerMatrix 断言 drainBody 之后，所有时序变体都能读到响应。
//
// 修复前这个矩阵里有相当比例是「无响应」；修复后必须全绿。
// 这比只测一种时序更严格，因为它顺带证明了修复不依赖特定包到达顺序。
func TestRSTTriggerMatrix(t *testing.T) {
	h, _ := newTestHub(t)
	addr := dialHub(t, h)
	host := strings.TrimPrefix(addr, "http://")

	// 并发跑，避免时序互相影响；每个变体一个连接。
	var wg sync.WaitGroup
	type res struct {
		v      rstVariant
		status string
		err    string
	}
	out := make([]res, len(rstVariants))

	for i, v := range rstVariants {
		wg.Add(1)
		go func(i int, v rstVariant) {
			defer wg.Done()
			body := "id=" + strings.Repeat("A", v.bodyBytes)
			status, resp := rstProbe(t, host, "/admin/themes/delete", body, v)
			if status == "" {
				out[i] = res{v, "", "客户端读不到任何响应（连接被重置）"}
				return
			}
			if !strings.Contains(status, " 401 ") {
				out[i] = res{v, status, "状态码不是 401"}
				return
			}
			if !strings.Contains(resp, "未登录") {
				out[i] = res{v, status, "正文不含「未登录」"}
				return
			}
			out[i] = res{v, status, ""}
		}(i, v)
	}
	wg.Wait()

	for _, r := range out {
		if r.err != "" {
			t.Errorf("变体 %q 失败：%s", r.v.name, r.err)
		}
	}
}
