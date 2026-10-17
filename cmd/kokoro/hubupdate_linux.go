//go:build linux

package main

import (
	"os"
	"syscall"
)

// hubPlatformSupported 面板自更新只在 Linux + systemd 上有意义。
func hubPlatformSupported() bool {
	return true
}

func runningAsRoot() bool {
	return os.Geteuid() == 0
}

// fileOwnership 取出文件的 uid/gid，替换二进制后要原样保留。
func fileOwnership(info os.FileInfo) (uid, gid int, ok bool) {
	stat, isStat := info.Sys().(*syscall.Stat_t)
	if !isStat {
		return 0, 0, false
	}
	return int(stat.Uid), int(stat.Gid), true
}

func chownPath(path string, uid, gid int) error {
	return os.Chown(path, uid, gid)
}
