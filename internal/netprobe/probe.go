// Package netprobe 提供网络质量探测能力。
//
// 设计目标：agent 跑在各地小鸡上，很多机器没装 ping 命令、也没有执行权限，
// 因此探测不依赖外部命令，而是直接用标准库发原生 ICMP Echo Request（需要 raw socket，
// Linux 上要 root 或 CAP_NET_RAW，agent 本来就以 root 运行）；
// 打不开 raw socket 时（权限不足 / Windows 非管理员 / 容器）自动降级为 TCP Ping。
//
// 只用标准库：golang.org/x/net/icmp 属于 x/net，不是标准库，所以 ICMP 报文的
// 构造与解析、RFC 1071 校验和全部在本包内手写。
package netprobe

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// 探测模式。
const (
	ModeICMP = "icmp" // 原生 ICMP Echo
	ModeTCP  = "tcp"  // TCP Ping 降级
)

const (
	// icmpEchoReply / icmpEchoRequest / icmpTimeExceeded 是 ICMPv4 报文类型。
	icmpEchoReply    = 0
	icmpEchoRequest  = 8
	icmpTimeExceeded = 11

	icmpHeaderLen = 8      // ICMPv4 首部长度
	echoDataLen   = 48     // Echo 数据负载长度（对齐常见 ping 的 56 字节报文体）
	recvBufLen    = 1500   // 收包缓冲，一个以太网 MTU 足够
	ipHeaderMin   = 20     // IPv4 首部最小长度
	echoIDMask    = 0xffff // ICMP Identifier 只有 16 位

	defaultCount   = 3                       // 每目标默认发几个包
	defaultTimeout = 1200 * time.Millisecond // 单次探测默认超时
	defaultWorkers = 8                       // 默认并发数
	maxRound       = 30 * time.Second        // 整轮探测总时长上限
	maxResolveWait = 2 * time.Second         // DNS 解析超时
	maxHopsDefault = 30                      // traceroute 默认最大跳数
	hopTimeout     = 2 * time.Second         // traceroute 每跳等待超时
)

// tcpPorts 是 TCP 降级时依次尝试的端口。
// 443/80 是通用 Web 端口；53 是为 DNS 类探测点（本表绝大多数点都是 DNS 服务器）留的最后退路，
// 注意 DNS 主机通常只开 UDP/53，TCP/53 也常常不通，所以 TCP 模式对本表命中率很低。
var tcpPorts = []int{443, 80, 53}

// icmpUnavailable 是降级事实的固定描述，会写进 Result.Error。
const icmpUnavailable = "icmp unavailable, tcp fallback"

// errUnsupportedTTL 是 setTTL 在不支持的平台上的返回错误（traceroute 因此不可用）。
var errUnsupportedTTL = errors.New("当前平台不支持设置 IP_TTL，traceroute 不可用")

// echoIDVal 是本机 ICMP Identifier：进程 PID 的低 16 位。
var echoIDVal = uint16(os.Getpid() & echoIDMask)

// seqCounter 全局递增的 ICMP Sequence Number。
// 每个目标用独立 socket，但 Linux 的 raw socket 会收到本机**所有** ICMP 回包，
// 因此序号必须进程内唯一，才能把回包与请求对上。
var seqCounter uint32

func echoID() uint16 { return echoIDVal }

func nextSeq() uint16 { return uint16(atomic.AddUint32(&seqCounter, 1)) }

// Result 单个探测点的探测结果。
type Result struct {
	Target    Target  `json:"target"`
	OK        bool    `json:"ok"`
	LatencyMS float64 `json:"latency_ms,omitempty"` // 平均往返延迟
	JitterMS  float64 `json:"jitter_ms,omitempty"`  // 抖动：延迟样本的标准差
	LossPct   float64 `json:"loss_pct,omitempty"`   // 丢包率 0-100
	Error     string  `json:"error,omitempty"`      // 失败原因或降级说明
}

