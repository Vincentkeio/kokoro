package hub

// 三网分省探测的范围配置。
//
// 2026-10-11 boss：内置表是「每个省 × 电信/联通/移动」100 多个点，
// 30 分钟一轮全探，流量和意义都不划算。所以改成：
//   - **默认只探三网基线**（上海电信/联通/移动三个省级 DNS，
//     2026-10-12 起基线省从北京换上海）—— 明细页「电信/联通/移动」
//     三张卡就靠它；
//   - 分省**按需勾选**：可全选、可反选、可全不选、可点选几个。
//     通不通以**本机实测**为准（后台每个省标注实测通率）；
//   - **连续探测**（2026-10-12 起默认）：一轮打完歇几秒就打下一轮，
//     不再等 30 分钟；间隔可调（勾几十个省时再加大）；
//   - **即点即生效**：保存后各台机器秒级切到新范围，不用重启、
//     不用等下一轮（agent 重启后也会被自愈纠正，见 netqTargetsCommand）。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/netprobe"
)

// settingNetQProvinces 存省份选择（JSON 数组）。
const settingNetQProvinces = "netq_provinces"

// settingNetQCatalog 存**覆盖用**的探测点目录（netprobe.CatalogFile 的 JSON）。
// 没设过 = 用二进制里内置的表（构建时从社区数据 vendor 进来的那份）。
const settingNetQCatalog = "netq_catalog"
const settingNetQCatalogLastAttempt = "netq_catalog_last_attempt"

const netqCatalogRefreshInterval = 24 * time.Hour

// netqUpstreamURL 是「更新探测点列表」按钮拉取的地址。
// 项目：https://github.com/spiritLHLS/icmp_targets
//
//	「闭源爬虫解析 CN 全国三大运营商**可用 ICMP** 的 Ping 地址，每日更新」
const netqUpstreamURL = "https://raw.githubusercontent.com/spiritLHLS/icmp_targets/main/nodes.json"

// netqCatalogOnce 保证「库里覆盖件 → netprobe」只装一次。
var netqCatalogOnce sync.Once

// applyStoredNetQCatalog 首次用到时，把库里的覆盖目录装进 netprobe。
// 没覆盖件就什么都不做（内置表生效）。
func (h *Hub) applyStoredNetQCatalog() {
	netqCatalogOnce.Do(func() {
		raw, err := h.store.GetSetting(settingNetQCatalog)
		if err != nil || strings.TrimSpace(raw) == "" {
			return
		}
		var f netprobe.CatalogFile
		if err := json.Unmarshal([]byte(raw), &f); err != nil {
			log.Printf("[hub] netq_catalog 解析失败，退回内置表: %v", err)
			return
		}
		f.Targets = netprobe.PreserveBuiltinIPv6(f.Targets)
		netprobe.SetCatalog(f.Targets)
		log.Printf("[hub] 已加载覆盖探测点目录：%d 个点（抓取于 %s）", len(f.Targets), f.FetchedAt)
	})
}

// netqCatalogCommand 在下发「探测范围」之外，顺带处理**目录覆盖**：
// agent 上报自己目录的版本号，与 Hub 的（覆盖件）不一致就发一条 netq_catalog。
// 没有覆盖件时永远不发 —— 内置表各机器自带，不需要走网络。
func (h *Hub) netqCatalogCommand(reportedVer string) *model.Command {
	raw, err := h.store.GetSetting(settingNetQCatalog)
	if err != nil || strings.TrimSpace(raw) == "" {
		return nil // 没有覆盖件
	}
	h.applyStoredNetQCatalog()
	var file netprobe.CatalogFile
	if err := json.Unmarshal([]byte(raw), &file); err != nil {
		return nil
	}
	file.Targets = netprobe.PreserveBuiltinIPv6(file.Targets)
	if reportedVer == netprobe.CatalogVersion() {
		return nil
	}
	encoded, err := json.Marshal(file)
	if err != nil {
		return nil
	}
	return &model.Command{
		ID:      "netqcat-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Type:    "netq_catalog",
		Payload: map[string]any{"catalog": string(encoded)},
	}
}

