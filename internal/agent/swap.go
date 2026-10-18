package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

const maxSwapSizeMB = 512 * 1024
const swapDiskReserveBytes = uint64(512 * 1024 * 1024)

func validateSwapDiskSpace(sizeMB int, available uint64) error {
	needed := uint64(sizeMB) * 1024 * 1024
	if available < swapDiskReserveBytes || needed > available-swapDiskReserveBytes {
		return fmt.Errorf("磁盘空间不足：新 SWAP 需要 %.1f GiB，并需保留至少 512 MiB 可用空间", float64(needed)/(1024*1024*1024))
	}
	return nil
}

const (
	// 交换文件按系统惯例放在根目录（/swapfile、/swapfile2…）。
	// 它是这台机器的交换空间，属于系统，不是面板数据：不放进任何 Kokoro 目录，
	// 卸载面板和导入导出备份都不会碰它（备份只装 images/ 与 kokoro.db）。
	managedSwapName  = "swapfile"
	managedSwapSlots = 9
	swapFstabMark    = "# kokoro-managed-swap"
	fstabPath        = "/etc/fstab"
)

// managedSwapCandidate 返回第 n 个候选位置（1 -> /swapfile，2 -> /swapfile2…）。
func managedSwapCandidate(dir string, n int) string {
	if n <= 1 {
		return filepath.Join(dir, managedSwapName)
	}
	return filepath.Join(dir, fmt.Sprintf("%s%d", managedSwapName, n))
}

// managedSwapFstabPath 从 fstab 里取出我们之前标记过、并且可以继续沿用的交换文件。
//
// 只认放在根目录的 /swapfileN。旧版把交换文件塞进 Kokoro 目录（/var/lib/kokoro、
// /var/lib/kokoro-swap），那条标记不再沿用——否则 Kokoro 会一直把自己的交换空间
// 放在自己的目录下，正是要改掉的做法。旧文件留着不动，算作别人的既有 SWAP。
func managedSwapFstabPath(dir, fstab string) string {
	for _, line := range strings.Split(fstab, "\n") {
		if !strings.Contains(line, swapFstabMark) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 || !isManagedSwapName(filepath.Base(fields[0])) {
			continue
		}
		if filepath.Dir(filepath.Clean(fields[0])) == filepath.Clean(dir) {
			return fields[0]
		}
	}
	return ""
}

// isManagedSwapName 报告文件名是不是我们那套命名（swapfile、swapfile2…）。
func isManagedSwapName(base string) bool {
	if base == managedSwapName {
		return true
	}
	if !strings.HasPrefix(base, managedSwapName) {
		return false
	}
	n, err := strconv.Atoi(strings.TrimPrefix(base, managedSwapName))
	return err == nil && n >= 2 && n <= managedSwapSlots
}

