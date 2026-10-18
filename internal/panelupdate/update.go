// Package panelupdate contains the fixed, official-release web update protocol.
package panelupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}
type Release struct {
	Tag    string  `json:"tag_name"`
	Draft  bool    `json:"draft"`
	Assets []Asset `json:"assets"`
}

var tagPattern = regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?$`)
var idPattern = regexp.MustCompile(`^[a-f0-9]{32,64}$`)

func ValidTag(s string) bool { return len(s) < 100 && tagPattern.MatchString(s) }
func ValidID(s string) bool  { return idPattern.MatchString(s) }

// Compare preserves the menu updater's existing version ordering.
func Compare(a, b string) int {
	parse := func(v string) ([3]int, string) {
		v = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(v), "v"), "V")
		core, pre, _ := strings.Cut(v, "-")
		var n [3]int
		for i, p := range strings.Split(core, ".") {
			if i < 3 {
				n[i], _ = strconv.Atoi(strings.TrimSpace(p))
			}
		}
		return n, pre
	}
	an, ap := parse(a)
	bn, bp := parse(b)
	for i := 0; i < 3; i++ {
		if an[i] < bn[i] {
			return -1
		}
		if an[i] > bn[i] {
			return 1
		}
	}
	if ap == "" && bp == "" {
		return 0
	}
	if ap == "" {
		return 1
	}
	if bp == "" {
		return -1
	}
	as, bs := strings.Split(ap, "."), strings.Split(bp, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		ai, ae := strconv.Atoi(as[i])
		bi, be := strconv.Atoi(bs[i])
		if ae == nil && be == nil {
			if ai < bi {
				return -1
			}
			if ai > bi {
				return 1
			}
		} else {
			if as[i] < bs[i] {
				return -1
			}
			if as[i] > bs[i] {
				return 1
			}
		}
	}
	if len(as) < len(bs) {
		return -1
	}
	if len(as) > len(bs) {
		return 1
	}
	return 0
}

func releaseRequest(ctx context.Context, endpoint string, out any) error {
	req, e := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/Vincentkeio/kokoro/"+endpoint, nil)
	if e != nil {
		return e
	}
	req.Header.Set("User-Agent", "kokoro-updater")
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("无法连接 GitHub，请稍后重试")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub 返回 HTTP %d，请稍后重试", resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if e != nil {
		return e
	}
	if len(b) > 1<<20 {
		return errors.New("发布信息超过上限")
	}
	return json.Unmarshal(b, out)
}
func Latest(ctx context.Context) (Release, error) {
	var list []Release
	if e := releaseRequest(ctx, "releases?per_page=30", &list); e != nil {
		return Release{}, e
	}
	var best Release
	for _, r := range list {
		if !r.Draft && ValidTag(r.Tag) && (best.Tag == "" || Compare(r.Tag, best.Tag) > 0) {
			best = r
		}
	}
	if best.Tag == "" {
		return best, errors.New("没有可用的已发布版本")
	}
	return best, nil
}
func ByTag(ctx context.Context, tag string) (Release, error) {
	var r Release
	if !ValidTag(tag) {
		return r, errors.New("更新目标无效")
	}
	if e := releaseRequest(ctx, "releases/tags/"+url.PathEscape(tag), &r); e != nil {
		return r, e
	}
	if r.Draft || r.Tag != tag {
		return r, errors.New("更新目标尚未发布")
	}
	return r, nil
}
func (r Release) Has(name string) bool { _, e := r.URL(name); return e == nil }
func (r Release) URL(name string) (string, error) {
	for _, a := range r.Assets {
		if a.Name == name {
			u, e := url.Parse(a.URL)
			prefix := "/Vincentkeio/kokoro/releases/download/" + r.Tag + "/"
			if e != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || !strings.HasPrefix(u.Path, prefix) || strings.TrimPrefix(u.Path, prefix) != name {
				return "", errors.New("发布文件地址无效")
			}
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("发布缺少 %s", name)
}

type State struct {
	ID        string `json:"id"`
	Target    string `json:"target"`
	Status    string `json:"status"`
	Message   string `json:"message"`
	UpdatedAt int64  `json:"updated_at"`
}

func (s State) Busy() bool { return s.Status == "queued" || s.Status == "running" }
func Load(path string) (State, error) {
	var s State
	b, e := os.ReadFile(path)
	if e != nil {
		return s, e
	}
	if len(b) > 8192 {
		return s, errors.New("更新记录无效")
	}
	e = json.Unmarshal(b, &s)
	return s, e
}
func Save(path string, s State) error {
	s.UpdatedAt = time.Now().UnixMilli()
	b, e := json.Marshal(s)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".update-*")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		_, e = f.Write(b)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	if e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
func Supported() bool {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return false
	}
	_, e := exec.LookPath("systemd-run")
	return e == nil
}

type Spec struct{ Role, Target, ID, StatePath, DataDir, ConfigPath, HealthURL string }

func Launch(s Spec) error {
	if !Supported() {
		return errors.New("在线更新需要 Linux、root 和 systemd")
	}
	if (s.Role != "hub" && s.Role != "agent") || !ValidTag(s.Target) || !ValidID(s.ID) {
		return errors.New("更新请求无效")
	}
	if old, e := Load(s.StatePath); e == nil {
		if old.ID == s.ID {
			return nil
		}
		if old.Busy() && time.Now().UnixMilli()-old.UpdatedAt < 20*60*1000 {
			return errors.New("已有更新正在执行")
		}
	}
	state := State{ID: s.ID, Target: s.Target, Status: "queued", Message: "等待更新进程启动"}
	if e := Save(s.StatePath, state); e != nil {
		return e
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	exe, e = filepath.EvalSymlinks(exe)
	if e != nil {
		return e
	}
	args := []string{"--quiet", "--collect", "--unit=kokoro-web-update-" + s.Role, "--property=RuntimeMaxSec=15min", exe, "apply-update", "--role", s.Role, "--target", s.Target, "--job", s.ID, "--state", s.StatePath, "--data", s.DataDir, "--config", s.ConfigPath, "--health", s.HealthURL}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if b, e := exec.CommandContext(ctx, "systemd-run", args...).CombinedOutput(); e != nil {
		state.Status = "failed"
		state.Message = "无法启动独立更新进程"
		_ = Save(s.StatePath, state)
		return fmt.Errorf("%s：%s", state.Message, strings.TrimSpace(string(b)))
	}
	return nil
}
