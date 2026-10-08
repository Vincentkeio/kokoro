// Package collector 采集一次完整的机器指标快照，输出 *model.Metrics。
//
// 设计约定：
//  1. 只用标准库，不引入 gopsutil 之类第三方依赖（控制二进制体积）。
//  2. 容错优先：任何单项采集失败都只记日志并省略该字段，不让 Collect 整体失败；
//     只有 CPU / 内存 / 磁盘 / 网络四项核心指标全部失败才返回 error。
//  3. CPU 使用率与网络速率必须靠两次采样的差分得出，因此采集器内部保存上一次的
//     累计计数（首次调用没有基线，返回 0 是可接受的）。
//
// 平台相关的原子函数（readCPUTicks / readIfaceCounters / MemStat / DiskStat /
// LoadAvg / Uptime / ProcCount / ConnCount / Temps / HostInfo）分别在
// collector_linux.go 与 collector_other.go 中实现。
package collector

import (
	"errors"
	"log"
	"regexp"
	"sync"
	"time"

	"github.com/kokoro-probe/kokoro/internal/model"
)

// logf 统一日志前缀，方便运维 grep。
func logf(format string, args ...any) {
	log.Printf("[collector] "+format, args...)
}

// Collector 采集一次完整快照。实现内部保存上一次的 CPU 与网卡计数，
// 以便算出两次采集之间的使用率与速率。
type Collector interface {
	Collect() (*model.Metrics, error)
	Close() error
}

// New 创建与当前平台匹配的采集器。
func New() (Collector, error) { return newCollector() }

// ---- 采样状态 ----

// cpuTicks 是一次 CPU 采样结果，记录累计 jiffies。
type cpuTicks struct {
	total   uint64 // 所有状态的时间之和
	idle    uint64 // idle + iowait
	perCore []coreTicks
}

// coreTicks 是单个逻辑核的累计 jiffies。
type coreTicks struct {
	total uint64
	idle  uint64
}

// ifaceCounters 是一张网卡的累计收发字节数。
type ifaceCounters struct {
	name string
	rx   int64 // 下载（接收）字节
	tx   int64 // 上传（发送）字节
}

// cpuState 保存上一次 CPU 采样，用于差分。
type cpuState struct {
	last *cpuTicks
	at   time.Time
}

// netState 保存上一次网卡采样，用于差分。
type netState struct {
	last map[string]ifaceCounters
	at   time.Time
}

// ioState 保存上一次磁盘 IO 累计字节数，用于差分。
type ioState struct {
	lastRead  int64
	lastWrite int64
	has       bool
	at        time.Time
}

// physicalDiskRe 匹配「整块物理盘」的设备名：只认 sd/vd/hd/xvd 与 nvme 的整盘，
// 自动排除分区（sda1、nvme0n1p1）、loop、ram、sr、dm-*、md 等，避免重复计数。
var physicalDiskRe = regexp.MustCompile(`^(?:sd[a-z]+|vd[a-z]+|hd[a-z]+|xvd[a-z]+|nvme[0-9]+n[0-9]+)$`)

// isPhysicalDisk 判断 /proc/diskstats 里的设备名是否是整块物理盘。
func isPhysicalDisk(name string) bool { return physicalDiskRe.MatchString(name) }

// sampleCPUInto 读取当前累计值并与 s 中保存的上一次做差分，同时更新 s。
// 首次调用（没有基线）返回 0 与 nil 错误。
func sampleCPUInto(s *cpuState, now time.Time) (float64, []float64, error) {
	cur, err := readCPUTicks()
	if err != nil {
		return 0, nil, err
	}
	prev := s.last
	s.last = &cur
	s.at = now
	if prev == nil {
		return 0, nil, nil
	}
	usage, perCore := cpuUsageFrom(prev, &cur)
	return usage, perCore, nil
}

