//go:build !linux

package main

import "os"

// 面板自更新依赖 systemd 与 Linux 文件语义；非 Linux 上直接判为不支持，
// 这样同一份代码在开发机（Windows/macOS）上也能编译与测试。
func hubPlatformSupported() bool {
	return false
}

func runningAsRoot() bool {
	return false
}

func fileOwnership(os.FileInfo) (uid, gid int, ok bool) {
	return 0, 0, false
}

func chownPath(string, int, int) error {
	return nil
}
