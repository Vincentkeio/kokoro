package netprobe

// 可替换的探测点目录。
//
// 内置表（Targets）是**构建时**打进来的兜底；后台「更新探测点列表」按钮
// 会用线上拉到的社区数据覆盖它 —— 覆盖件存库里、经命令通道推给各台探针，
// 所以更新探测点**不需要重新构建和部署**。
//
// 覆盖只在内存/进程内生效（agent 重启后由 Hub 按版本号自愈补发，见
// agent 侧 netq_catalog 命令与 hub 侧的 catalog 版本比对）。

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
)

var (
	catalogMu sync.RWMutex
	// activeCatalog 为 nil 表示用内置表（Targets）。
	activeCatalog []Target
)

// PreserveBuiltinIPv6 appends the embedded IPv6 targets when a legacy Hub
// override contains only IPv4. Existing IPv4 overrides remain authoritative.
func PreserveBuiltinIPv6(targets []Target) []Target {
	for _, t := range targets {
		if normalizeIPVersion(t.IPVersion) == "v6" {
			return targets
		}
	}
	out := append([]Target(nil), targets...)
	for _, t := range Targets {
		if normalizeIPVersion(t.IPVersion) == "v6" {
			out = append(out, t)
		}
	}
	return out
}

// SetCatalog 用外部目录覆盖内置表。传 nil/空 = 退回内置表。
func SetCatalog(ts []Target) {
	clean := make([]Target, 0, len(ts))
	for _, t := range ts {
		if t.Province == "" || t.ISP == "" || t.Host == "" {
			continue
		}
		if len(t.Hosts) == 0 {
			t.Hosts = []string{t.Host}
		}
		clean = append(clean, t)
	}
	sort.SliceStable(clean, func(i, j int) bool {
		if clean[i].Province != clean[j].Province {
			return clean[i].Province < clean[j].Province
		}
		if normalizeIPVersion(clean[i].IPVersion) != normalizeIPVersion(clean[j].IPVersion) {
			return normalizeIPVersion(clean[i].IPVersion) < normalizeIPVersion(clean[j].IPVersion)
		}
		return ispRank[clean[i].ISP] < ispRank[clean[j].ISP]
	})
	catalogMu.Lock()
	activeCatalog = clean
	catalogMu.Unlock()
}

// Catalog 返回当前生效的探测点目录（覆盖生效时是覆盖件，否则是内置表）。
func Catalog() []Target {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	if len(activeCatalog) > 0 {
		return activeCatalog
	}
	return Targets
}

// CatalogVersion 返回当前目录的短版本号（内容哈希）。
//
// 用途：agent 把它带在每条上报里，Hub 一比对就知道这台机器用的是不是最新
// 目录 —— 不一致就补发一次；一致就不发。agent 重启后靠这条自愈。
func CatalogVersion() string {
	h := sha256.New()
	for _, t := range Catalog() {
		h.Write([]byte(t.Province))
		h.Write([]byte{0})
		h.Write([]byte(t.ISP))
		h.Write([]byte{0})
		h.Write([]byte(normalizeIPVersion(t.IPVersion)))
		h.Write([]byte{0})
		for _, x := range t.Hosts {
			h.Write([]byte(x))
			h.Write([]byte{0})
		}
		h.Write([]byte{1})
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}