// sampleNetInto 读取当前网卡计数并与 s 中保存的上一次做差分，同时更新 s。
// 首次调用（没有基线）速率为 0，但累计总量仍然正确。
func sampleNetInto(s *netState, now time.Time) (model.NetStat, error) {
	cur, err := readIfaceCounters()
	if err != nil {
		return model.NetStat{}, err
	}
	prev, prevAt := s.last, s.at
	next := make(map[string]ifaceCounters, len(cur))
	var totalUp, totalDown int64
	for _, i := range cur {
		totalUp += i.tx
		totalDown += i.rx
		next[i.name] = i
	}
	s.last = next
	s.at = now

	up, down, ifaces := netRateFrom(prev, prevAt, cur, now)
	return model.NetStat{
		Up:        up,
		Down:      down,
		TotalUp:   totalUp,
		TotalDown: totalDown,
		Ifaces:    ifaces,
	}, nil
}

// sampleIOInto 读取当前磁盘累计读写字节数并与 s 中保存的上一次做差分，同时更新 s。
// 返回的 bool 表示是否算出了有效速率：首次采样没有基线，此时应省略该字段。
func sampleIOInto(s *ioState, now time.Time) (model.IOStat, bool, error) {
	read, write, err := readDiskIOCounters()
	if err != nil {
		return model.IOStat{}, false, err
	}
	prevRead, prevWrite, has, prevAt := s.lastRead, s.lastWrite, s.has, s.at
	s.lastRead, s.lastWrite, s.has, s.at = read, write, true, now
	if !has {
		return model.IOStat{}, false, nil
	}
	secs := now.Sub(prevAt).Seconds()
	if secs <= 0 {
		return model.IOStat{}, false, nil
	}
	var st model.IOStat
	if read >= prevRead {
		st.Read = int64(float64(read-prevRead) / secs)
	}
	if write >= prevWrite {
		st.Write = int64(float64(write-prevWrite) / secs)
	}
	return st, true, nil
}

// cpuUsageFrom 由前后两次累计 jiffies 算出总使用率与每核使用率（百分比）。
// 计数回绕或没有增量时安全返回 0。
func cpuUsageFrom(prev, cur *cpuTicks) (float64, []float64) {
	if prev == nil || cur == nil || cur.total <= prev.total {
		return 0, nil
	}
	total := cur.total - prev.total
	idle := cur.idle - prev.idle
	if idle > total {
		idle = total
	}
	usage := pct(total-idle, total)

	n := len(cur.perCore)
	if len(prev.perCore) < n {
		n = len(prev.perCore)
	}
	perCore := make([]float64, 0, n)
	for i := 0; i < n; i++ {
		dt := cur.perCore[i].total - prev.perCore[i].total
		if cur.perCore[i].total <= prev.perCore[i].total || dt == 0 {
			perCore = append(perCore, 0)
			continue
		}
		di := cur.perCore[i].idle - prev.perCore[i].idle
		if di > dt {
			di = dt
		}
		perCore = append(perCore, pct(dt-di, dt))
	}
	return usage, perCore
}

// netRateFrom 由前后两次网卡计数算出速率（B/s）与每网卡明细。
func netRateFrom(prev map[string]ifaceCounters, prevAt time.Time, cur []ifaceCounters, now time.Time) (up, down int64, ifaces []model.IfaceStat) {
	secs := now.Sub(prevAt).Seconds()
	if prev == nil || secs <= 0 {
		secs = 0
	}
	for _, c := range cur {
		st := model.IfaceStat{
			Name:      c.name,
			TotalUp:   c.tx,
			TotalDown: c.rx,
		}
		if secs > 0 {
			if p, ok := prev[c.name]; ok {
				if c.tx >= p.tx {
					st.Up = int64(float64(c.tx-p.tx) / secs)
				}
				if c.rx >= p.rx {
					st.Down = int64(float64(c.rx-p.rx) / secs)
				}
			}
		}
		up += st.Up
		down += st.Down
		ifaces = append(ifaces, st)
	}
	return up, down, ifaces
}

// pct 计算 part/all 的百分比并夹在 [0,100]。
func pct(part, all uint64) float64 {
	if all == 0 {
		return 0
	}
	v := float64(part) / float64(all) * 100
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// ---- 采集器实现 ----

type collector struct {
	mu     sync.Mutex
	closed bool
	cpu    cpuState
	net    netState
	io     ioState
}

func (c *collector) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.cpu.last = nil
	c.net.last = nil
	c.io.has = false
	return nil
}

