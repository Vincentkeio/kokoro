package hub

// 响应 gzip 压缩。
//
// 为什么在应用层做：hub 自己吐静态资源和 HTML（http.FileServer + 模板渲染），
// 前面的 Nginx 没配 gzip —— style.css 裸奔 104KB、详情页 HTML 38KB、
// app.js 49KB，一个详情页首访要拉 190KB+。国内线路下这就是
// "点进详情页卡半天"的主因（boss 2026-10-12 报的）。
// Nginx 配置不在代码里，在应用层补齐最稳，也不用动服务器。
//
// 只压值得压的：
//   - 客户端得先说要（Accept-Encoding: gzip）；
//   - Range 请求放过 —— 206 部分响应被压过后，Range 语义就没了；
//   - SSE 放过 —— 流式响应，压了反而拖慢首帧，且 EventSource 带不了
//     自定义头关不掉它，只能认 Content-Type；
//   - 已经是压缩格式的（图片/音视频/zip/octet-stream）放过 ——
//     agent 二进制下载 15MB，压一遍纯费 1 核小鸡的 CPU；
//   - 小响应（<1KB）放过 —— 压不出几个字节，白费 CPU。

import (
	"compress/gzip"
	"net/http"
	"strings"
)

// gzipMinBody 小于这个长度的响应不压。
const gzipMinBody = 1024

// gzipWrap 给 next 的响应加 gzip。SSE、Range、图片等自动跳过（见包注释）。
func gzipWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !wantsGzip(r) {
			next.ServeHTTP(w, r)
			return
		}
		gw := &gzipResponse{ResponseWriter: w, code: http.StatusOK}
		defer gw.close()
		next.ServeHTTP(gw, r)
	})
}

// wantsGzip 判断这个请求值不值得包一层。
func wantsGzip(r *http.Request) bool {
	if r.Method == http.MethodHead {
		return false // HEAD 没有 body，压了没意义
	}
	if r.Header.Get("Range") != "" {
		return false // 206 部分响应，见包注释
	}
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

// gzipResponse 是"先看看再压"的 ResponseWriter。
//
// 为什么不无脑压：压缩比要到 1KB 以上才划得来（小响应压完反而更大），
// 所以先攒着，第一次 Write 攒够阈值才真正开压；不够就原样吐。
// 代价是 WriteHeader 被推迟到决定的那一刻 —— 对普通响应无感知
// （header 本来就要等第一个字节），SSE/流式在 WriteHeader 就认输直通。
type gzipResponse struct {
	http.ResponseWriter
	gz   *gzip.Writer
	buf  []byte
	code int
	mode int8 // 0=还没定 1=压缩 2=直通（不压）
}

func (g *gzipResponse) WriteHeader(code int) {
	g.code = code
	g.passthroughIfNeeded()
}

// passthroughIfNeeded 还没定去留时，看看 Content-Type 是不是"压了也没用"
// 的那类（SSE / 图片 / 二进制）—— 是就立刻认输直通。
func (g *gzipResponse) passthroughIfNeeded() {
	if g.mode != 0 {
		return
	}
	if skipGzipType(g.Header().Get("Content-Type")) {
		g.mode = 2
		g.ResponseWriter.WriteHeader(g.code)
	}
}

func (g *gzipResponse) Write(b []byte) (int, error) {
	switch g.mode {
	case 1:
		return g.gz.Write(b)
	case 2:
		return g.ResponseWriter.Write(b)
	}
	// 第一把之前也要认一次 SSE/图片：handler 常常 Set 完 Content-Type
	// 直接 Write（隐式 WriteHeader），只查 WriteHeader 会错过它。
	if skipGzipType(g.Header().Get("Content-Type")) {
		g.mode = 2
		g.ResponseWriter.WriteHeader(g.code)
		return g.ResponseWriter.Write(b)
	}
	// 未定：先攒着，够阈值才开压
	g.buf = append(g.buf, b...)
	if len(g.buf) >= gzipMinBody {
		g.startGzip()
	}
	return len(b), nil
}

// Flush 让包装器仍然是 http.Flusher —— SSE handler 第一件事就是强转
// 它（拿不到直接 500，见 sse.go）。压缩流里 Flush 要先把 gzip 的缓冲
// 推下去，否则客户端收到的是半截流；还没决定压不压时，Flush 就是
// "现在就要发" 的信号，直接按直通吐出去。
func (g *gzipResponse) Flush() {
	switch g.mode {
	case 1:
		_ = g.gz.Flush()
	case 0:
		g.mode = 2
		g.ResponseWriter.WriteHeader(g.code)
		if len(g.buf) > 0 {
			_, _ = g.ResponseWriter.Write(g.buf)
			g.buf = nil
		}
	}
	if f, ok := g.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// startGzip 决定压缩：补写 header、开压缩流、把攒着的一把写进去。
func (g *gzipResponse) startGzip() {
	g.mode = 1
	h := g.Header()
	h.Set("Content-Encoding", "gzip")
	h.Del("Content-Length") // 长度变了，原值必须作废
	g.ResponseWriter.WriteHeader(g.code)
	g.gz = gzip.NewWriter(g.ResponseWriter)
	_, _ = g.gz.Write(g.buf)
	g.buf = nil
}

// close 收尾：未够阈值的小响应原样补写；开过压的要把 gzip 流收干净
// （不 Close 的话尾部 CRC/length 缺失，浏览器会当损坏）。
func (g *gzipResponse) close() {
	switch g.mode {
	case 1:
		_ = g.gz.Close()
	case 0:
		g.ResponseWriter.WriteHeader(g.code)
		if len(g.buf) > 0 {
			_, _ = g.ResponseWriter.Write(g.buf)
		}
	}
}

// staticCacheWrap 给 /static/ 加长期缓存头。
//
// 为什么必须加：http.FileServer 默认**一个缓存头都不吐**
// （没有 Cache-Control / ETag / Last-Modified）—— 浏览器连启发式
// 缓存都没得用，每翻一个页面都全量重下静态资源（gzip 后仍有约
// 48KB：style.css 30KB + app.js 18KB + globe.js/land.bin）。
// 这是 2026-10-12 排查"用了代理还卡"时顺带挖出来的：就算直连，
// 每个页面也都在为同一批字节重复付费。
//
// 安全性：URL 带 ?v=N（staticAssetURL 拼的，改内容必须 bump），
// immutable 不会把用户卡在旧文件上 —— 这正是注释里要求的"改了
// CSS/JS 必须 +1"的兑现处。
func staticCacheWrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		next.ServeHTTP(w, r)
	})
}

// skipGzipType 这些 Content-Type 压了没收益（甚至有害）。
func skipGzipType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if ct == "" {
		return false
	}
	for _, p := range []string{
		"text/event-stream", // SSE：流式，见包注释
		"image/", "audio/", "video/",
		"application/octet-stream", "application/zip", "application/gzip",
		"application/pdf", "font/", "application/wasm",
	} {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	return false
}