// ResultsPayload 是 agent 回传给 Hub 的探测结果。
type ResultsPayload struct {
	NodeID  string   `json:"node_id"`
	Ts      int64    `json:"ts"`   // 毫秒 Unix 时间戳
	Mode    string   `json:"mode"` // icmp | tcp
	Results []Result `json:"results"`
}

// Prober 是探测器接口。
type Prober interface {
	// Probe 对一组目标做探测；count 每个目标发几个包，timeout 单次超时。
	// 返回的切片与 targets 一一对应（顺序一致），不会因为某个目标失败而缺失。
	Probe(ctx context.Context, targets []Target, count int, timeout time.Duration) []Result
	// Mode 返回当前探测模式：icmp | tcp。
	Mode() string
}

// prober 是 Prober 的默认实现。
type prober struct {
	mode   string // icmp | tcp
	reason string // 降级原因，仅用于排查
}

// New 创建探测器：优先原生 ICMP，raw socket 不可用时降级为 TCP Ping。
func New() (Prober, error) {
	p := &prober{}
	if canRawICMP() {
		if c, err := openICMP(); err == nil {
			_ = c.Close()
			p.mode = ModeICMP
			return p, nil
		} else {
			p.reason = err.Error()
		}
	} else {
		p.reason = "raw socket 不可用（无 raw ICMP 权限）"
	}
	p.mode = ModeTCP
	return p, nil
}

// Mode 返回探测模式。
func (p *prober) Mode() string { return p.mode }

// Reason 返回降级原因（仅用于日志排查，成功使用 ICMP 时为空）。
func (p *prober) Reason() string { return p.reason }

// Probe 并发探测一组目标。
func (p *prober) Probe(ctx context.Context, targets []Target, count int, timeout time.Duration) []Result {
	if ctx == nil {
		ctx = context.Background()
	}
	if count <= 0 {
		count = defaultCount
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	out := make([]Result, len(targets))
	if len(targets) == 0 {
		return out
	}

	// 整轮总时长上限，防止目标很多时无限拖长。
	ctx, cancel := context.WithTimeout(ctx, maxRound)
	defer cancel()

	workers := defaultWorkers
	if len(targets) < workers {
		workers = len(targets)
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				if err := ctx.Err(); err != nil {
					out[i] = Result{Target: targets[i], Error: err.Error()}
					continue
				}
				out[i] = p.probeOne(ctx, targets[i], count, timeout)
			}
		}()
	}
	for i := range targets {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	return out
}

// probeOne 探测单个目标：解析 -> ICMP（或降级 TCP）-> 统计。
func (p *prober) probeOne(ctx context.Context, t Target, count int, timeout time.Duration) Result {
	ip, err := resolveIPv4(ctx, t.Host)
	if err != nil {
		return Result{Target: t, Error: "resolve: " + err.Error()}
	}

	// ICMP 模式下每个目标单独建 socket，个别目标建 socket 失败时也会退化到 TCP。
	if p.mode == ModeICMP {
		samples, fails, ierr := icmpPing(ctx, ip, count, timeout)
		if ierr == nil {
			mean, jitter, loss := stat(samples, fails+len(samples))
			return Result{
				Target:    t,
				OK:        len(samples) > 0,
				LatencyMS: mean,
				JitterMS:  jitter,
				LossPct:   loss,
			}
		}
	}

	// TCP 降级。
	samples, fails, derr := tcpPing(ctx, ip, count, timeout)
	if derr != nil {
		return Result{Target: t, Error: icmpUnavailable + ": " + derr.Error()}
	}
	mean, jitter, loss := stat(samples, fails+len(samples))
	return Result{
		Target:    t,
		OK:        len(samples) > 0,
		LatencyMS: mean,
		JitterMS:  jitter,
		LossPct:   loss,
		Error:     icmpUnavailable,
	}
}

// ---- ICMP ----

// openICMP 打开 raw ICMP socket。失败通常意味着权限不足或平台不支持。
func openICMP() (net.PacketConn, error) {
	c, err := net.ListenPacket("ip4:icmp", "")
	if err != nil {
		return nil, err
	}
	return c, nil
}

