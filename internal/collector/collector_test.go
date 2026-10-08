package collector

import (
	"testing"
	"time"
)

func TestCPUUsageFrom(t *testing.T) {
	// 没有基线时不 panic，返回 0。
	if usage, perCore := cpuUsageFrom(nil, &cpuTicks{total: 100, idle: 50}); usage != 0 || perCore != nil {
		t.Fatalf("首次采样应为 0，got %v %v", usage, perCore)
	}

	prev := &cpuTicks{total: 1000, idle: 600, perCore: []coreTicks{{total: 500, idle: 300}}}
	cur := &cpuTicks{total: 1200, idle: 660, perCore: []coreTicks{{total: 700, idle: 360}}}
	usage, perCore := cpuUsageFrom(prev, cur)
	// 总：200 jiffies 里 140 忙 → 70%
	if diff := usage - 70; diff > 0.01 || diff < -0.01 {
		t.Fatalf("usage = %v, want 70", usage)
	}
	if len(perCore) != 1 {
		t.Fatalf("perCore len = %d, want 1", len(perCore))
	}
	// 单核：200 jiffies 里 140 忙 → 70%
	if diff := perCore[0] - 70; diff > 0.01 || diff < -0.01 {
		t.Fatalf("perCore[0] = %v, want 70", perCore[0])
	}

	// 计数回绕（cur < prev）不应产生负数或巨大值。
	if usage, _ := cpuUsageFrom(cur, prev); usage != 0 {
		t.Fatalf("计数回绕应为 0，got %v", usage)
	}
}

func TestNetRateFrom(t *testing.T) {
	now := time.Now()
	prev := map[string]ifaceCounters{"eth0": {name: "eth0", rx: 1000, tx: 500}}
	cur := []ifaceCounters{{name: "eth0", rx: 3000, tx: 1500}}

	up, down, ifaces := netRateFrom(prev, now.Add(-2*time.Second), cur, now)
	if len(ifaces) != 1 {
		t.Fatalf("ifaces len = %d, want 1", len(ifaces))
	}
	if down != 1000 { // (3000-1000)/2s
		t.Fatalf("down = %v, want 1000", down)
	}
	if up != 500 { // (1500-500)/2s
		t.Fatalf("up = %v, want 500", up)
	}
	if ifaces[0].TotalDown != 3000 || ifaces[0].TotalUp != 1500 {
		t.Fatalf("累计值不对: %+v", ifaces[0])
	}

	// 没有基线时速率为 0，但累计值仍然正确。
	up, down, ifaces = netRateFrom(nil, now, cur, now)
	if up != 0 || down != 0 {
		t.Fatalf("无基线速率应为 0，got up=%v down=%v", up, down)
	}
	if ifaces[0].TotalDown != 3000 {
		t.Fatalf("无基线累计值应保留: %+v", ifaces[0])
	}
}

func TestIsPhysicalDisk(t *testing.T) {
	yes := []string{"sda", "sdb", "sdaa", "vda", "hda", "xvda", "nvme0n1", "nvme1n2"}
	no := []string{"sda1", "sdaa2", "nvme0n1p1", "nvme0n1p3", "loop0", "ram0", "sr0", "fd0", "dm-0", "md0", "zram0", ""}
	for _, n := range yes {
		if !isPhysicalDisk(n) {
			t.Fatalf("%q 应被判为物理盘", n)
		}
	}
	for _, n := range no {
		if isPhysicalDisk(n) {
			t.Fatalf("%q 不应被判为物理盘", n)
		}
	}
}

func TestPctClamp(t *testing.T) {
	if v := pct(0, 0); v != 0 {
		t.Fatalf("pct(0,0) = %v, want 0", v)
	}
	if v := pct(150, 100); v != 100 {
		t.Fatalf("pct(150,100) = %v, want 100", v)
	}
	if v := pct(25, 100); v != 25 {
		t.Fatalf("pct(25,100) = %v, want 25", v)
	}
}