// handleAdminNetQRefresh 从上游社区项目拉最新的「可用 ICMP」表并落库
// （POST /admin/netq/refresh）。落库后 Hub 会按版本号把目录推给各台探针
// —— 所以**不用重新构建/部署**就能换表。
func (h *Hub) handleAdminNetQRefresh(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		drainBody(w, r)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	file, err := h.refreshNetQCatalog(r.Context())
	if err != nil {
		h.netqRefreshFail(w, err.Error())
		return
	}
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"targets":   len(file.Targets),
			"fetchedAt": file.FetchedAt,
			"version":   netprobe.CatalogVersion(),
		})
		return
	}
	http.Redirect(w, r, "/admin#nodes", http.StatusSeeOther)
}

// Successful and failed attempts both reserve the next 24-hour interval.
// Automatic failures are deliberately silent and leave the current table intact.
func (h *Hub) netqCatalogRefreshLoop(ctx context.Context) {
	var lastAttempt int64
	for ctx.Err() == nil {
		raw, _ := h.store.GetSetting(settingNetQCatalogLastAttempt)
		stored, _ := strconv.ParseInt(raw, 10, 64)
		if stored > lastAttempt {
			lastAttempt = stored
		}
		wait := time.Duration(0)
		if lastAttempt > 0 {
			wait = time.Until(time.UnixMilli(lastAttempt).Add(netqCatalogRefreshInterval))
			if wait > netqCatalogRefreshInterval {
				wait = netqCatalogRefreshInterval
			}
		}
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				continue
			}
		}
		lastAttempt = time.Now().UnixMilli()
		_, _ = h.refreshNetQCatalog(ctx)
	}
}