// echoRequest 构造一条 ICMPv4 Echo Request（type 8, code 0）。
func echoRequest(seq uint16) []byte {
	msg := make([]byte, icmpHeaderLen+echoDataLen)
	msg[0] = icmpEchoRequest
	msg[1] = 0
	// 校验和字段先置 0 再算
	binary.BigEndian.PutUint16(msg[4:6], echoID())
	binary.BigEndian.PutUint16(msg[6:8], seq)
	// 负载里放纳秒时间戳，方便将来核对回包是否真的对应本次请求
	binary.BigEndian.PutUint64(msg[8:16], uint64(time.Now().UnixNano()))
	binary.BigEndian.PutUint16(msg[2:4], checksum(msg))
	return msg
}

// checksum 按 RFC 1071 计算 16 位反码和（ICMP 校验和）。
func checksum(b []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(b); i += 2 {
		sum += uint32(b[i])<<8 | uint32(b[i+1])
	}
	if len(b)%2 == 1 {
		sum += uint32(b[len(b)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = (sum & 0xffff) + (sum >> 16)
	}
	return ^uint16(sum)
}

// icmpPing 对单个 IPv4 地址发 count 个 Echo Request，返回成功的 RTT 样本与失败次数。
// 每个目标独立开一个 socket，这样并发 worker 之间不会互相干扰。
func icmpPing(ctx context.Context, ip net.IP, count int, timeout time.Duration) ([]time.Duration, int, error) {
	c, err := openICMP()
	if err != nil {
		return nil, 0, err
	}
	defer c.Close()

	dst := &net.IPAddr{IP: ip}
	samples := make([]time.Duration, 0, count)
	fails := 0
	for i := 0; i < count; i++ {
		if err := ctx.Err(); err != nil {
			fails += count - i
			break
		}
		seq := nextSeq()
		start := time.Now()
		if _, err := c.WriteTo(echoRequest(seq), dst); err != nil {
			fails++
			continue
		}
		if err := c.SetReadDeadline(start.Add(timeout)); err != nil {
			fails++
			continue
		}
		if _, err := awaitEcho(c, start, ip, seq); err != nil {
			fails++
			continue
		}
		samples = append(samples, time.Since(start))
	}
	return samples, fails, nil
}

// PingOnce 单次探测：优先 ICMP，失败降级 TCP Ping。返回往返耗时。
func PingOnce(ctx context.Context, host string, timeout time.Duration) (time.Duration, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	ip, err := resolveIPv4(ctx, host)
	if err != nil {
		return 0, err
	}

	if c, err := openICMP(); err == nil {
		defer c.Close()
		seq := nextSeq()
		start := time.Now()
		if _, err := c.WriteTo(echoRequest(seq), &net.IPAddr{IP: ip}); err != nil {
			return 0, err
		}
		if err := c.SetReadDeadline(start.Add(timeout)); err != nil {
			return 0, err
		}
		return awaitEcho(c, start, ip, seq)
	}

	// 降级：TCP Ping。
	d := &net.Dialer{Timeout: timeout}
	for _, port := range tcpPorts {
		start := time.Now()
		conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
		if err != nil {
			continue
		}
		conn.Close()
		return time.Since(start), nil
	}
	return 0, errors.New("icmp 不可用，且 TCP 降级探测（443/80/53）全部失败")
}

// awaitEcho 一直读到与 (identifier, seq, ip) 匹配的 Echo Reply，返回其 RTT。
// raw socket 会收到本机所有 ICMP 报文（别人的 ping、Time Exceeded 等），不匹配的直接丢弃。
func awaitEcho(c net.PacketConn, start time.Time, want net.IP, seq uint16) (time.Duration, error) {
	buf := make([]byte, recvBufLen)
	for {
		n, from, err := c.ReadFrom(buf)
		if err != nil {
			return 0, err
		}
		if !sameIP(from, want) {
			continue
		}
		m := stripIPHeader(buf[:n])
		if len(m) < icmpHeaderLen || m[0] != icmpEchoReply {
			continue
		}
		if binary.BigEndian.Uint16(m[4:6]) != echoID() || binary.BigEndian.Uint16(m[6:8]) != seq {
			continue
		}
		return time.Since(start), nil
	}
}

// ---- TCP 降级 ----

// tcpPing 对目标依次尝试 tcpPorts，选中第一个能连上的端口后测 count 次握手耗时。
func tcpPing(ctx context.Context, ip net.IP, count int, timeout time.Duration) ([]time.Duration, int, error) {
	d := &net.Dialer{Timeout: timeout}
	for _, port := range tcpPorts {
		addr := net.JoinHostPort(ip.String(), strconv.Itoa(port))
		samples := make([]time.Duration, 0, count)
		fails := 0
		ok := false
		for i := 0; i < count; i++ {
			if err := ctx.Err(); err != nil {
				break
			}
			start := time.Now()
			conn, err := d.DialContext(ctx, "tcp", addr)
			if err != nil {
				fails++
				break // 这个端口不可用，换下一个
			}
			conn.Close()
			samples = append(samples, time.Since(start))
			ok = true
		}
		if ok {
			return samples, fails, nil
		}
	}
	return nil, 0, errors.New("tcp 握手全部失败（443/80/53）")
}

// ---- 辅助 ----

// resolveIPv4 解析目标地址，返回第一个 IPv4 地址。Host 本身是 IPv4 时直接返回。
func resolveIPv4(ctx context.Context, host string) (net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4, nil
		}
		return nil, errors.New("暂不支持 IPv6 目标")
	}
	rctx, cancel := context.WithTimeout(ctx, maxResolveWait)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(rctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if v4 := a.IP.To4(); v4 != nil {
			return v4, nil
		}
	}
	return nil, errors.New("无 IPv4 解析结果")
}

