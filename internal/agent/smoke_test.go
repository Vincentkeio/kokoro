package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

type fakeCollector struct{ n int }

func (f *fakeCollector) Collect() (*model.Metrics, error) {
	f.n++
	return &model.Metrics{Ts: time.Now().UnixMilli(), CPU: model.CPUStat{Usage: float64(f.n)}}, nil
}

func TestSmoke(t *testing.T) {
	var mu sync.Mutex
	var gotRegister model.RegisterRequest
	var reports []model.Metrics
	var results []model.CommandResult
	reportN := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b []byte
		buf := make([]byte, 1<<16)
		n, _ := r.Body.Read(buf)
		b = buf[:n]
		switch r.URL.Path {
		case "/api/v1/register":
			_ = json.Unmarshal(b, &gotRegister)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"node_id":"nd_1","node_token":"nt_abcdef123456","interval_ms":300,"ca_fingerprint":"","hub_version":"0.1.0"}`))
		case "/api/v1/report":
			var m model.Metrics
			_ = json.Unmarshal(b, &m)
			mu.Lock()
			reports = append(reports, m)
			reportN++
			i := reportN
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if i == 1 {
				_, _ = w.Write([]byte(`{"ok":true,"interval_ms":300,"commands":[{"id":"cm_1","type":"shell","payload":{"cmd":"echo hi","timeout_ms":3000}}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"interval_ms":300,"commands":[]}`))
		case "/api/v1/command/result":
			var res model.CommandResult
			_ = json.Unmarshal(b, &res)
			mu.Lock()
			results = append(results, res)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, []byte("# 测试配置\nhub = \""+srv.URL+"\"\ninterval_ms = 300\n\n[extra]\nfoo = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(ConfigEnv, cfgPath)
	t.Setenv(InstallTokenEnv, "it_test")

	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, &fakeCollector{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := a.Run(ctx); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotRegister.InstallToken != "it_test" || gotRegister.AgentVersion != Version {
		t.Fatalf("注册请求异常：%+v", gotRegister)
	}
	if len(reports) < 2 {
		t.Fatalf("上报次数不足：%d", len(reports))
	}
	if reports[0].Seq != 1 || reports[1].Seq != 2 {
		t.Fatalf("seq 异常：%d %d", reports[0].Seq, reports[1].Seq)
	}
	if len(results) != 1 || results[0].ID != "cm_1" || !strings.Contains(results[0].Stdout, "hi") {
		t.Fatalf("命令结果异常：%+v", results)
	}
	// token 应已落盘
	saved, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "nt_abcdef123456") {
		t.Fatalf("token 未落盘：%s", saved)
	}
	if runtime.GOOS != "windows" { // Windows 无 Unix 权限位，只在类 Unix 上校验
		info, err := os.Stat(cfgPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o777 != 0o600 {
			t.Fatalf("配置文件权限异常：%v", info.Mode().Perm())
		}
	}
}

// TestOfflineAndFatal 验证断线缓冲补报与 401 致命退出。
func TestOfflineAndFatal(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	os.WriteFile(cfgPath, []byte("hub = \"https://example.invalid\"\ntoken = \"nt_abcdef123456\"\ninterval_ms = 100\n"), 0o600)
	t.Setenv(ConfigEnv, cfgPath)

	// 1) Hub 不可达：指标应留在缓冲里，上限 30 条
	var mu sync.Mutex
	seqs := map[int64]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 1<<16)
		n, _ := r.Body.Read(buf)
		var m model.Metrics
		_ = json.Unmarshal(buf[:n], &m)
		mu.Lock()
		seqs[m.Seq] = true
		mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"interval_ms":100,"commands":[]}`))
	}))
	defer srv.Close()

	cfg, err := LoadConfig("")
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(cfg, &fakeCollector{})
	if err != nil {
		t.Fatal(err)
	}
	// 先指向不可达地址，采集若干轮填满缓冲
	a.hub = "http://127.0.0.1:1"
	for i := 0; i < 35; i++ {
		a.collectOnce()
	}
	a.mu.Lock()
	n := len(a.buf)
	first := a.buf[0].Seq
	a.mu.Unlock()
	if n != replayBufferSize {
		t.Fatalf("缓冲条数应为 %d，实际 %d", replayBufferSize, n)
	}
	if first != 6 { // 1..35 里保留最近 30 条，首条 seq=6
		t.Fatalf("缓冲首条 seq 应为 6，实际 %d", first)
	}
	// 恢复连通后按原 seq 补报
	a.hub = srv.URL
	if err := a.flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	left := len(a.buf)
	a.mu.Unlock()
	if left != 0 {
		t.Fatalf("补报后缓冲应清空，实际剩 %d", left)
	}
	mu.Lock()
	defer mu.Unlock()
	if !seqs[6] || !seqs[35] || len(seqs) != replayBufferSize {
		t.Fatalf("补报 seq 不完整：共 %d 条，含6=%v 含35=%v", len(seqs), seqs[6], seqs[35])
	}

	// 2) 401 应让 Run 返回错误
	unauth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer unauth.Close()
	cfg2 := &model.AgentConfig{Hub: unauth.URL, Token: "nt_abcdef123456", IntervalMS: 100}
	a2, err := New(cfg2, &fakeCollector{})
	if err != nil {
		t.Fatal(err)
	}
	if err := a2.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "重新注册") {
		t.Fatalf("401 应导致 Run 返回致命错误，实际：%v", err)
	}
}

func TestFingerprintParse(t *testing.T) {
	if _, err := normalizeFingerprint("sha256/abc"); err == nil {
		t.Fatal("非法指纹应报错")
	}
	fp := "sha256/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	if got, err := normalizeFingerprint(fp); err != nil || got != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" {
		t.Fatalf("指纹解析异常：%q %v", got, err)
	}
}

func TestParseFlatTOML(t *testing.T) {
	raw := parseFlatTOML("hub = \"http://x\" # 注释\ninterval_ms = 1000\ninsecure = true\n[sec]\ntoken = 'abc'\n")
	if raw["hub"] != "http://x" || raw["interval_ms"] != "1000" || raw["insecure"] != "true" || raw["token"] != "abc" {
		t.Fatalf("解析结果异常：%+v", raw)
	}
}
