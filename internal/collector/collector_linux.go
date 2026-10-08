//go:build linux

package collector

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/kokoro-probe/kokoro/internal/model"
)

// newCollector 创建 Linux 采集器。
func newCollector() (Collector, error) { return &collector{}, nil }

// ---- CPU ----

// readCPUTicks 解析 /proc/stat，返回累计 jiffies。
func readCPUTicks() (cpuTicks, error) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return cpuTicks{}, err
	}
	defer f.Close()

	var t cpuTicks
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "cpu") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		name := fields[0]
		// guest 已计入 user，这里只取前 8 个字段（user nice system idle iowait irq softirq steal）。
		var vals [8]uint64
		n := 0
		for i := 1; i < len(fields) && n < len(vals); i++ {
			v, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				n = 0
				break
			}
			vals[n] = v
			n++
		}
		if n < 5 {
			continue
		}
		var total uint64
		for i := 0; i < n; i++ {
			total += vals[i]
		}
		idle := vals[3] + vals[4] // idle + iowait
		if name == "cpu" {
			t.total = total
			t.idle = idle
			continue
		}
		if isAllDigits(name[3:]) {
			t.perCore = append(t.perCore, coreTicks{total: total, idle: idle})
		}
	}
	if err := sc.Err(); err != nil {
		return cpuTicks{}, err
	}
	if t.total == 0 && len(t.perCore) == 0 {
		return cpuTicks{}, errors.New("no cpu line in /proc/stat")
	}
	return t, nil
}

// ---- 网络 ----

// readIfaceCounters 解析 /proc/net/dev，返回除 lo 之外每张网卡的累计收发字节数。
func readIfaceCounters() ([]ifaceCounters, error) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []ifaceCounters
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue // 跳过表头
		}
		name := strings.TrimSpace(line[:idx])
		if name == "" || name == "lo" {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		// 接收段 9 个字段后才是发送段，tx 字节在偏移 8。
		if len(fields) < 9 {
			continue
		}
		rx, err1 := strconv.ParseInt(fields[0], 10, 64)
		tx, err2 := strconv.ParseInt(fields[8], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, ifaceCounters{name: name, rx: rx, tx: tx})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, errors.New("no network interface in /proc/net/dev")
	}
	return out, nil
}

// ---- 磁盘 IO ----

// sectorSize 是 /proc/diskstats 统计所用的扇区大小（字节）。
const sectorSize = 512

// readDiskIOCounters 解析 /proc/diskstats，返回整块物理盘的累计读写字节数。
// 只统计 sd/vd/hd/xvd 与 nvme 整盘，分区与 loop/ram/dm/md 等一律忽略，避免重复计数。
func readDiskIOCounters() (int64, int64, error) {
	f, err := os.Open("/proc/diskstats")
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	var readSectors, writeSectors int64
	found := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		// 字段（1-based）：1 major 2 minor 3 设备名 4 读完成 5 读合并 6 读扇区
		// ... 8 写完成 9 写合并 10 写扇区
		fields := strings.Fields(sc.Text())
		if len(fields) < 10 {
			continue
		}
		if !isPhysicalDisk(fields[2]) {
			continue
		}
		r, err1 := strconv.ParseInt(fields[5], 10, 64) // 字段 6：读扇区数
		w, err2 := strconv.ParseInt(fields[9], 10, 64) // 字段 10：写扇区数
		if err1 != nil || err2 != nil {
			continue
		}
		readSectors += r
		writeSectors += w
		found = true
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	if !found {
		return 0, 0, errors.New("no physical disk in /proc/diskstats")
	}
	return readSectors * sectorSize, writeSectors * sectorSize, nil
}

// ---- 内存 ----

// MemStat 解析 /proc/meminfo。used = total - available。
func MemStat() (model.MemStat, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return model.MemStat{}, err
	}
	kv := parseKeyValues(string(data))

	total := kv["MemTotal"]
	if total <= 0 {
		return model.MemStat{}, errors.New("MemTotal missing in /proc/meminfo")
	}
	available := kv["MemAvailable"]
	if available <= 0 {
		// 老内核没有 MemAvailable，退化用 free + buffers + cached。
		available = kv["MemFree"] + kv["Buffers"] + kv["Cached"]
	}
	used := total - available
	if used < 0 {
		used = 0
	}
	swapUsed := kv["SwapTotal"] - kv["SwapFree"]
	if swapUsed < 0 {
		swapUsed = 0
	}
	return model.MemStat{
		Total:     total * 1024,
		Used:      used * 1024,
		Cached:    kv["Cached"] * 1024,
		SwapTotal: kv["SwapTotal"] * 1024,
		SwapUsed:  swapUsed * 1024,
	}, nil
}

