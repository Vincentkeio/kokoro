package hub

// 从 GitHub 仓库抓主题。
//
// 为什么单独开一条路：探针站的 /theme.json 是「当前生效主题」的实时快照，
// 而主题作者通常把主题放在 GitHub 仓库里（一份 theme.json，可能带 assets/）。
// 让管理员直接贴仓库链接，比要求对方先装上一套、再开放抓取端点更省事，
// 也绕开了「从陌生探针站下载」的心理门槛——GitHub 至少是个可信来源。
//
// 解析规则（只认这几种形态，其余原样交回通用抓取）：
//
//	https://github.com/U/R                          → 仓库根，默认分支
//	https://github.com/U/R/tree/REF[/SUB...]        → 仓库内子目录
//	https://github.com/U/R/blob/REF/FILE...         → 仓库内单个文件
//	https://raw.githubusercontent.com/U/R/REF/...   → 原样
//
// issue / pull / wiki / releases 等页面一律不认——那些不是主题。
//
// 网络出口仍然走 newThemeGrabClient：GitHub 是公网地址，现有的
// SSRF 防护（逐跳校验 + 私有段拒绝）对它照常生效，不需要为它开任何后门。

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/Vincentkeio/kokoro/internal/theme"
)

// githubRawHost 是 raw 内容的基址。做成变量而不是常量，
// 是为了让测试能把它指向本地 httptest 服务器，从而端到端跑通抓取链路。
var githubRawHost = "https://raw.githubusercontent.com/"

// githubThemeTarget 描述一次 GitHub 抓取要打的地址。
type githubThemeTarget struct {
	// Dir 是主题所在目录的 raw 地址（不带末尾斜杠）。File 为空时用它。
	Dir string
	// File 非空表示链接直接指向一个文件（theme.json 或主题包）。
	File string
}

// parseGitHubThemeURL 把 GitHub 链接解析成 raw 抓取目标。
// 第二个返回值为 false 表示「这不是 GitHub 链接」，调用方应走通用路径。
func parseGitHubThemeURL(src string) (githubThemeTarget, bool) {
	u, err := url.Parse(strings.TrimSpace(src))
	if err != nil {
		return githubThemeTarget{}, false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return githubThemeTarget{}, false
	}
	rawHost := githubRawHost
	switch strings.ToLower(u.Hostname()) {
	case "raw.githubusercontent.com":
		p := strings.Trim(u.Path, "/")
		if p == "" || strings.Contains(p, "..") {
			return githubThemeTarget{}, false
		}
		if isThemeFile(p) {
			return githubThemeTarget{File: rawHost + p}, true
		}
		return githubThemeTarget{Dir: rawHost + p}, true
	case "github.com", "www.github.com":
	default:
		return githubThemeTarget{}, false
	}

	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 2 {
		return githubThemeTarget{}, false
	}
	user, repo := segs[0], segs[1]
	if user == "" || repo == "" {
		return githubThemeTarget{}, false
	}
	repo = strings.TrimSuffix(repo, ".git")

	base := rawHost + user + "/" + repo
	rest := segs[2:]
	if len(rest) == 0 {
		// 仓库根。HEAD 是 GitHub 的默认分支别名。
		return githubThemeTarget{Dir: base + "/HEAD"}, true
	}
	switch rest[0] {
	case "tree":
		if len(rest) < 2 {
			return githubThemeTarget{Dir: base + "/HEAD"}, true
		}
		return githubThemeTarget{Dir: base + "/" + strings.Join(rest[1:], "/")}, true
	case "blob":
		if len(rest) < 3 {
			return githubThemeTarget{}, false
		}
		return githubThemeTarget{File: base + "/" + strings.Join(rest[1:], "/")}, true
	default:
		// /U/R/xxx 这种不是标准页面形态，别猜，交回通用路径。
		return githubThemeTarget{}, false
	}
}

// isThemeFile 判断 raw 路径是否直指一个可抓的文件（清单或包）。
func isThemeFile(p string) bool {
	lp := strings.ToLower(p)
	return strings.HasSuffix(lp, ".json") || strings.HasSuffix(lp, theme.PackageExt)
}

// grabGitHubTheme 按解析出的目标抓取主题。
func (h *Hub) grabGitHubTheme(src string, target githubThemeTarget) (*theme.Manifest, string, error) {
	fetch := themeFetcher(newThemeGrabClient(themeFetchTimeout, h.themeFetchPolicy()))

	// 直指文件：按后缀决定走清单还是包。
	if target.File != "" {
		raw, err := fetch(target.File)
		if err != nil {
			return nil, "", err
		}
		return h.installFetchedTheme(raw, src)
	}

	// 目录：先找 theme.json。
	if raw, err := fetch(target.Dir + "/theme.json"); err == nil {
		m, err := h.importManifest(raw, src)
		if err != nil {
			return nil, "", err
		}
		return m, "manifest", nil
	}
	// 再找仓库里的主题包（约定名 theme.kokoro-theme）。
	raw, err := fetch(target.Dir + "/theme" + theme.PackageExt)
	if err != nil {
		return nil, "", fmt.Errorf("该目录下既没有 theme.json，也没有 theme%s", theme.PackageExt)
	}
	m, err := h.installBundle(raw, src)
	if err != nil {
		return nil, "", err
	}
	return m, "bundle", nil
}

// installFetchedTheme 按内容判断抓来的是清单还是包，分别走对应安装路径。
func (h *Hub) installFetchedTheme(raw []byte, src string) (*theme.Manifest, string, error) {
	if len(raw) >= 2 && raw[0] == 'P' && raw[1] == 'K' {
		m, err := h.installBundle(raw, src)
		if err != nil {
			return nil, "", err
		}
		return m, "bundle", nil
	}
	m, err := h.importManifest(raw, src)
	if err != nil {
		return nil, "", err
	}
	return m, "manifest", nil
}
