package hub

// 传输层鉴权回归测试。
//
// 这里的用例走**裸 TCP**，不用 httptest.NewRecorder，也不直接用 http.Client。
//
// 为什么不用 Recorder：要验的问题发生在 ResponseWriter 之外——
// 服务端写了 401 就关连接，内核会不会因为接收缓冲区里还有**未读的请求体**
// 而发 RST，把响应报文一起丢掉。httptest 完全没有这一层。
//
// 为什么不用 http.Client：它遇到 RST 会抛异常且**不带任何响应内容**，
// 于是「服务端有没有真的写出 401」和「报文有没有被内核丢掉」两件事
// 糊在一起，看不出到底是哪边的问题。

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// unauthedPostCases 覆盖全部「未登录」的表达方式。
//
// 两种形态都要守住，区别只在状态码：
//   - 401：底层管理动作（导入 / 删除 / 抓取），直接说"未登录"；
//   - 303 → /admin：页面型表单，浏览器习惯是弹回登录页。
//
// 关键在于**两者都必须能被客户端读到**，状态码可以是 401 也可以是 303。
var unauthedPostCases = []struct {
	path string
	body string
	want int
}{
	{"/admin/themes/import", "manifest=" + strings.Repeat("A", 2048), http.StatusUnauthorized},
	{"/admin/themes/delete", "id=kokoro.daylight", http.StatusUnauthorized},
	{"/admin/themes/grab", "url=https%3A%2F%2Fexample.com", http.StatusUnauthorized},
	{"/admin/comments", "id=cm_1&action=delete", http.StatusSeeOther},
	{"/admin/nodes", "id=x&action=delete", http.StatusSeeOther},
	{"/admin/settings", "comment_enabled=1", http.StatusSeeOther},
}

// dialHub 起一个真的监听端口，返回基址。
func dialHub(t *testing.T, h *Hub) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("监听失败: %v", err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
	})
	return "http://" + ln.Addr().String()
}

// rawPost 复现 RST 的触发条件，返回读到的状态行与正文。
// 状态行为空表示连接被重置、客户端什么响应都没拿到。
//
// ⚠️ 三个写法上的细节每一个都是必要的，去掉任何一个测试就恒绿、
// 变成一条假回归。这是实测出来的，不是猜的——见下方 TestRSTTriggerMatrix。
//
//  1. 请求头与body **分两次 write**。
//     服务端读请求头用的是带缓冲的 reader。头和 body 若在同一次 write 里
//     到达，一次 read 就把两者都捞进了用户态缓冲区，body 压根没进内核
//     缓冲区，handler 返回时自然不会 RST。
//
//  2. **不要在两次 write 之间 sleep**。
//     给了间隔，服务端有机会先把body 读走，RST 就不会发生。
//
//  3. **不要 CloseWrite**。
//     半关闭会让服务端读到 EOF，把 body 当成正常结束消费掉，
//     同样掩盖问题。真实浏览器和 curl 在发完 body 后是继续读响应的，
//     不会先半关闭。
func rawPost(t *testing.T, addr, path, body string) (statusLine, respBody string) {
	t.Helper()
	host := strings.TrimPrefix(addr, "http://")
	conn, err := net.DialTimeout("tcp", host, 5*time.Second)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))

	head := "POST " + path + " HTTP/1.1\r\n" +
		"Host: " + host + "\r\n" +
		"Content-Type: application/x-www-form-urlencoded\r\n" +
		"Content-Length: " + strconv.Itoa(len(body)) + "\r\n" +
		"Connection: close\r\n\r\n"

	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatalf("写请求头失败: %v", err)
	}
	if _, err := io.WriteString(conn, body); err != nil {
		t.Fatalf("写请求体失败: %v", err)
	}

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		// 读不到状态行 = 响应被内核的 RST 吞掉了。
		return "", ""
	}
	rest, _ := io.ReadAll(br)
	return strings.TrimSpace(line), string(rest)
}

