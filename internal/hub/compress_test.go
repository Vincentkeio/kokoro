package hub

// gzip 压缩：静态资源与 HTML 要压，SSE / Range / 二进制 / 小响应不能压。
//
// 这条防线为什么存在：2026-10-12 boss 报"点进详情页卡半天"，
// 根因是 Nginx 那层没配压缩，一个详情页首访要拉近 190KB 裸内容。

import (
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// getGzip 带 Accept-Encoding: gzip 请求一次，返回响应记录。
func getGzip(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// readEmbedded 读 embed 进二进制的静态文件原文（压缩对比基准）。
func readEmbedded(name string) (string, error) {
	b, err := fs.ReadFile(staticFS, name)
	return string(b), err
}

// gunzip 解压 body，失败即测试失败。
func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(strings.NewReader(string(b)))
	if err != nil {
		t.Fatalf("body 不是合法 gzip 流: %v", err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip 解压失败: %v", err)
	}
	return string(raw)
}

// TestGzipCompressesStaticAndHTML 资源与 HTML 要真压、内容还得对。
func TestGzipCompressesStaticAndHTML(t *testing.T) {
	h, _ := newTestHub(t)

	rec := getGzip(t, h, "/static/style.css")
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("style.css 应该被 gzip 压缩")
	}
	if rec.Body.Len() >= 104000 {
		t.Errorf("压缩后仍有个百 KB：%d 字节", rec.Body.Len())
	}
	want, err := readEmbedded("static/style.css")
	if err != nil {
		t.Fatal(err)
	}
	if got := gunzip(t, rec.Body.Bytes()); got != want {
		t.Error("解压内容和磁盘上的 style.css 不一致")
	}

	// 首页 HTML（库是空的也一样走模板，肯定 >1KB）
	rec = getGzip(t, h, "/")
	if rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatal("首页 HTML 应该被 gzip 压缩")
	}
	if !strings.Contains(gunzip(t, rec.Body.Bytes()), "</html>") {
		t.Error("解压后的首页不是完整 HTML")
	}
}

// TestGzipSkippedWhenNotAccepted 客户端没说要就不压。
func TestGzipSkippedWhenNotAccepted(t *testing.T) {
	h, _ := newTestHub(t)
	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("没带 Accept-Encoding 不该压缩")
	}
	if rec.Body.Len() < 100000 {
		t.Errorf("不该压缩时 body 应是原始大小，实际 %d", rec.Body.Len())
	}
}

// TestGzipSkipsSSE SSE 是流式响应，压了会拖慢首帧，且 EventSource 关不掉。
func TestGzipSkipsSSE(t *testing.T) {
	// 不真的连 SSE（会挂住），直接造一个同形态的 handler：
	// set Content-Type: text/event-stream 后写一帧。
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: hello\n\n"))
	})
	rec := getGzip(t, gzipWrap(inner), "/api/v1/stream")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("SSE 不该被压缩")
	}
	if rec.Body.String() != "data: hello\n\n" {
		t.Errorf("SSE 帧必须原样透传，实际 %q", rec.Body.String())
	}
}

// TestGzipSkipsBinaryAndRange 二进制下载与 Range 请求都不压。
func TestGzipSkipsBinaryAndRange(t *testing.T) {
	// octet-stream（agent 二进制下载 15MB，压一遍纯费 CPU）
	bin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 4096))
	})
	rec := getGzip(t, gzipWrap(bin), "/api/v1/dl/x")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("二进制下载不该压缩")
	}

	// Range：206 部分响应被压过 Range 语义就没了
	part := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "style.css", time.Now(), strings.NewReader(strings.Repeat("a", 4096)))
	})
	req := httptest.NewRequest(http.MethodGet, "/static/style.css", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-99")
	rec = httptest.NewRecorder()
	gzipWrap(part).ServeHTTP(rec, req)
	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("Range 请求不该压缩（206 语义会被破坏）")
	}
	if rec.Code != http.StatusPartialContent {
		t.Errorf("Range 请求应正常返回 206，实际 %d", rec.Code)
	}
}

// TestGzipSkipsSmallBody 小响应压不出收益，还白费 CPU。
func TestGzipSkipsSmallBody(t *testing.T) {
	small := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("tiny"))
	})
	rec := getGzip(t, gzipWrap(small), "/tiny")
	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("小响应不该压缩")
	}
	if rec.Body.String() != "tiny" {
		t.Errorf("小响应必须原样，实际 %q", rec.Body.String())
	}
}

// TestGzipFlushesHeaderlessStatus 只 WriteHeader 不写 body 的响应
// （重定向 303、204）也要能把状态码透出去 —— close() 的兜底路径。
func TestGzipFlushesHeaderlessStatus(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusSeeOther)
	})
	rec := getGzip(t, gzipWrap(h), "/redirect")
	if rec.Code != http.StatusSeeOther {
		t.Errorf("303 应透传，实际 %d", rec.Code)
	}
	if rec.Header().Get("Content-Encoding") != "" {
		t.Error("空 body 不该压")
	}
}