func (h *Hub) refreshNetQCatalog(parent context.Context) (netprobe.CatalogFile, error) {
	var file netprobe.CatalogFile
	if !h.netqCatalogRefreshMu.TryLock() {
		return file, fmt.Errorf("探测点列表正在更新，请稍后重试")
	}
	defer h.netqCatalogRefreshMu.Unlock()
	if err := h.store.SetSetting(settingNetQCatalogLastAttempt, strconv.FormatInt(time.Now().UnixMilli(), 10)); err != nil {
		return file, err
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, netqUpstreamURL, nil)
	if err != nil {
		return file, fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("User-Agent", "kokoro-hub/1")
	resp, err := client.Do(req)
	if err != nil {
		return file, fmt.Errorf("拉取上游失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return file, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (4<<20)+1))
	if err != nil {
		return file, fmt.Errorf("读取上游数据失败：%w", err)
	}
	if len(body) > 4<<20 {
		return file, fmt.Errorf("探测点列表超过大小上限")
	}

	targets, err := netprobe.ParseUpstreamNodes(body)
	if err != nil {
		return file, fmt.Errorf("解析失败：%w", err)
	}

	targets = netprobe.PreserveBuiltinIPv6(targets)
	file = netprobe.CatalogFile{
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
		Source:    netqUpstreamURL,
		Targets:   targets,
	}
	buf, err := json.Marshal(file)
	if err != nil {
		return file, fmt.Errorf("序列化失败：%w", err)
	}
	if err := h.store.SetSetting(settingNetQCatalog, string(buf)); err != nil {
		return file, fmt.Errorf("保存失败：%w", err)
	}
	netprobe.SetCatalog(targets)
	log.Printf("[hub] 探测点已更新：%d 个点（来源 %s）", len(targets), netqUpstreamURL)

	return file, nil
}

func (h *Hub) netqRefreshFail(w http.ResponseWriter, msg string) {
	if w.Header().Get("Content-Type") != "" {
		http.Error(w, msg, http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": msg})
}

// netqDefaultProvinces 是默认探测范围：只探上海的三网基线。
//
// 选上海是 boss 2026-10-12 定的：三网 PING 的速度以上海三节点为准，
// 其他省默认不勾（按需开）。北京是 2026-10-11 的旧默认，仅存注释备查。
var netqDefaultProvinces = []string{"上海"}

// netqProvinces 返回当前生效的省份选择（已按内置表校验、去重、排序）。
// 读不出来/没设过时退回默认（三网基线）——探测配置坏了不该让探测停摆。
func (h *Hub) netqProvinces() []string {
	raw, err := h.store.GetSetting(settingNetQProvinces)
	if err != nil || strings.TrimSpace(raw) == "" {
		return append([]string(nil), netqDefaultProvinces...)
	}
	var list []string
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		log.Printf("[hub] netq_provinces 解析失败，退回默认: %v", err)
		return append([]string(nil), netqDefaultProvinces...)
	}
	return requiredNetQProvinces(list)
}

// Shanghai is the mandatory baseline, including for legacy empty selections.
func requiredNetQProvinces(list []string) []string {
	clean := sanitizeNetQProvinces(list)
	for _, p := range clean {
		if p == "上海" {
			return clean
		}
	}
	return append([]string{"上海"}, clean...)
}

// sanitizeNetQProvinces 去掉不在内置表里的省份、去重、排序。
func sanitizeNetQProvinces(in []string) []string {
	known := make(map[string]bool)
	for _, p := range netprobe.Provinces() {
		known[p] = true
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || !known[p] || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	order := make(map[string]int, len(known))
	for i, p := range netprobe.Provinces() {
		order[p] = i
	}
	sort.Slice(out, func(i, j int) bool { return order[out[i]] < order[out[j]] })
	return out
}

// netqSaveProvinces 校验并保存范围，上海基准始终保留。
func (h *Hub) netqSaveProvinces(list []string) ([]string, error) {
	clean := requiredNetQProvinces(list)
	buf, err := json.Marshal(clean)
	if err != nil {
		return nil, err
	}
	if err := h.store.SetSetting(settingNetQProvinces, string(buf)); err != nil {
		return nil, err
	}
	return clean, nil
}

// netqTargetCount 按当前选择算出探测点数（省数 × 3，方便后台显示工作量）。
func netqTargetCount(provinces []string) int {
	return len(provinces) * 3
}

// netqIntervalKey 是探测间隔的设置键（分钟）。
//
//	 0 = 连续探测（一轮打完歇几秒就打下一轮）
//	-1 = 关闭探测
//	>0 = 固定间隔分钟
const settingNetQInterval = "netq_interval_min"

// netqDefaultIntervalMin 是没配置时的默认探测间隔（分钟）。
//
// 2026-10-16 由"连续模式(0)"改为 5 分钟：连续探测把 net_quality 写爆了
// （实测约 1 轮/分钟 × 省×网 行，30 天保留在单核小机上撑不住，库光这一项就 167 MiB）。
const netqDefaultIntervalMin = 5

// netqInterval 读取探测间隔设置（分钟）。没设过 = 默认 5 分钟。
func (h *Hub) netqInterval() int {
	raw, err := h.store.GetSetting(settingNetQInterval)
	if err != nil {
		return netqDefaultIntervalMin
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < -1 {
		return netqDefaultIntervalMin
	}
	return n
}

// netqTargetsCommand 生成一条「探测范围」命令；范围没变就返回 nil。
//
// reported 是 agent 上一次上报里带的当前范围。为什么靠上报比对而不是
// Hub 记账：agent 重启会丢掉内存里的选择，Hub 记的「已告知」就成了谎言；
// 让 agent 每 2 秒的上报带上自己的范围，Hub 只做比对，天然自愈 ——
// 保存后/agent 重启后/Hub 重启后，两条路径都能在几秒内收敛。
//
// 间隔（interval_min）随命令一起带：agent 侧只在**范围变化**时才收命令，
// 所以单独改间隔时也让范围"看起来没变也发一次"不方便 —— 现在的做法是
// 间隔和范围一起存、一起下发，改动范围时顺带把间隔带上；单独改间隔的
// 场景（范围没动）由 reported 比对覆盖不到，所以 reported 同时带上
// agent 当前间隔，一起比对（见 api.go 调用处）。
func (h *Hub) netqTargetsCommand(reported []string, reportedInterval int, families ...string) *model.Command {
	want := h.netqProvinces()
	desiredFamily, reportedFamily := "v4", "v4"
	if len(families) > 0 && families[0] == "v6" {
		desiredFamily = "v6"
	}
	if len(families) > 1 && families[1] != "" {
		reportedFamily = families[1]
	}
	if sameStringSet(want, reported) && h.netqInterval() == reportedInterval && desiredFamily == reportedFamily {
		return nil
	}
	payload := make([]any, 0, len(want))
	for _, p := range want {
		payload = append(payload, p)
	}
	return &model.Command{
		ID:   "netq-" + strconv.FormatInt(time.Now().UnixNano(), 10),
		Type: "netq_targets",
		Payload: map[string]any{
			"provinces":    payload,
			"interval_min": h.netqInterval(),
			"ip_version":   desiredFamily,
		},
	}
}

// sameStringSet 比对两个字符串集合（忽略顺序与重复）。
func sameStringSet(a, b []string) bool {
	m := make(map[string]int, len(a))
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
	}
	for _, n := range m {
		if n != 0 {
			return false
		}
	}
	return true
}

// ---- 后台保存 ----

// handleAdminNetQ 保存三网分省探测范围（POST /admin/netq）。
// 表单字段：provinces（可多选，同名校验框）+ interval_min（探测间隔）。
func (h *Hub) handleAdminNetQ(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		drainBody(w, r)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "表单解析失败", http.StatusBadRequest)
		return
	}
	saved, err := h.netqSaveProvinces(r.Form["provinces"])
	if err != nil {
		log.Printf("[hub] 保存探测范围失败: %v", err)
		if wantsJSON(r) {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "保存失败"})
			return
		}
		http.Error(w, "保存失败", http.StatusInternalServerError)
		return
	}
	// 探测间隔：0=连续（默认）、-1=关、正数=分钟。非法值按连续处理，
	// 但记日志 —— 表单是 number 控件，非法值只会来自手搓请求。
	interval := 0
	if raw := strings.TrimSpace(r.FormValue("interval_min")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < -1 {
			log.Printf("[hub] 探测间隔值非法: %q，按连续处理", raw)
		} else {
			interval = n
		}
	}
	if err := h.store.SetSetting(settingNetQInterval, strconv.Itoa(interval)); err != nil {
		log.Printf("[hub] 保存探测间隔失败: %v", err)
	}
	log.Printf("[hub] 三网探测范围已更新: %v（%d 个点，间隔 %s，各机器将在数秒内生效）",
		saved, netqTargetCount(saved), netqIntervalLabel(interval))
	if wantsJSON(r) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":        true,
			"provinces": saved,
			"targets":   netqTargetCount(saved),
			"interval":  interval,
		})
		return
	}
	http.Redirect(w, r, "/admin#nodes", http.StatusSeeOther)
}

// netqIntervalLabel 给日志/界面用的间隔文案。
func netqIntervalLabel(min int) string {
	switch {
	case min < 0:
		return "关闭"
	case min == 0:
		return "连续"
	default:
		return fmt.Sprintf("%d 分钟", min)
	}
}

// wantsJSON 判断这次请求是不是后台的 fetch（要 JSON 而不是重定向）。
// 与 web.go 里其它后台处理器保持同一口径。
func wantsJSON(r *http.Request) bool {
	return r.Header.Get("X-Requested-With") == "XMLHttpRequest" || r.FormValue("json") == "1"
}