// stripIPHeader 去掉报文前可能存在的 IPv4 首部，返回 ICMP 报文部分。
// Linux/Windows 的 raw socket 收包都带 IP 首部；datagram socket 不带，这里两种情况都兼容。
func stripIPHeader(b []byte) []byte {
	if len(b) >= ipHeaderMin && b[0]>>4 == 4 {
		ihl := int(b[0]&0x0f) * 4
		if ihl >= ipHeaderMin && ihl <= len(b) {
			return b[ihl:]
		}
	}
	return b
}

// sameIP 判断回包来源是否为期待的目标地址。
func sameIP(addr net.Addr, want net.IP) bool {
	ipAddr, ok := addr.(*net.IPAddr)
	if !ok {
		return false
	}
	return ipAddr.IP.Equal(want)
}

// stat 由样本算出 平均延迟 / 抖动（总体标准差）/ 丢包率。单位：毫秒、百分比。
// total 是本次探测的总发包数（成功样本 + 失败数）。
func stat(samples []time.Duration, total int) (mean, jitter, loss float64) {
	if total <= 0 {
		return 0, 0, 0
	}
	loss = float64(total-len(samples)) / float64(total) * 100
	if len(samples) == 0 {
		return 0, 0, round3(loss)
	}
	ms := make([]float64, len(samples))
	var sum float64
	for i, s := range samples {
		ms[i] = float64(s) / float64(time.Millisecond)
		sum += ms[i]
	}
	mean = sum / float64(len(ms))
	if len(ms) > 1 {
		var v float64
		for _, x := range ms {
			d := x - mean
			v += d * d
		}
		jitter = math.Sqrt(v / float64(len(ms)))
	}
	return round3(mean), round3(jitter), round3(loss)
}

func round3(x float64) float64 { return math.Round(x*1000) / 1000 }

// ---- Traceroute ----

