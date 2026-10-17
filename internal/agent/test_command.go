package agent

// The Hub selects a test, never its executable, script URL, arguments or timeout.
// Agent recipes use a pinned testing tool; even the Hub fallback must match its
// compiled-in digest. Changing the tool requires an Agent release.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
	benchtool "github.com/Vincentkeio/kokoro/scripts"
)

const testToolSHA256 = "e25d02de9e81b9ec8b3d056387ff7cb4406c064972ab75ef89ca09bdf92aa58e"
const testToolLimit = 1024 * 1024

// Invoked by a fixed local oneshot service, outside the Agent filesystem
// sandbox. Both script URL and prepare-only arguments are compiled constants.
func PrepareTestDependencies(parent context.Context, cfg *model.AgentConfig) error {
	if runtime.GOOS != "linux" {
		return errors.New("测试组件安装仅支持 Linux")
	}
	a, err := New(cfg, nil)
	if err != nil {
		return err
	}
	defer a.client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(parent, 8*time.Minute)
	defer cancel()
	tool, err := a.fetchTestTool(ctx)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "kokoro-components-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "kokoro-bench.sh")
	if err := os.WriteFile(path, tool, 0600); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "bash", path, "--prepare-only")
	configureTestProcess(command)
	// apt drops download privileges to _apt; a root-only temp cwd is not
	// traversable to that user. The script itself remains in its private dir.
	command.Dir = "/"
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}

type testRecipe struct {
	only    string
	timeout time.Duration
}

// This local map is the complete test allowlist. Payloads cannot extend it.
var testRecipes = map[string]testRecipe{
	"bench":       {"sysinfo,disk,cpu,memory", 30 * time.Minute},
	"ipquality4":  {"ip4", 15 * time.Minute},
	"ipquality6":  {"ip6", 15 * time.Minute},
	"ipquality":   {"ip", 30 * time.Minute},
	"netquality4": {"net,route4", 20 * time.Minute},
	"netquality6": {"net,route6", 20 * time.Minute},
	"netquality":  {"net,route", 35 * time.Minute},
}

func (a *Agent) runTest(cmd model.Command) model.CommandResult {
	return a.runTestContext(context.Background(), cmd)
}

func (a *Agent) runTestContext(parent context.Context, cmd model.Command) model.CommandResult {
	res := model.CommandResult{ID: cmd.ID}
	kind, ok := cmd.Payload["kind"].(string)
	recipe, allowed := testRecipes[kind]
	if !ok || !allowed || len(cmd.Payload) != 1 {
		res.Stderr = "安全限制：仅接受内置硬件、IP 与线路测试类型，不接受自定义命令、参数或脚本地址"
		return res
	}
	if runtime.GOOS != "linux" {
		res.Stderr = "这些测试目前仅支持 Linux Agent"
		return res
	}
	start := time.Now()
	defer func() { logger.Printf("测试 %s 完成，用时 %s", kind, time.Since(start)) }()
	ctx, cancel := context.WithTimeout(parent, recipe.timeout)
	defer cancel()
	dir, err := os.MkdirTemp("", "kokoro-test-")
	if err != nil {
		res.Stderr = err.Error()
		return res
	}
	defer os.RemoveAll(dir)
	tool, err := a.fetchTestTool(ctx)
	if err != nil {
		res.Stderr = err.Error()
		return res
	}
	path := filepath.Join(dir, "kokoro-bench.sh")
	if err = os.WriteFile(path, tool, 0600); err != nil {
		res.Stderr = err.Error()
		return res
	}
	// Arguments are local constants. No -c shell string is accepted from the Hub.
	c := exec.CommandContext(ctx, "bash", path, "-y", "--only", recipe.only)
	configureTestProcess(c)
	c.Dir = dir
	var stdout, stderr limitedBuffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err = c.Run()
	res.DurationMS = time.Since(start).Milliseconds()
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	res.OK = err == nil
	if ctx.Err() != nil {
		res.OK, res.ExitCode = false, -1
		if errors.Is(ctx.Err(), context.Canceled) {
			res.Stderr += "\n[kokoro] Agent 停止，测试已中断并终止子进程"
		} else {
			res.Stderr += "\n[kokoro] 测试超时，已终止"
		}
	} else if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			res.ExitCode = exit.ExitCode()
		} else {
			res.ExitCode = -1
			res.Stderr += "\n[kokoro] " + err.Error()
		}
	}
	return res
}

func (a *Agent) fetchTestTool(ctx context.Context) ([]byte, error) {
	// The embedded script and its digest ship in the same reviewed release.
	// Keep verified downloads as a recovery path for mismatched builds.
	tool := benchtool.Script()
	digest := sha256.Sum256(tool)
	if len(tool) <= testToolLimit && hex.EncodeToString(digest[:]) == testToolSHA256 {
		return tool, nil
	}
	sources := []string{strings.TrimRight(a.hub, "/") + "/api/v1/dl/kokoro-bench.sh"}
	var lastErr error
	for _, source := range sources {
		fetchCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, source, nil)
		if err != nil {
			cancel()
			lastErr = err
			continue
		}
		// The Agent client includes the configured Hub certificate pin.
		client := a.client
		resp, err := client.Do(req)
		if err == nil {
			var data []byte
			data, err = io.ReadAll(io.LimitReader(resp.Body, testToolLimit+1))
			resp.Body.Close()
			if err == nil && resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			if err == nil {
				digest := sha256.Sum256(data)
				if len(data) > testToolLimit || hex.EncodeToString(digest[:]) != testToolSHA256 {
					err = errors.New("测试工具完整性校验失败，拒绝执行")
				} else {
					cancel()
					return data, nil
				}
			}
		}
		cancel()
		lastErr = err
	}
	return nil, fmt.Errorf("无法取得可信测试工具：%w", lastErr)
}
