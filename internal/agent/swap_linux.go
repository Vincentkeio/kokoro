//go:build linux

package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ApplySwap is called only by the dedicated, capability-limited systemd unit.
func ApplySwap(sizeMB int) error {
	return ApplySwapContext(context.Background(), sizeMB)
}

func ApplySwapContext(parent context.Context, sizeMB int) error {
	if !validSwapSizeMB(sizeMB) {
		return errors.New("SWAP 容量必须为 128 到 524288 MiB")
	}
	if os.Geteuid() != 0 {
		return errors.New("调整 SWAP 需要 root 权限")
	}
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	if err := ensureManagedSwapDir(); err != nil {
		return err
	}
	currentMB, err := currentSwapSizeMB()
	if err != nil {
		return fmt.Errorf("读取当前 SWAP 容量失败：%w", err)
	}
	if sizeMB < currentMB {
		return fmt.Errorf("不能执行：目标 SWAP 容量小于当前容量（当前约 %.1f GiB）", float64(currentMB)/1024)
	}
	if sizeMB == currentMB && sizeMB > 0 {
		return nil
	}
	sizeMB, err = managedSwapSizeForTotal(sizeMB)
	if err != nil {
		return err
	}
	active, err := managedSwapIsActive()
	if err != nil {
		return err
	}
	info, err := os.Lstat(managedSwapFile)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil && !info.Mode().IsRegular() {
		return errors.New("拒绝替换非普通文件的 Kokoro SWAP 文件")
	}

	if _, err := exec.LookPath("fallocate"); err != nil {
		return errors.New("系统缺少 fallocate，无法安全创建 SWAP 文件")
	}
	tmp := managedSwapFile + ".new"
	backup := managedSwapFile + ".old"
	for _, path := range []string{tmp, backup} {
		if fi, statErr := os.Lstat(path); statErr == nil {
			if !fi.Mode().IsRegular() {
				return fmt.Errorf("拒绝覆盖非普通文件：%s", path)
			}
			if path == backup {
				return errors.New("发现遗留的 swapfile.old，请先检查磁盘后再调整")
			}
			if err := os.Remove(path); err != nil {
				return err
			}
		} else if !errors.Is(statErr, fs.ErrNotExist) {
			return statErr
		}
	}
	defer os.Remove(tmp)
	var disk syscall.Statfs_t
	if err := syscall.Statfs(managedSwapDir, &disk); err != nil {
		return fmt.Errorf("读取 SWAP 磁盘剩余空间失败：%w", err)
	}
	if err := validateSwapDiskSpace(sizeMB, uint64(disk.Bavail)*uint64(disk.Bsize)); err != nil {
		return err
	}
	file, createErr := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if createErr != nil {
		return createErr
	}
	if err := file.Close(); err != nil {
		return err
	}
	bytes := int64(sizeMB) * 1024 * 1024
	if out, err := exec.CommandContext(ctx, "fallocate", "-l", fmt.Sprint(bytes), tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("预留 SWAP 磁盘空间失败：%s", commandError(out, err))
	}
	if err := os.Chmod(tmp, 0600); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "mkswap", tmp).CombinedOutput(); err != nil {
		return fmt.Errorf("初始化 SWAP 文件失败：%s", commandError(out, err))
	}
	if err := ensureManagedFstabCanBeWritten(); err != nil {
		return err
	}
	if active {
		if err := runSwapTool(ctx, "swapoff", managedSwapFile); err != nil {
			return fmt.Errorf("关闭现有 SWAP 失败（可能是内存不足以迁出 SWAP）：%w", err)
		}
	}
	hadOldFile := err == nil
	if hadOldFile {
		if err := os.Rename(managedSwapFile, backup); err != nil {
			if active {
				_ = runSwapTool(ctx, "swapon", managedSwapFile)
			}
			return err
		}
	}
	restoreOld := func() {
		_ = os.Remove(managedSwapFile)
		if hadOldFile {
			_ = os.Rename(backup, managedSwapFile)
			if active {
				_ = runSwapTool(ctx, "swapon", managedSwapFile)
			}
		}
	}
	if err := os.Rename(tmp, managedSwapFile); err != nil {
		if hadOldFile {
			_ = os.Rename(backup, managedSwapFile)
		}
		if active {
			_ = runSwapTool(ctx, "swapon", managedSwapFile)
		}
		return err
	}
	if err := runSwapTool(ctx, "swapon", managedSwapFile); err != nil {
		restoreOld()
		return fmt.Errorf("启用新 SWAP 失败：%w", err)
	}
	if err := writeManagedSwapFstab(); err != nil {
		_ = runSwapTool(ctx, "swapoff", managedSwapFile)
		restoreOld()
		return fmt.Errorf("写入 /etc/fstab 失败：%w", err)
	}
	if hadOldFile {
		_ = os.Remove(backup)
	}
	return nil
}

func currentSwapSizeMB() (int, error) {
	f, err := os.Open("/proc/swaps")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	var totalKiB int64
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) < 3 || fields[0] == "Filename" {
			continue
		}
		sizeKiB, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || sizeKiB < 0 {
			return 0, errors.New("无法读取现有 SWAP 容量")
		}
		totalKiB += sizeKiB
	}
	if err := s.Err(); err != nil {
		return 0, err
	}
	return int((totalKiB + 1023) / 1024), nil
}

func ensureManagedSwapDir() error {
	if info, err := os.Lstat(managedSwapDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Kokoro SWAP 状态路径不是普通目录")
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 {
			return errors.New("Kokoro SWAP 目录必须由 root 拥有")
		}
		return os.Chmod(managedSwapDir, 0700)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.MkdirAll(managedSwapDir, 0700)
}

func managedSwapIsActive() (bool, error) {
	f, err := os.Open("/proc/swaps")
	if err != nil {
		return false, err
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		fields := strings.Fields(s.Text())
		if len(fields) > 0 && fields[0] == managedSwapFile {
			return true, nil
		}
	}
	return false, s.Err()
}

func ensureManagedFstabCanBeWritten() error {
	data, err := os.ReadFile("/etc/fstab")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && fields[0] == managedSwapFile && !strings.Contains(line, swapFstabMark) {
			return errors.New("/etc/fstab 已有未标记的 Kokoro SWAP 路径，为避免覆盖手动配置已停止")
		}
	}
	return nil
}

func writeManagedSwapFstab() error {
	if err := ensureManagedFstabCanBeWritten(); err != nil {
		return err
	}
	path := "/etc/fstab"
	data, err := os.ReadFile(path)
	mode := fs.FileMode(0644)
	if err == nil {
		if info, statErr := os.Stat(path); statErr == nil {
			mode = info.Mode().Perm()
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	content := managedSwapFstabContent(string(data))
	tmp, err := os.CreateTemp("/etc", ".kokoro-fstab-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func runSwapTool(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", commandError(out, err))
	}
	return nil
}

func commandError(out []byte, err error) string {
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return msg
	}
	return err.Error()
}
