//go:build linux

package netprobe

import (
	"bufio"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// canRawICMP 判断本进程能否打开 raw ICMP socket。
//
// Linux 上 raw socket 需要 root（uid 0）或 CAP_NET_RAW（capability 13）。
// agent 以 root 运行，所以正常情况返回 true；这里提前判断是为了避免在没有权限时
// 逐个目标反复失败、浪费整轮探测时间。
func canRawICMP() bool {
	if os.Geteuid() == 0 {
		return true
	}
	return hasCapNetRaw()
}

// hasCapNetRaw 读 /proc/self/status 的 CapEff，检查 CAP_NET_RAW（第 13 位）是否置位。
func hasCapNetRaw() bool {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "CapEff:") {
			continue
		}
		hexStr := strings.TrimSpace(strings.TrimPrefix(line, "CapEff:"))
		mask, err := strconv.ParseUint(hexStr, 16, 64)
		if err != nil {
			return false
		}
		const capNetRaw = 13
		return mask&(1<<capNetRaw) != 0
	}
	return false
}

// setTTL 设置 raw socket 的 IP_TTL，用于 traceroute 逐跳探测。
func setTTL(c net.PacketConn, ttl int) error {
	ipc, ok := c.(*net.IPConn)
	if !ok {
		return errUnsupportedTTL
	}
	raw, err := ipc.SyscallConn()
	if err != nil {
		return err
	}
	var serr error
	if err := raw.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_TTL, ttl)
	}); err != nil {
		return err
	}
	return serr
}
