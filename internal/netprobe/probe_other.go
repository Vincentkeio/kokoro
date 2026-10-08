//go:build !linux

package netprobe

import "net"

// canRawICMP 判断本进程能否打开 raw ICMP socket。
//
// 非 Linux 平台（Windows / macOS / BSD）没有统一的 capability 查询方式：
// Windows 上 raw socket 通常要管理员权限，macOS/BSD 要 root，但都没有 /proc 可读。
// 所以这里一律返回 true，由 openICMP 的实际返回值决定走 ICMP 还是 TCP 降级。
func canRawICMP() bool { return true }

// setTTL 设置 raw socket 的 IP_TTL。
//
// 非 Linux 平台：Windows 的 SOCK_RAW 不支持 IP_TTL（返回 WSAEINVAL），
// macOS/BSD 的常量取值又与 Linux 不同，统一按不支持处理，traceroute 直接返回错误。
// TODO：如果将来要支持 Windows/macOS 的 traceroute，可按 GOOS 在这里分支实现
// （Windows/BSD 的 IP_TTL 均为 4，IPPROTO_IP 均为 0）；
// 但目前 Windows 的 raw ICMP socket 多数场景拿不到管理员权限，优先级不高。
func setTTL(c net.PacketConn, ttl int) error { return errUnsupportedTTL }
