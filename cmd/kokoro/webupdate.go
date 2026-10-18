package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/panelupdate"
)

// The transient systemd unit survives stopping/restarting the main service.
func runWebUpdate(args []string) int {
	fs := flag.NewFlagSet("apply-update", flag.ContinueOnError)
	role := fs.String("role", "", "")
	target := fs.String("target", "", "")
	id := fs.String("job", "", "")
	state := fs.String("state", "", "")
	data := fs.String("data", "", "")
	cfg := fs.String("config", "", "")
	health := fs.String("health", "", "")
	if fs.Parse(args) != nil || fs.NArg() != 0 || !panelupdate.ValidID(*id) || !panelupdate.ValidTag(*target) || (*role != "hub" && *role != "agent") || !filepath.IsAbs(*state) {
		return 2
	}
	s, e := panelupdate.Load(*state)
	if e != nil || s.ID != *id || s.Target != *target || !s.Busy() {
		return 2
	}
	s.Status = "running"
	s.Message = "正在下载并校验官方发布文件"
	if panelupdate.Save(*state, s) != nil {
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 14*time.Minute)
	defer cancel()
	err := applyWebUpdate(ctx, *role, *target, *data, *cfg, *health)
	if err != nil {
		s.Status = "failed"
		s.Message = err.Error()
	} else {
		s.Status = "completed"
		s.Message = "更新完成"
	}
	if e := panelupdate.Save(*state, s); e != nil {
		fmt.Fprintln(os.Stderr, e)
		return 1
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func runWebUpdateRequest(args []string) int {
	fs := flag.NewFlagSet("apply-update-request", flag.ContinueOnError)
	request := fs.String("request", "", "")
	state := fs.String("state", "", "")
	data := fs.String("data", "", "")
	health := fs.String("health", "", "")
	if fs.Parse(args) != nil || fs.NArg() != 0 || !filepath.IsAbs(*request) || !filepath.IsAbs(*state) || !filepath.IsAbs(*data) || !strings.HasPrefix(*request, filepath.Clean(*data)+string(os.PathSeparator)) {
		return 2
	}
	info, e := os.Lstat(*request)
	if e != nil || !info.Mode().IsRegular() {
		return 1
	}
	f, e := os.Open(*request)
	if e != nil {
		return 1
	}
	opened, e := f.Stat()
	if e != nil || !os.SameFile(info, opened) {
		f.Close()
		return 1
	}
	b, e := io.ReadAll(io.LimitReader(f, 8193))
	f.Close()
	if e != nil || len(b) > 8192 {
		return 1
	}
	var req panelupdate.State
	if json.Unmarshal(b, &req) != nil || !panelupdate.ValidID(req.ID) || !panelupdate.ValidTag(req.Target) || req.Status != "queued" {
		return 1
	}
	if e = os.Remove(*request); e != nil {
		return 1
	}
	return runWebUpdate([]string{"--role=hub", "--target=" + req.Target, "--job=" + req.ID, "--state=" + *state, "--data=" + *data, "--health=" + *health})
}

type pinnedReleaseSource struct {
	hubReleaseSource
	release hubRelease
}

func (s pinnedReleaseSource) Releases(context.Context) ([]hubRelease, error) {
	return []hubRelease{s.release}, nil
}

type webHubService struct {
	systemdHubService
	target string
}

func (s webHubService) Healthy(ctx context.Context) error {
	if e := s.systemdHubService.Healthy(ctx); e != nil {
		return e
	}
	return verifyRunningUpdate(ctx, s.unit, s.target)
}
func verifyRunningUpdate(ctx context.Context, unit, target string) error {
	b, e := exec.CommandContext(ctx, "systemctl", "show", unit, "-p", "MainPID", "--value").Output()
	if e != nil {
		return e
	}
	pid := strings.TrimSpace(string(b))
	for _, c := range pid {
		if c < '0' || c > '9' {
			return errors.New("无法确认更新后的进程")
		}
	}
	if pid == "" || pid == "0" {
		return errors.New("更新后的服务尚未启动")
	}
	b, e = exec.CommandContext(ctx, "/proc/"+pid+"/exe", "version").Output()
	if e != nil || strings.TrimSpace(string(b)) != "kokoro "+target {
		return errors.New("运行中的程序与目标更新不一致")
	}
	return nil
}
func applyWebUpdate(ctx context.Context, role, target, data, config, health string) error {
	if !panelupdate.Supported() {
		return errors.New("当前环境不支持在线更新")
	}
	newer, e := panelupdate.IsNewer(ctx, target, Version)
	if e != nil {
		return e
	}
	if !newer {
		return nil
	}
	rel, e := panelupdate.ByTag(ctx, target)
	if e != nil {
		return e
	}
	prefix := "kokoro-linux-"
	if role == "agent" {
		prefix = "kokoro-agent-linux-"
	}
	name := prefix + runtime.GOARCH
	binURL, e := rel.URL(name)
	if e != nil {
		return e
	}
	sumURL, e := rel.URL(name + ".sha256")
	if e != nil {
		return e
	}
	src := newGitHubHubSource(&http.Client{Timeout: 5 * time.Minute})
	hr := hubRelease{Tag: rel.Tag}
	for _, a := range rel.Assets {
		hr.Assets = append(hr.Assets, hubAsset{Name: a.Name, URL: a.URL, Size: a.Size})
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	exe, e = filepath.EvalSymlinks(exe)
	if e != nil {
		return e
	}
	if role == "hub" {
		if !filepath.IsAbs(data) || health == "" {
			return errors.New("面板更新配置无效")
		}
		u := newHubUpdater(exe, Version, nil, io.Discard)
		u.databasePath = filepath.Join(data, "kokoro.db")
		u.assumeYes = true
		u.source = pinnedReleaseSource{src, hr}
		u.service = webHubService{systemdHubService{unit: hubUnitName, healthURL: health, client: &http.Client{Timeout: 5 * time.Second}}, target}
		return u.Run(ctx)
	}
	if !filepath.IsAbs(config) {
		return errors.New("探针配置路径无效")
	}
	before, e := os.ReadFile(config)
	if e != nil {
		return e
	}
	hash := sha256.Sum256(before)
	binary, e := src.Download(ctx, binURL, maxHubBinarySize)
	if e != nil {
		return fmt.Errorf("下载探针失败：%w", e)
	}
	sum, e := src.Download(ctx, sumURL, maxHubChecksumSize)
	if e != nil {
		return e
	}
	if e = verifyHubArtifact(hr, name, binary, sum, runtime.GOARCH); e != nil {
		return e
	}
	backup, e := replaceHubBinary(exe, binary, time.Now())
	if e != nil {
		return e
	}
	svc := systemdHubService{unit: "kokoro-agent"}
	check := func() error {
		if e := svc.Restart(ctx); e != nil {
			return e
		}
		for i := 0; i < 10; i++ {
			time.Sleep(2 * time.Second)
			if e := verifyRunningUpdate(ctx, "kokoro-agent", target); e == nil {
				after, e := os.ReadFile(config)
				if e == nil && sha256.Sum256(after) == hash {
					return nil
				}
				return errors.New("探针配置校验失败")
			}
		}
		return errors.New("探针重启检查失败")
	}
	if e = check(); e != nil {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		re := restoreHubBinary(exe, backup)
		se := svc.Restart(rollbackCtx)
		return errors.Join(fmt.Errorf("探针更新失败，已尝试回退：%w", e), re, se)
	}
	pruneHubBackups(exe, hubBackupsToKeep)
	return nil
}