// parseKeyValues 解析 "Key: 123 kB" 形式的文本，值统一为 int64。
func parseKeyValues(text string) map[string]int64 {
	out := make(map[string]int64, 32)
	for _, line := range strings.Split(text, "\n") {
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		fields := strings.Fields(line[idx+1:])
		if len(fields) == 0 {
			continue
		}
		v, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		out[strings.TrimSpace(line[:idx])] = v
	}
	return out
}

// ---- 磁盘 ----

// skipFSTypes 是伪文件系统，不计入磁盘总量。
var skipFSTypes = map[string]bool{
	"tmpfs":       true,
	"devtmpfs":    true,
	"ramfs":       true,
	"proc":        true,
	"sysfs":       true,
	"cgroup":      true,
	"cgroup2":     true,
	"overlay":     true,
	"overlayfs":   true,
	"squashfs":    true,
	"devpts":      true,
	"mqueue":      true,
	"binfmt_misc": true,
	"securityfs":  true,
	"debugfs":     true,
	"tracefs":     true,
	"configfs":    true,
	"hugetlbfs":   true,
	"pstore":      true,
	"efivarfs":    true,
	"autofs":      true,
	"nsfs":        true,
	"bpf":         true,
	"rpc_pipefs":  true,
	"selinuxfs":   true,
	"fusectl":     true,
}

// DiskStat 遍历 /proc/mounts 的真实物理挂载点，返回合计容量与每挂载点明细。
// 同一设备号只统计一次，避免 docker overlay 之类重复计数。
func DiskStat() (model.DiskStat, []model.PartStat, error) {
	mounts, err := readMounts()
	if err != nil {
		return model.DiskStat{}, nil, err
	}

	seen := make(map[syscall.Fsid]bool, len(mounts))
	var total, used int64
	var parts []model.PartStat

	for _, mnt := range mounts {
		var st syscall.Statfs_t
		if err := syscall.Statfs(mnt, &st); err != nil {
			continue
		}
		if seen[st.Fsid] {
			continue
		}
		seen[st.Fsid] = true

		bsize := uint64(st.Bsize)
		if bsize == 0 {
			bsize = uint64(st.Frsize)
		}
		if bsize == 0 {
			continue
		}
		t := int64(st.Blocks * bsize)
		if t <= 0 {
			continue
		}
		// 非特权用户可见的可用空间（Bavail*bsize），已用 = 总量 - 可用。
		avail := int64(st.Bavail * bsize)
		u := t - avail
		if u < 0 {
			u = 0
		}
		total += t
		used += u
		parts = append(parts, model.PartStat{Mount: mnt, Total: t, Used: u})
	}
	if len(parts) == 0 {
		return model.DiskStat{}, nil, errors.New("no physical mount point found")
	}
	return model.DiskStat{Total: total, Used: used}, parts, nil
}

// readMounts 读取 /proc/mounts，过滤掉伪文件系统后返回挂载点列表。
func readMounts() ([]string, error) {
	f, err := os.Open("/proc/mounts")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var mounts []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 挂载点里如果有空格会被转义成 \040，不能整体用 Fields 切。
		fields := strings.Split(line, " ")
		if len(fields) < 3 {
			continue
		}
		if skipFSTypes[fields[2]] {
			continue
		}
		mounts = append(mounts, unescapeMount(fields[1]))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return mounts, nil
}