// Collect 采集一次完整快照。Seq 由调用方（agent）填充，这里保持 0。
func (c *collector) Collect() (*model.Metrics, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("collector: closed")
	}

	now := time.Now()
	m := &model.Metrics{
		V:  model.ProtocolVersion,
		Ts: now.UnixMilli(),
	}

	// 核心项失败计数：四项全失败才认为这次采集彻底失败。
	coreFail := 0

	// --- CPU ---
	usage, perCore, err := sampleCPUInto(&c.cpu, now)
	if err != nil {
		coreFail++
		logf("cpu: %v", err)
	} else {
		m.CPU.Usage = usage
		m.CPU.PerCore = perCore
	}
	if l1, l5, l15, err := LoadAvg(); err != nil {
		logf("loadavg: %v", err)
	} else {
		m.CPU.Load1, m.CPU.Load5, m.CPU.Load15 = l1, l5, l15
	}

	// --- 内存 ---
	if mem, err := MemStat(); err != nil {
		coreFail++
		logf("mem: %v", err)
	} else {
		m.Mem = mem
	}

	// --- 磁盘 ---
	if disk, parts, err := DiskStat(); err != nil {
		coreFail++
		logf("disk: %v", err)
	} else {
		m.Disk = disk
		if len(parts) > 0 {
			m.Parts = parts
		}
	}

	// --- 网络 ---
	if ns, err := sampleNetInto(&c.net, now); err != nil {
		coreFail++
		logf("net: %v", err)
	} else {
		m.Net = ns
	}

	if coreFail >= 4 {
		return nil, errors.New("collector: 核心指标 cpu/mem/disk/net 全部采集失败")
	}

	// --- 磁盘 IO（可选项，读不到就省略，不算核心失败）---
	if st, ok, err := sampleIOInto(&c.io, now); err != nil {
		logf("io: %v", err)
	} else if ok {
		m.IO = &st
	}

	// --- 主机 ---
	if up, err := Uptime(); err != nil {
		logf("uptime: %v", err)
	} else {
		m.Host.Uptime = up
	}
	if n, err := ProcCount(); err != nil {
		logf("procs: %v", err)
	} else {
		m.Host.Procs = n
	}

	// --- 连接数（可选项，失败省略）---
	if tcp, udp, err := ConnCount(); err != nil {
		logf("conn: %v", err)
	} else {
		m.Conn = &model.ConnStat{TCP: tcp, UDP: udp}
	}

	// --- 温度（可选项，机器没有传感器是常态，失败静默省略）---
	if temps, err := Temps(); err != nil {
		logf("temp: %v", err)
	} else if len(temps) > 0 {
		m.Temp = &model.TempStat{Values: temps}
	}

	return m, nil
}

// ---- 可独立调用的原子函数（CPU/网络用包级状态做差分）----

var (
	pkgMu  sync.Mutex
	pkgCPU cpuState
	pkgNet netState
	pkgIO  ioState
)

// CPUUsage 返回整机 CPU 使用率（%）与每核使用率（%）。
// 首次调用没有基线，返回 0 是可接受的。
func CPUUsage() (float64, []float64, error) {
	pkgMu.Lock()
	defer pkgMu.Unlock()
	return sampleCPUInto(&pkgCPU, time.Now())
}

// NetStat 返回汇总的网络速率与累计流量。
// 首次调用速率为 0，累计总量仍然正确。
func NetStat() (model.NetStat, error) {
	pkgMu.Lock()
	defer pkgMu.Unlock()
	return sampleNetInto(&pkgNet, time.Now())
}

// DiskIO 返回磁盘读写速率（B/s）。首次调用没有基线，返回 0。
func DiskIO() (model.IOStat, error) {
	pkgMu.Lock()
	defer pkgMu.Unlock()
	st, _, err := sampleIOInto(&pkgIO, time.Now())
	return st, err
}