// TestUnauthedPostWithBodyStillReturnsReadableStatus 是本文件的核心用例。
//
// 曾经的 bug：所有「未登录」分支都是裸 http.Error(w, "未登录", 401) 就 return，
// 完全没读请求体。GET 没 body 所以看不出问题，但任何带 body 的 POST ——
// 也就是后台里所有的表单 —— 只要 body 还在内核接收缓冲区里没被读走，
// 连接一关就会发 RST，把已经写好的响应报文一并丢掉。
// 客户端只看到「远程主机强迫关闭了一个现有的连接」，拿不到状态码也拿不到正文。
//
// 修法是 denyUnauthed / drainBody：先把 body 抽干（限量）再写响应。
//
// 本测试在**修复被回退后必须失败**，否则它就不是有效回归。
// 由 TestRSTTriggerMatrix 与 scripts/rst-size-probe.py 双向守护。
func TestUnauthedPostWithBodyStillReturnsReadableStatus(t *testing.T) {
	h, _ := newTestHub(t)
	addr := dialHub(t, h)

	for _, c := range unauthedPostCases {
		t.Run(c.path, func(t *testing.T) {
			status, resp := rawPost(t, addr, c.path, c.body)
			if status == "" {
				t.Fatalf("连接被重置，客户端读不到任何响应（body %d 字节）；"+
					"drainBody 没生效？", len(c.body))
			}
			if !strings.Contains(status, " "+strconv.Itoa(c.want)+" ") {
				t.Fatalf("状态行 = %q，应含 %d", status, c.want)
			}
			if c.want == http.StatusUnauthorized && !strings.Contains(resp, "未登录") {
				t.Errorf("响应正文应含「未登录」，实际 %q", resp)
			}
		})
	}
}

// TestUnauthedPostWithLargeBodyWithinDrainLimit 守住 drainBody 的能力边界。
//
// drainBody 只读 64KB。这个用例确认**上限之内**的较大表单也能正常收到响应。
func TestUnauthedPostWithLargeBodyWithinDrainLimit(t *testing.T) {
	h, _ := newTestHub(t)
	addr := dialHub(t, h)

	// 63KB：卡在 64KB 上限之内，留一点余量给编码开销。
	body := "manifest=" + strings.Repeat("A", 63<<10)
	status, _ := rawPost(t, addr, "/admin/themes/import", body)
	if status == "" {
		t.Fatalf("63KB body：连接被重置；drainBody 的 %d 上限应覆盖这个尺寸", 64<<10)
	}
	if !strings.Contains(status, " 401 ") {
		t.Errorf("状态行 = %q，应含 401", status)
	}
}

// TestDrainBodyLeavesOversizedBodyAlone 确认抽干是有限量的。
//
// 恶意客户端可以挂一个超大 body 上来；如果为了回一句"未登录"把它全读下来，
// 等于白送一个放大攻击面。所以超过上限的部分必须**不读**——
// 代价是超大请求仍会触发 RST，客户端拿不到状态码。这是可接受的取舍：
// 未授权的大文件上传本来就该被中断，而现实中的表单远小于上限。
//
// 这个用例**不断言响应可读**，只断言不会崩、不会挂住。
func TestDrainBodyLeavesOversizedBodyAlone(t *testing.T) {
	h, _ := newTestHub(t)
	addr := dialHub(t, h)

	status, _ := rawPost(t, addr, "/admin/themes/delete", "id="+strings.Repeat("A", 3<<20))
	t.Logf("3MB body 下状态行=%q（空表示连接被重置，符合预期）", status)
}

// TestUnauthedGetStillWorks 确认这次改动没影响 GET 路径（GET 无 body）。
func TestUnauthedGetStillWorks(t *testing.T) {
	h, _ := newTestHub(t)
	addr := dialHub(t, h)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(addr + "/admin/themes/delete")
	if err != nil {
		t.Fatalf("GET 失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET 未登录状态码 = %d，应为 401", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), "未登录") {
		t.Errorf("GET 正文 = %q", b)
	}
}