// unescapeMount 还原 mount 表里被转义的特殊字符。
func unescapeMount(s string) string {
	repl := []struct{ from, to string }{
		{`\040`, " "},
		{`\011`, "\t"},
		{`\012`, "\n"},
		{`\134`, `\`},
	}
	for _, r := range repl {
		s = strings.ReplaceAll(s, r.from, r.to)
	}
	return s
}

// ---- 负载 / 运行时间 ----

// LoadAvg 返回 1/5/15 分钟平均负载。
func LoadAvg() (float64, float64, float64, error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0, errors.New("unexpected /proc/loadavg format")
	}
	l1, err1 := strconv.ParseFloat(fields[0], 64)
	l5, err2 := strconv.ParseFloat(fields[1], 64)
	l15, err3 := strconv.ParseFloat(fields[2], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, 0, errors.New("unparsable /proc/loadavg")
	}
	return l1, l5, l15, nil
}

// Uptime 返回开机时长（秒）。
func Uptime() (int64, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, errors.New("unexpected /proc/uptime format")
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, err
	}
	return int64(v), nil
}

// ---- 进程 ----

// ProcCount 统计 /proc 下的数字目录个数。
func ProcCount() (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if isAllDigits(e.Name()) {
			n++
		}
	}
	if n == 0 {
		return 0, errors.New("no process dir under /proc")
	}
	return n, nil
}

// ---- 连接数 ----

// ConnCount 返回 TCP 与 UDP 的 inuse 连接数（IPv4 + IPv6 合并）。
func ConnCount() (int, int, error) {
	tcp, udp := 0, 0
	found := false
	for _, path := range []string{"/proc/net/sockstat", "/proc/net/sockstat6"} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 3 {
				continue
			}
			// 形如 "TCP: inuse 12 ..." / "UDP6: inuse 0 ..."
			proto := strings.TrimSuffix(fields[0], ":")
			proto = strings.TrimSuffix(proto, "6")
			if proto != "TCP" && proto != "UDP" {
				continue
			}
			inuse := 0
			for i := 1; i+1 < len(fields); i++ {
				if fields[i] == "inuse" {
					if v, err := strconv.Atoi(fields[i+1]); err == nil {
						inuse = v
					}
					break
				}
			}
			if proto == "TCP" {
				tcp += inuse
			} else {
				udp += inuse
			}
			found = true
		}
	}
	if !found {
		return 0, 0, errors.New("no TCP/UDP line in sockstat")
	}
	return tcp, udp, nil
}

// ---- 温度 ----

// Temps 读取 /sys/class/thermal/thermal_zone*/temp（毫摄氏度）。
// 没有传感器时返回空 map 与 nil 错误——缺失是可接受的。
func Temps() (map[string]float64, error) {
	paths, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	out := make(map[string]float64, len(paths))
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64)
		if err != nil {
			continue
		}
		out[filepath.Base(filepath.Dir(p))] = v / 1000
	}
	return out, nil
}

// ---- 主机信息 ----

// HostInfo 返回主机名、发行版、内核版本、架构与虚拟化类型。
func HostInfo() (hostname, osName, kernel, arch, virt string, err error) {
	hostname, err = os.Hostname()
	if err != nil {
		logf("hostname: %v", err)
		hostname = ""
		err = nil
	}
	osName = osPrettyName()
	kernel = kernelRelease()
	arch = runtime.GOARCH
	virt = detectVirt()
	if hostname == "" && osName == "" && kernel == "" {
		return hostname, osName, kernel, arch, virt, errors.New("host info unavailable")
	}
	return hostname, osName, kernel, arch, virt, nil
}

// osPrettyName 解析 /etc/os-release 的 PRETTY_NAME，缺则退回 ID。
func osPrettyName() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	name, id := "", ""
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if v, ok := unquoteValue(line, "PRETTY_NAME"); ok && name == "" {
			name = v
		}
		if v, ok := unquoteValue(line, "ID"); ok && id == "" {
			id = v
		}
	}
	if name != "" {
		return name
	}
	return id
}

// unquoteValue 取出 KEY=VALUE 形式的行，并去掉两边的引号。
func unquoteValue(line, key string) (string, bool) {
	if !strings.HasPrefix(line, key+"=") {
		return "", false
	}
	v := strings.TrimSpace(line[len(key)+1:])
	v = strings.Trim(v, `"'`)
	return v, v != ""
}

// kernelRelease 读 /proc/sys/kernel/osrelease（等价于 uname -r，且跨架构一致）。
func kernelRelease() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// virtKeywords 是虚拟化判定关键字，按优先级从具体到宽泛排列。
var virtKeywords = []struct {
	kw   string
	name string
}{
	{"docker", "docker"},
	{"kubepods", "kubernetes"},
	{"lxc", "lxc"},
	{"openvz", "openvz"},
	{"kvm", "kvm"},
	{"qemu", "qemu"},
	{"vmware", "vmware"},
	{"virtualbox", "virtualbox"},
	{"vbox", "virtualbox"},
	{"bochs", "bochs"},
	{"xen", "xen"},
	{"microsoft", "hyperv"},
	{"hyper-v", "hyperv"},
	{"openstack", "openstack"},
	{"bhyve", "bhyve"},
	{"parallels", "parallels"},
	{"container", "container"},
}

// detectVirt 粗判虚拟化类型，判不出来返回空串。
func detectVirt() string {
	var sources []string
	for _, p := range []string{
		"/sys/class/dmi/id/product_name",
		"/sys/class/dmi/id/sys_vendor",
		"/sys/class/dmi/id/product_version",
		"/proc/cpuinfo",
		"/proc/1/cgroup",
	} {
		if b, err := os.ReadFile(p); err == nil {
			sources = append(sources, string(b))
		}
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		sources = append(sources, "docker")
	}
	blob := strings.ToLower(strings.Join(sources, "\n"))
	for _, kv := range virtKeywords {
		if strings.Contains(blob, kv.kw) {
			return kv.name
		}
	}
	return ""
}

// ---- 小工具 ----

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
