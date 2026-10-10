package hub

// 贴 GitHub 链接抓主题的测试。
//
// 分两层：
//   - parseGitHubThemeURL 是纯映射，直接表驱动钉住各种链接形态；
//   - 抓取链路把 githubRawHost 指到本地 httptest 服务器端到端跑一遍，
//     顺带验证「来源被记进清单」。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseGitHubThemeURL(t *testing.T) {
	// 前置：确保是默认的 raw 域名，下面的期望值依赖它。
	if githubRawHost != "https://raw.githubusercontent.com/" {
		t.Fatalf("githubRawHost 被改过：%q", githubRawHost)
	}
	const raw = "https://raw.githubusercontent.com/"
	cases := []struct {
		in   string
		dir  string
		file string
		ok   bool
	}{
		{"https://github.com/user/repo", raw + "user/repo/HEAD", "", true},
		{"https://github.com/user/repo/", raw + "user/repo/HEAD", "", true},
		{"https://github.com/user/repo.git", raw + "user/repo/HEAD", "", true},
		{"https://github.com/user/repo/tree/main", raw + "user/repo/main", "", true},
		{"https://github.com/user/repo/tree/main/themes/dark",
			raw + "user/repo/main/themes/dark", "", true},
		{"https://github.com/user/repo/blob/main/theme.json",
			"", raw + "user/repo/main/theme.json", true},
		{"https://raw.githubusercontent.com/user/repo/main/theme.json",
			"", raw + "user/repo/main/theme.json", true},
		{"https://raw.githubusercontent.com/user/repo/main/themes/dark",
			raw + "user/repo/main/themes/dark", "", true},
		// 非主题页面 / 非 GitHub：交回通用路径。
		{"https://github.com/user", "", "", false},
		{"https://github.com/user/repo/issues/1", "", "", false},
		{"https://example.com/user/repo", "", "", false},
		{"https://gitlab.com/user/repo", "", "", false},
		{"ftp://github.com/user/repo", "", "", false},
		{"", "", "", false},
	}
	for _, c := range cases {
		got, ok := parseGitHubThemeURL(c.in)
		if ok != c.ok {
			t.Errorf("%q: ok = %v，应为 %v", c.in, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if got.Dir != c.dir || got.File != c.file {
			t.Errorf("%q → {Dir:%q File:%q}，应为 {Dir:%q File:%q}",
				c.in, got.Dir, got.File, c.dir, c.file)
		}
	}
}

// withRawHost 把 raw 基址临时指向测试服务器。
func withRawHost(t *testing.T, base string) {
	t.Helper()
	old := githubRawHost
	githubRawHost = base
	t.Cleanup(func() { githubRawHost = old })
}

func TestGrabThemeFromGitHubRepo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/repo/HEAD/theme.json" {
			_, _ = w.Write([]byte(`{"schemaVersion":1,"id":"gh.repo","name":"仓库主题",
				"version":"1.2.0","author":"gh","tokensSchemaVersion":2,
				"layoutSchemaVersion":1,"tokens":{"--kokoro-color-primary":"#abcdef"},
				"layout":{}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withRawHost(t, srv.URL+"/")

	h, _ := newThemeTestHub(t)
	const src = "https://github.com/user/repo"
	target, ok := parseGitHubThemeURL(src)
	if !ok {
		t.Fatal("应识别为 GitHub 链接")
	}
	m, kind, err := h.grabGitHubTheme(src, target)
	if err != nil {
		t.Fatalf("GitHub 抓取失败: %v", err)
	}
	if kind != "manifest" {
		t.Errorf("kind = %q，应为 manifest", kind)
	}
	if m.ID != "gh.repo" {
		t.Errorf("ID = %q", m.ID)
	}
	if h.themes.Get("gh.repo") == nil {
		t.Error("抓到的主题应进入注册表")
	}
	// 来源要记进清单，后台才能展示、导出才能带走出处。
	if !strings.Contains(m.SourceURL, "github.com") {
		t.Errorf("SourceURL = %q，应记下来源", m.SourceURL)
	}
}

// 仓库里没有 theme.json、但有主题包时，应能装包。
func TestGrabThemeFromGitHubBundle(t *testing.T) {
	bundle := makeBundle(t, "gh.bundled", map[string][]byte{"bg.png": []byte("GHPNG")})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/repo/HEAD/theme.kokoro-theme" {
			_, _ = w.Write(bundle)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withRawHost(t, srv.URL+"/")

	h, _ := newThemeTestHub(t)
	src := "https://github.com/user/repo"
	target, _ := parseGitHubThemeURL(src)
	m, kind, err := h.grabGitHubTheme(src, target)
	if err != nil {
		t.Fatalf("应能从仓库装包: %v", err)
	}
	if kind != "bundle" {
		t.Errorf("kind = %q，应为 bundle", kind)
	}
	if m.ID != "gh.bundled" {
		t.Errorf("ID = %q", m.ID)
	}
	// 包里的资源也应进缓存，/_theme-assets/ 立刻可用。
	if got, ok := h.themeAssets.get("gh.bundled", "assets/bg.png"); !ok || string(got) != "GHPNG" {
		t.Error("从 GitHub 抓来的包，资源没有进缓存")
	}
}

// 直指文件的链接（blob 形态）走单文件路径。
func TestGrabThemeFromGitHubBlobFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/repo/main/themes/x/theme.json" {
			_, _ = w.Write([]byte(`{"schemaVersion":1,"id":"gh.blob","name":"单文件",
				"version":"1.0.0","author":"gh","tokensSchemaVersion":2,
				"layoutSchemaVersion":1,"tokens":{"--kokoro-color-primary":"#111111"},
				"layout":{}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withRawHost(t, srv.URL+"/")

	h, _ := newThemeTestHub(t)
	src := "https://github.com/user/repo/blob/main/themes/x/theme.json"
	target, ok := parseGitHubThemeURL(src)
	if !ok || target.File == "" {
		t.Fatal("blob 链接应解析为直指文件")
	}
	m, kind, err := h.grabGitHubTheme(src, target)
	if err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	if kind != "manifest" || m.ID != "gh.blob" {
		t.Errorf("kind=%q id=%q", kind, m.ID)
	}
}

// 仓库里什么都没有时要给出可读的错误，而不是空指针或 500。
func TestGrabThemeFromGitHubNothing(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	withRawHost(t, srv.URL+"/")

	h, _ := newThemeTestHub(t)
	src := "https://github.com/user/repo"
	target, _ := parseGitHubThemeURL(src)
	if _, _, err := h.grabGitHubTheme(src, target); err == nil {
		t.Fatal("空仓库应报错")
	}
}

// 一键获取入口要能把 GitHub 链接分流过去（而不是按站点根拼 /theme.json）。
func TestGrabThemeRoutesGitHubLink(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/u/r/HEAD/theme.json" {
			_, _ = w.Write([]byte(`{"schemaVersion":1,"id":"gh.route","name":"分流",
				"version":"1.0.0","author":"gh","tokensSchemaVersion":2,
				"layoutSchemaVersion":1,"tokens":{"--kokoro-color-primary":"#222222"},
				"layout":{}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	withRawHost(t, srv.URL+"/")

	h, _ := newThemeTestHub(t)
	m, _, err := h.grabTheme("https://github.com/u/r")
	if err != nil {
		t.Fatalf("grabTheme 应把 GitHub 链接分流到 GitHub 路径: %v", err)
	}
	if m.ID != "gh.route" {
		t.Errorf("ID = %q", m.ID)
	}
}