// resolveManagedSwapFile 挑这次要用的交换文件，dir 是放置目录（生产环境为 /）。
//
// 已经有我们标记过的文件就继续用它，避免同一台机器上出现两个 Kokoro 交换文件；
// 否则取第一个空位。已存在、但 fstab 里没有我们标记的普通文件是别人的交换空间，
// 一律跳过，绝不覆盖。
func resolveManagedSwapFile(dir, fstab string) (string, error) {
	if marked := managedSwapFstabPath(dir, fstab); marked != "" {
		return marked, nil
	}
	for n := 1; n <= managedSwapSlots; n++ {
		candidate := managedSwapCandidate(dir, n)
		info, err := os.Lstat(candidate)
		if errors.Is(err, fs.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		if info.Mode().IsRegular() {
			continue
		}
		// 目录、设备之类占着这个名字，换下一个候选。
	}
	return "", fmt.Errorf("%s 下没有可用的 SWAP 文件位置（%s 到 %s%d 都被占用）",
		dir, managedSwapCandidate(dir, 1), managedSwapName, managedSwapSlots)
}

func managedSwapFstabContent(path, data string) string {
	var lines []string
	for _, line := range strings.Split(strings.TrimSuffix(data, "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == path && strings.Contains(line, swapFstabMark) {
			continue
		}
		if line != "" || len(lines) > 0 {
			lines = append(lines, line)
		}
	}
	lines = append(lines, path+" none swap sw 0 0 "+swapFstabMark)
	return strings.Join(lines, "\n") + "\n"
}

func validSwapSizeMB(sizeMB int) bool {
	return sizeMB >= 128 && sizeMB <= maxSwapSizeMB
}

// managedSwapSizeForTotal converts a node-wide SWAP target into the size of
// Kokoro's file while preserving active SWAP sources owned elsewhere.
func managedSwapSizeForTotal(managedFile string, targetMB int) (int, error) {
	f, err := os.Open("/proc/swaps")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return managedSwapFileSizeForTotal(managedFile, targetMB, f)
}

func managedSwapFileSizeForTotal(managedFile string, targetMB int, data io.Reader) (int, error) {
	s := bufio.NewScanner(data)
	var otherSwapKiB int64
	var currentSwapKiB int64
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 3 || fields[0] == "Filename" || fields[0] == managedFile {
			if len(fields) >= 3 && fields[0] == managedFile {
				sizeKiB, err := strconv.ParseInt(fields[2], 10, 64)
				if err != nil || sizeKiB < 0 {
					return 0, fmt.Errorf("无法读取现有 SWAP 容量")
				}
				currentSwapKiB += sizeKiB
			}
			continue
		}
		sizeKiB, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || sizeKiB < 0 {
			return 0, fmt.Errorf("无法读取现有 SWAP 容量")
		}
		otherSwapKiB += sizeKiB
		currentSwapKiB += sizeKiB
	}
	if err := s.Err(); err != nil {
		return 0, err
	}
	if int64(targetMB)*1024 < currentSwapKiB {
		return 0, fmt.Errorf("不能执行：目标 SWAP 容量小于当前容量（当前约 %.1f GiB）", float64(currentSwapKiB)/(1024*1024))
	}
	remainingKiB := int64(targetMB)*1024 - otherSwapKiB
	if remainingKiB < 0 {
		return 0, fmt.Errorf("现有其它 SWAP 已超过目标容量，Kokoro 不会关闭或缩小它")
	}
	if remainingKiB == 0 {
		return 0, fmt.Errorf("目标容量已由现有 SWAP 满足，Kokoro 不会关闭自己的 SWAP")
	}
	managedMB := (remainingKiB + 1023) / 1024
	if managedMB < 128 {
		return 0, fmt.Errorf("扣除现有 SWAP 后的剩余容量不足 128 MiB，无法创建 Kokoro SWAP 文件")
	}
	if managedMB > int64(maxSwapSizeMB) {
		return 0, fmt.Errorf("计算出的 Kokoro SWAP 容量超出上限")
	}
	return int(managedMB), nil
}

// runSwapContext delegates privileged filesystem and swapon work to a narrow
// systemd helper. The long-running Agent service never receives CAP_SYS_ADMIN.
func (a *Agent) runSwapContext(parent context.Context, cmd model.Command) model.CommandResult {
	res := model.CommandResult{ID: cmd.ID}
	for key := range cmd.Payload {
		if key != "size_mb" {
			res.Stderr = "安全限制：SWAP 指令包含不允许的参数：" + key
			return res
		}
	}
	raw, ok := cmd.Payload["size_mb"]
	if !ok || !validCommandInteger(raw, 128, maxSwapSizeMB) {
		res.Stderr = "SWAP 容量必须为 128 到 524288 MiB，且不能低于当前容量"
		return res
	}
	sizeMB := payloadIntValue(raw)
	if !validSwapSizeMB(sizeMB) {
		res.Stderr = "SWAP 容量必须为 128 到 524288 MiB，且不能低于当前容量"
		return res
	}
	if _, err := exec.LookPath("systemctl"); err != nil {
		if err := ApplySwapContext(parent, sizeMB); err != nil {
			res.Stderr = err.Error()
			return res
		}
		res.OK = true
		res.Stdout = swapResultText(sizeMB)
		return res
	}
	ctx, cancel := context.WithTimeout(parent, 11*time.Minute)
	defer cancel()
	unit := fmt.Sprintf("kokoro-swap@%d.service", sizeMB)
	out, err := exec.CommandContext(ctx, "systemctl", "start", unit).CombinedOutput()
	if err != nil {
		res.Stderr = strings.TrimSpace(string(out))
		if res.Stderr == "" {
			res.Stderr = err.Error()
		}
		if parent.Err() != nil {
			stopCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
			stopOut, stopErr := exec.CommandContext(stopCtx, "systemctl", "stop", unit).CombinedOutput()
			stop()
			res.Stderr = "任务已取消"
			if stopErr != nil {
				stopMessage := strings.TrimSpace(string(stopOut))
				if stopMessage == "" {
					stopMessage = stopErr.Error()
				}
				res.Stderr += "；停止 SWAP 调整失败：" + stopMessage
			}
		}
		return res
	}
	res.OK = true
	res.Stdout = swapResultText(sizeMB)
	return res
}

func swapResultText(sizeMB int) string {
	return fmt.Sprintf("SWAP 已调整为 %.1f GiB", float64(sizeMB)/1024)
}