// Hop 是 traceroute 的一跳。
type Hop struct {
	TTL     int     `json:"ttl"`
	IP      string  `json:"ip,omitempty"`
	RTTMS   float64 `json:"rtt_ms,omitempty"`
	Timeout bool    `json:"timeout,omitempty"` // true 表示该跳未收到任何响应
}

// Traceroute 用 TTL 递增 + ICMP Time Exceeded 做粗粒度去程探测。
// 依赖 raw socket 且需要能设置 IP_TTL，非 Linux 平台返回不支持错误。
func Traceroute(ctx context.Context, host string, maxHops int) ([]Hop, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if maxHops <= 0 {
		maxHops = maxHopsDefault
	}
	ip, err := resolveIPv4(ctx, host)
	if err != nil {
		return nil, err
	}
	c, err := openICMP()
	if err != nil {
		return nil, err
	}
	defer c.Close()

	dst := &net.IPAddr{IP: ip}
	hops := make([]Hop, 0, maxHops)
	for ttl := 1; ttl <= maxHops; ttl++ {
		if err := ctx.Err(); err != nil {
			return hops, err
		}
		if err := setTTL(c, ttl); err != nil {
			return hops, err
		}
		seq := nextSeq()
		start := time.Now()
		if _, err := c.WriteTo(echoRequest(seq), dst); err != nil {
			hops = append(hops, Hop{TTL: ttl, Timeout: true})
			continue
		}
		if err := c.SetReadDeadline(start.Add(hopTimeout)); err != nil {
			hops = append(hops, Hop{TTL: ttl, Timeout: true})
			continue
		}
		hop, done, err := awaitHop(c, start, ip, seq)
		if err != nil {
			hops = append(hops, Hop{TTL: ttl, Timeout: true})
			continue
		}
		hop.TTL = ttl
		hops = append(hops, hop)
		if done {
			return hops, nil
		}
	}
	return hops, nil
}

// awaitHop 读取一跳的响应：Time Exceeded 记下中间路由；Echo Reply 表示到达目标。
func awaitHop(c net.PacketConn, start time.Time, want net.IP, seq uint16) (Hop, bool, error) {
	buf := make([]byte, recvBufLen)
	for {
		n, from, err := c.ReadFrom(buf)
		if err != nil {
			return Hop{}, false, err
		}
		m := stripIPHeader(buf[:n])
		if len(m) < icmpHeaderLen {
			continue
		}
		rtt := round3(float64(time.Since(start)) / float64(time.Millisecond))
		switch m[0] {
		case icmpTimeExceeded:
			// 报文体里嵌着原始 IP 首部 + 原始 ICMP 前 8 字节，从中取 identifier/seq 做匹配。
			if id, sq, ok := embeddedEcho(m); !ok || id != echoID() || sq != seq {
				continue
			}
			return Hop{IP: addrIP(from), RTTMS: rtt}, false, nil
		case icmpEchoReply:
			if !sameIP(from, want) {
				continue
			}
			if binary.BigEndian.Uint16(m[4:6]) != echoID() || binary.BigEndian.Uint16(m[6:8]) != seq {
				continue
			}
			return Hop{IP: addrIP(from), RTTMS: rtt}, true, nil
		}
	}
}

// embeddedEcho 从 Time Exceeded 报文的负载里取出被丢弃报文的 identifier 与 sequence。
func embeddedEcho(m []byte) (id, seq uint16, ok bool) {
	body := m[icmpHeaderLen:]
	if len(body) < ipHeaderMin {
		return 0, 0, false
	}
	ihl := int(body[0]&0x0f) * 4
	if body[0]>>4 != 4 || ihl < ipHeaderMin || len(body) < ihl+icmpHeaderLen {
		return 0, 0, false
	}
	echo := body[ihl:]
	return binary.BigEndian.Uint16(echo[4:6]), binary.BigEndian.Uint16(echo[6:8]), true
}

func addrIP(a net.Addr) string {
	if ipAddr, ok := a.(*net.IPAddr); ok {
		return ipAddr.IP.String()
	}
	return a.String()
}
