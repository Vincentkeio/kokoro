package hub

// 首页信息面板的回归测试：时钟 / 统计卡 / 点阵地球 / 富卡片。
//
// 这些功能横跨「Go 组数据 → 模板渲染 → 前端脚本消费」三段，
// 只测 Go 侧或只看页面都可能漏（比如数据给了但模板没引用）。
// 所以这里统一断言**最终 HTML**。

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/store"
)

// mkNode 造一台带地理位置的小鸡。
func mkNode(t *testing.T, st *store.Store, name, country, region, city string) *model.Node {
	t.Helper()
	n := &model.Node{
		Name: name, Country: country, Region: region, City: city,
		Visibility: model.VisibilityPublic, CPUCores: 2,
		MemTotal: 4 << 30, DiskTotal: 40 << 30, OS: "Debian 13", Online: true,
		Tags: []string{"香港", "CN2"},
		// ⚠️ token_hash 上有唯一索引。不填的话两台机器都是空串，
		// 造第二台时会撞唯一约束 —— 报错还很像是"造节点失败"，
		// 容易以为是别的问题。
		TokenHash: "th_" + name,
	}
	if err := st.CreateNode(n); err != nil {
		t.Fatalf("造节点失败: %v", err)
	}
	return n
}

// seedMetrics 给节点塞一份指标，让卡片走「有数据」那条分支。
func seedMetrics(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	m := &model.Metrics{
		V: 1, Ts: time.Now().UnixMilli(),
		Host: model.HostStat{Uptime: 11 * 3600},
		CPU:  model.CPUStat{Usage: 12.5, Load1: 0.42},
		Mem:  model.MemStat{Total: 4 << 30, Used: 1 << 30},
		Disk: model.DiskStat{Total: 40 << 30, Used: 8 << 30},
		Net:  model.NetStat{Up: 464, Down: 988},
		NetQ: &model.NetQStat{HubLatencyMS: 42},
	}
	if err := st.InsertMetrics(nodeID, m); err != nil {
		t.Fatalf("写入指标失败: %v", err)
	}
}

// TestHomeHasClockAndStats 时钟条与总览卡必须渲染出来。
func TestHomeHasClockAndStats(t *testing.T) {
	h, st := newTestHub(t)
	mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))

	// 时钟只显示**主机自己的时间**：用 data-offset（固定 UTC 偏移）算，
	// 不再是本地/UTC/东京…那一排多时区。钟面文案是 UTC±HH:MM 偏移
	// （"主机时间"字样已按设计去掉，不再断言它）。
	for _, want := range []string{"hub-clock", "data-offset="} {
		if !strings.Contains(body, want) {
			t.Errorf("首页缺少主机时钟的 %s", want)
		}
	}
	// 「服务器总览」侧栏卡：机器数汇总 + 今日/本月流量。
	// 2026-10-17 起总览卡回归（boss 要求在这里也能看到总流量），
	// 旧的"整行拆掉、首页不许出现总览"约束随之作废。
	for _, want := range []string{"服务器总览", "台服务器", "点亮地区", "今日流量", "本月流量", "总流量"} {
		if !strings.Contains(body, want) {
			t.Errorf("首页缺少「%s」", want)
		}
	}
	// 探测点按 boss 要求删掉了
	if strings.Contains(body, "探测点") {
		t.Error("探测点应该已删除")
	}
	// hero 里不该再出现「N 台小鸡 / M 在线」
	if strings.Contains(body, "台小鸡</span>") {
		t.Error("hero 里不该再显示「台小鸡」，它和总览重复了")
	}
}

// TestHomeRichCard 富卡片：资源条带配色档位、规格表、标签。
func TestHomeRichCard(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	seedMetrics(t, st, n.ID)

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))

	for _, want := range []string{
		`class="gauges"`,       // 资源条容器
		`class="cpu lv-`,       // 条上必须带档位类，否则颜色不会随占用率变
		`class="specs"`,        // 规格表
		`class="tags"`,         // 自定义标签
		`>香港<`,                 // 标签内容
		`data-field="cpu-bar"`, // SSE 更新依赖这个钩子，掉了就变成静态页面
		`data-field="up"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("卡片缺少 %s", want)
		}
	}
	// 规格拆成独立行后各自成项，不再拼成 "2C / 4G / 40G"。
	// 标签是「核心数」而不是「CPU」—— CPU 那一行留给型号（独占整行）。
	// 这个测试节点没有 cpu_model，所以那行不渲染。
	for _, want := range []string{"<dt>核心数</dt>", "<dt>内存</dt>", "<dt>磁盘</dt>", ">2 核<"} {
		if !strings.Contains(body, want) {
			t.Errorf("规格表缺少 %s", want)
		}
	}
}

// TestHomeGlobeData 地球数据要真的塞进页面。
func TestHomeGlobeData(t *testing.T) {
	h, st := newTestHub(t)
	node := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	seedMetrics(t, st, node.ID)
	if err := st.SetSetting(settingHubLat, "34.05"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(settingHubLon, "-118.24"); err != nil {
		t.Fatal(err)
	}

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.Contains(body, `class="globe"`) {
		t.Fatal("有可定位的节点时应该渲染地球容器")
	}
	if !strings.Contains(body, "/static/land.bin?") {
		t.Error("地球容器应带上陆地掩码地址（含版本号）")
	}
	if !strings.Contains(body, "KokoroGlobe") {
		tail := body
		if len(tail) > 900 {
			tail = tail[len(tail)-900:]
		}
		t.Errorf("缺少地球初始化脚本；HTML 尾部=\n%s", tail)
	}

	// 解析嵌入的 JSON，确认坐标真的传下去了
	const open = `<script id="globe-data" type="application/json">`
	i := strings.Index(body, open)
	if i < 0 {
		t.Fatal("缺少 globe-data 脚本块")
	}
	rest := body[i+len(open):]
	j := strings.Index(rest, "</script>")
	var g globeData
	if err := json.Unmarshal([]byte(rest[:j]), &g); err != nil {
		t.Fatalf("地球 JSON 解析失败: %v", err)
	}
	if !g.HasHub {
		t.Error("配置了主机坐标，HasHub 应为真")
	}
	if len(g.Places) != 1 {
		t.Fatalf("应有 1 个节点，实际 %d", len(g.Places))
	}
	p := g.Places[0]
	if p.Key != node.ID {
		t.Errorf("节点 key = %q，应为 %q", p.Key, node.ID)
	}
	// 东京坐标
	if p.Lat < 35 || p.Lat > 36.5 || p.Lon < 139 || p.Lon > 141 {
		t.Errorf("东京坐标不合理: (%.2f, %.2f)", p.Lat, p.Lon)
	}
	// 不得把 HTML 转义后的内容写进 JSON（template.JS 的意义所在）
	if strings.Contains(rest[:j], "&#") {
		t.Error("地球 JSON 被 HTML 转义了，前端 JSON.parse 会失败")
	}
}

// TestHomeGlobeHiddenWithoutCoord 认不出坐标时不渲染地球。
//
// 宁可整块不显示，也不要把节点画到错误的位置上。
func TestHomeGlobeHiddenWithoutCoord(t *testing.T) {
	h, st := newTestHub(t)
	mkNode(t, st, "某处的小鸡", "", "不存在的地区", "")

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if strings.Contains(body, `class="globe"`) {
		t.Error("没有可定位的节点时不该渲染地球")
	}
	if strings.Contains(body, "globe-data") {
		t.Error("没有可定位的节点时不该输出地球数据")
	}
}

// TestHomeGlobeNoHubStillRenders 没配置主机坐标时：仍然显示地球，只是没有航线。
func TestHomeGlobeNoHubStillRenders(t *testing.T) {
	h, st := newTestHub(t)
	mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(body, `class="globe"`) {
		t.Fatal("有节点就该渲染地球")
	}
	const open = `<script id="globe-data" type="application/json">`
	i := strings.Index(body, open)
	rest := body[i+len(open):]
	var g globeData
	if err := json.Unmarshal([]byte(rest[:strings.Index(rest, "</script>")]), &g); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if g.HasHub {
		t.Error("没配置主机坐标时 HasHub 应为假")
	}
	if len(g.Places) != 1 {
		t.Errorf("节点仍应在列表里，实际 %d", len(g.Places))
	}
}

// ---- 纯函数 ----

func TestSparkPaths(t *testing.T) {
	// 数据不足：不出图，避免画一条贴底的假零线
	if l, a := sparkPaths(nil); l != "" || a != "" {
		t.Error("空序列不该产生路径")
	}
	if l, _ := sparkPaths([]store.NetPoint{{Down: 5}}); l != "" {
		t.Error("单点不该产生路径")
	}
	// 全零：同样不出图
	zero := []store.NetPoint{{}, {}, {}}
	if l, _ := sparkPaths(zero); l != "" {
		t.Error("全零序列不该产生路径")
	}
	// 正常序列
	pts := []store.NetPoint{{Up: 1, Down: 2}, {Up: 3, Down: 4}, {Up: 2, Down: 2}}
	line, area := sparkPaths(pts)
	if !strings.HasPrefix(line, "M") || !strings.Contains(line, "L") {
		t.Errorf("折线路径不合法: %q", line)
	}
	if !strings.HasSuffix(area, "Z") {
		t.Errorf("面积路径应闭合: %q", area)
	}
	// 点全在画布内
	for _, seg := range strings.Split(strings.TrimPrefix(line, "M"), "L") {
		var x, y float64
		if _, err := fmt.Sscanf(seg, "%f %f", &x, &y); err != nil {
			t.Fatalf("坐标解析失败 %q: %v", seg, err)
		}
		if x < 0 || x > sparkW || y < 0 || y > sparkH {
			t.Errorf("坐标越界: (%.1f, %.1f)", x, y)
		}
	}
}

func TestNetQWavePaths(t *testing.T) {
	// 单点 / 空 / 全平：不出图。全平画一条中线会被误读成"接口在抖"，
	// 而"只有一轮数据"用空白表达更诚实。
	if l, a := netqWavePaths(nil); l != "" || a != "" {
		t.Error("空序列不该产生路径")
	}
	if l, _ := netqWavePaths([]float64{50}); l != "" {
		t.Error("单点不该产生路径")
	}
	if l, _ := netqWavePaths([]float64{50, 50, 50}); l != "" {
		t.Error("全平序列不该产生路径（留缺口比画条中线诚实）")
	}

	// 正常序列：按自身 min~max 缩放，峰值贴顶区、谷值贴底区
	pts := []float64{100, 300, 200}
	line, area := netqWavePaths(pts)
	if !strings.HasPrefix(line, "M") || !strings.HasSuffix(area, "Z") {
		t.Fatalf("路径不合法: line=%q area=%q", line, area)
	}
	var ys []float64
	for _, seg := range strings.Split(strings.TrimPrefix(line, "M"), "L") {
		var x, y float64
		if _, err := fmt.Sscanf(seg, "%f %f", &x, &y); err != nil {
			t.Fatalf("坐标解析失败 %q: %v", seg, err)
		}
		if x < 0 || x > netqWaveW || y < 0 || y > netqWaveH {
			t.Errorf("坐标越界: (%.1f, %.1f)", x, y)
		}
		ys = append(ys, y)
	}
	// 100 是最小 → y 最大（靠近底部）；300 是最大 → y 最小（靠近顶部）
	if ys[0] <= ys[1] || ys[2] >= ys[0] {
		t.Errorf("缩放方向反了或没拉开: y = %v（期望 [大, 小, 中]）", ys)
	}
	// 上下各留 15% 余量：最值不该顶到 pad 边界
	if ys[1] < 2 || ys[1] > netqWaveH-2-(netqWaveH-4)*0.15 {
		t.Errorf("峰值太贴边了: %.1f", ys[1])
	}
}

// Missing renewal metadata must retain a visible placeholder with its own style.
func TestRenewalTagMissingAndFilled(t *testing.T) {
	for _, tc := range []struct {
		name, price, class, text string
	}{
		{"missing", "", `class="tag renewal-slot renew-empty"`, "¥- /年"},
		{"filled", "¥100/月", `class="tag renewal-slot"`, "¥100/月"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, st := newTestHub(t)
			n := mkNode(t, st, "续费标签", "JP", "日本", "")
			if err := st.SaveProfile(&model.NodeProfile{NodeID: n.ID, Price: tc.price}); err != nil {
				t.Fatal(err)
			}
			body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
			if !strings.Contains(body, tc.class) || !strings.Contains(body, tc.text) {
				t.Fatalf("renewal tag did not render its %s state", tc.name)
			}
			if tc.price != "" && strings.Contains(body, "renew-empty") {
				t.Fatal("a filled renewal tag must not use the empty state")
			}
		})
	}
}

// TestCardDropsRegionKeepsExpireTag 卡片第二行的地名删了，这一行留给标签；
// 到期是**必须的 tag**，文案「XX 天到期」；CPU 型号不带标签。
//
// boss 2026-10-11 三条一起报的，钉在一起：都是卡片头部那一块的排版。
func TestCardDropsRegionKeepsExpireTag(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	// 补一个 CPU 型号（mkNode 不带这个字段，节点已建那就地更新）
	n.CPUModel = "Intel(R) Xeon(R) Platinum 8272CL CPU @ 2.60GHz"
	if err := st.UpdateNode(n); err != nil {
		t.Fatalf("更新节点失败: %v", err)
	}
	expire := time.Now().AddDate(0, 0, 45).Format("2006-01-02")
	if err := st.SaveProfile(&model.NodeProfile{NodeID: n.ID, ExpireAt: expire}); err != nil {
		t.Fatal(err)
	}
	seedMetrics(t, st, n.ID)

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))

	// 1) 第二行的地名整行删掉（详情页还有，卡片上不再重复）
	if strings.Contains(body, `class="meta"`) {
		t.Error("卡片第二行的地名应已删除（这一行留给标签）")
	}
	// 2) 到期 tag：必须显示，文案是「XX 天到期」
	if !strings.Contains(body, "45 天到期") {
		t.Error("到期 tag 应显示「45 天到期」")
	}
	if strings.Contains(body, "剩 45 天") {
		t.Error("旧文案「剩 N 天」已改成「N 天到期」")
	}
	// 3) CPU 型号保留，但不带"CPU"标签
	if !strings.Contains(body, "Xeon Platinum 8272CL") {
		t.Error("CPU 型号内容要保留 —— 只删标签别删内容")
	}
	if strings.Contains(body, "<dt>CPU</dt>") {
		t.Error("型号行不该再有 <dt>CPU</dt> 标签")
	}
	// 型号行要落在 bare 的无标签格子里
	if !strings.Contains(body, `class="full bare "`) && !strings.Contains(body, `class="full bare"`) {
		t.Error("型号行应有 bare 标记（模板据此跳过 <dt>）")
	}
}

// TestNATShowsAsTag NAT 是卡片标签行的**固定 tag**，不在规格表里。
//
// boss 2026-10-11：规格表「网络」项删掉，NAT 挪到标签行。
// 端到端钉三件事：标签出现了、排在自定义标签前面、悬停有解释。
func TestNATShowsAsTag(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "香港小鸡", "HK", "中国 · 香港", "")
	n.NAT = true
	if err := st.UpdateNode(n); err != nil {
		t.Fatalf("更新节点失败: %v", err)
	}
	seedMetrics(t, st, n.ID)

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.Contains(body, `class="tag nat"`) {
		t.Error("NAT 机器应该有固定 tag「NAT」")
	}
	if !strings.Contains(body, "共享公网 IP") {
		t.Error("NAT tag 的悬停解释不能丢")
	}
	// 固定 tag 排在自定义标签前面：NAT 是机器属性，比"大盘鸡"更基本
	iNat := strings.Index(body, `class="tag nat"`)
	iCustom := strings.Index(body, ">CN2<") // mkNode 造机器时自带的自定义标签
	if iNat < 0 || iCustom < 0 || iNat > iCustom {
		t.Errorf("NAT tag 应排在自定义标签前面（nat=%d, 自定义=%d）", iNat, iCustom)
	}
	// 规格表里不能再有「网络」项
	if strings.Contains(body, "<dt>网络</dt>") {
		t.Error("规格表的「网络」项已删")
	}
}

func TestLevelClass(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "ok"}, {64.9, "ok"}, {65, "warn"}, {84.9, "warn"}, {85, "bad"}, {100, "bad"},
	}
	for _, c := range cases {
		if got := levelClass(c.in); got != c.want {
			t.Errorf("levelClass(%.1f) = %q，应为 %q", c.in, got, c.want)
		}
	}
}

func TestLatencyPct(t *testing.T) {
	if got := latencyPct(0); got != 0 {
		t.Errorf("0ms 应为 0，实际 %v", got)
	}
	if got := latencyPct(150); got != 50 {
		t.Errorf("150ms 应为 50，实际 %v", got)
	}
	// 超过 300ms 封顶，条不能溢出
	if got := latencyPct(9999); got != 100 {
		t.Errorf("超长延迟应封顶 100，实际 %v", got)
	}
}

func TestSpecText(t *testing.T) {
	// 三项分开给，缺项显示「—」而不是 "0C" 这种看着像数据错误的东西
	full := &model.Node{CPUCores: 2, MemTotal: 4 << 30, DiskTotal: 40 << 30}
	if got := specCPU(full.CPUCores); got != "2 核" {
		t.Errorf("specCPU = %q", got)
	}
	if specMem(full.MemTotal) == "—" || specDisk(full.DiskTotal) == "—" {
		t.Errorf("内存/磁盘不该为空: %q %q", specMem(full.MemTotal), specDisk(full.DiskTotal))
	}
	empty := &model.Node{}
	for name, got := range map[string]string{
		"CPU": specCPU(empty.CPUCores), "内存": specMem(empty.MemTotal), "磁盘": specDisk(empty.DiskTotal),
	} {
		if got != "—" {
			t.Errorf("空节点 %s 应为「—」，实际 %q", name, got)
		}
	}
}

func TestHomeSparklineFromAggregated(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	// 铺最近 40 分钟的原始点，跨过多个 5 分钟窗口
	now := time.Now().UnixMilli()
	var batch []*model.Metrics
	for i := 0; i < 9; i++ {
		batch = append(batch, &model.Metrics{
			V: 1, Ts: now - int64(40-i*5)*60*1000, Seq: int64(i),
			Host: model.HostStat{Uptime: 11 * 3600},
			CPU:  model.CPUStat{Usage: 10 + float64(i)},
			Mem:  model.MemStat{Total: 4 << 30, Used: 1 << 30},
			Disk: model.DiskStat{Total: 40 << 30, Used: 8 << 30},
			Net:  model.NetStat{Up: int64(1000 + i*500), Down: int64(2000 + i*900)},
		})
	}
	if err := st.InsertMetricsBatch(n.ID, batch); err != nil {
		t.Fatalf("写入原始指标失败: %v", err)
	}
	if err := st.Aggregate5m(); err != nil {
		t.Fatalf("聚合失败: %v", err)
	}
	series, err := st.RecentNetSeries(now - 60*60*1000)
	if err != nil {
		t.Fatalf("读曲线失败: %v", err)
	}
	if len(series[n.ID]) < 2 {
		t.Fatalf("聚合后应有多个窗口，实际 %d", len(series[n.ID]))
	}

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(body, `class="spark"`) {
		t.Error("有聚合数据时应该画出网络曲线")
	}
	if !strings.Contains(body, `class="sp-line" d="M`) {
		t.Error("曲线路径没生成")
	}
	if strings.Contains(body, "还没有足够的历史数据") {
		t.Error("有数据时不该显示「没有历史数据」占位")
	}
}

// ---- 全网速率卡：CPU / 内存平均使用率图 ----

// seedResSeries 给一台节点铺最近一小时里 3 个分钟桶的占用率。
// cpuBase + i 让三个桶的 CPU 依次抬一档；内存固定一个占比（used/total）。
func seedResSeries(t *testing.T, st *store.Store, nodeID string, cpuBase float64, used, total int64) {
	t.Helper()
	now := time.Now().UnixMilli()
	var batch []*model.Metrics
	for i := 0; i < 3; i++ {
		batch = append(batch, &model.Metrics{
			V: 1, Ts: now - int64(30-i*10)*60*1000, Seq: int64(i),
			CPU: model.CPUStat{Usage: cpuBase + float64(i), Load1: 0.1},
			Mem: model.MemStat{Total: total, Used: used},
			Net: model.NetStat{Up: 1000, Down: 2000},
		})
	}
	if err := st.InsertMetricsBatch(nodeID, batch); err != nil {
		t.Fatalf("写入指标失败: %v", err)
	}
}

// TestHomeResChartInSpeedCard 「全网速率」卡里的第二张图。
//
// 这条钉三件事，缺一件这张图就会静默坏掉：
//  1. 图真的渲染出来（模板钩子 + 纵轴标签在 HTML 里）；
//  2. 数据真的传到前端（res-data JSON 解析出来值和入库一致）；
//  3. 跨节点是「平均」不是「求和」—— 两台 CPU 60/40 的机器必须是 50。
func TestHomeResChartInSpeedCard(t *testing.T) {
	h, st := newTestHub(t)
	a := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	b := mkNode(t, st, "香港小鸡", "HK", "中国 · 香港", "")

	// A：CPU 10/11/12%，内存 25%（1G/4G）；B：CPU 20/21/22%，内存 75%（3G/4G）
	seedResSeries(t, st, a.ID, 10, 1<<30, 4<<30)
	seedResSeries(t, st, b.ID, 20, 3<<30, 4<<30)

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))

	for _, want := range []string{
		"CPU / 内存 平均使用率", // 小标题
		`class="res-chart-wrap"`,
		"data-res-chart",
		// 纵轴固定 0–100：占用率不按峰值自适应，否则两张图没法比
		"<span>100%</span>", "<span>50%</span>", "<span>0%</span>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("首页缺少占用率图的 %s", want)
		}
	}

	// 解析嵌入式 JSON，逐桶核对数值
	const open = `<script id="res-data" type="application/json">`
	i := strings.Index(body, open)
	if i < 0 {
		t.Fatal("缺少 res-data 脚本块")
	}
	rest := body[i+len(open):]
	j := strings.Index(rest, "</script>")
	var pts []resPointJSON
	if err := json.Unmarshal([]byte(rest[:j]), &pts); err != nil {
		t.Fatalf("占用率 JSON 解析失败: %v", err)
	}
	if len(pts) != 3 {
		t.Fatalf("应有 3 个分钟桶，实际 %d", len(pts))
	}
	for k, p := range pts {
		wantCPU := 15 + float64(k) // (10+k + 20+k) / 2
		if math.Abs(p.CPU-wantCPU) > 0.01 {
			t.Errorf("第 %d 个桶 CPU 平均 = %.2f，应为 %.2f（跨节点平均，不是求和）",
				k, p.CPU, wantCPU)
		}
		if math.Abs(p.Mem-50) > 0.01 {
			t.Errorf("第 %d 个桶内存平均 = %.2f，应为 50（25%% 与 75%% 的平均，与总量无关）",
				k, p.Mem)
		}
	}
	// 不得把 HTML 转义后的内容写进 JSON（template.JS 的意义所在）
	if strings.Contains(rest[:j], "&#") {
		t.Error("占用率 JSON 被 HTML 转义了，前端 JSON.parse 会失败")
	}

	// 图例均值：整段平均（16.0 / 50.0）
	if !strings.Contains(body, "CPU 平均 16.0%") {
		t.Error("图例缺少 CPU 窗口均值 16.0%")
	}
	if !strings.Contains(body, "内存 平均 50.0%") {
		t.Error("图例缺少内存窗口均值 50.0%")
	}
}

// TestDashResPointsAveragesAcrossNodes 跨节点求平均（不是求和）+ 空输入返回 nil。
func TestDashResPointsAveragesAcrossNodes(t *testing.T) {
	h, st := newTestHub(t)
	a := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")
	b := mkNode(t, st, "香港小鸡", "HK", "中国 · 香港", "")

	now := time.Now().UnixMilli()
	for _, n := range []struct {
		id  string
		cpu float64
		mem int64
	}{
		{a.ID, 60, 1 << 30}, // 内存 25%
		{b.ID, 40, 3 << 30}, // 内存 75%
	} {
		if err := st.InsertMetrics(n.id, &model.Metrics{
			V: 1, Ts: now - 5*60*1000,
			CPU: model.CPUStat{Usage: n.cpu},
			Mem: model.MemStat{Total: 4 << 30, Used: n.mem},
			Net: model.NetStat{Up: 100, Down: 200},
		}); err != nil {
			t.Fatalf("写入指标失败: %v", err)
		}
	}

	pts := h.dashResPoints([]string{a.ID, b.ID}, netqWindow("1h"),
		map[string]int64{a.ID: 4 << 30, b.ID: 4 << 30})
	if len(pts) != 1 {
		t.Fatalf("同一分钟桶应合成 1 个点，实际 %d", len(pts))
	}
	// 求和会是 100 / 100 —— 那就是把两台机器当成一台在算，必须是平均
	if math.Abs(pts[0].CPU-50) > 0.01 {
		t.Errorf("CPU 跨节点平均 = %.2f，应为 50", pts[0].CPU)
	}
	if math.Abs(pts[0].Mem-50) > 0.01 {
		t.Errorf("内存跨节点平均 = %.2f，应为 50", pts[0].Mem)
	}

	// 没有节点时不该查库（ids 为空会拼出 IN () 的错 SQL）
	if got := h.dashResPoints(nil, netqWindow("1h"), nil); got != nil {
		t.Errorf("空 ids 应返回 nil，实际 %v", got)
	}
}

// ---- 总览卡时间窗切换 ----

// activeRangeIs 判断总览卡切换器里 key 对应的按钮是否是高亮态
// （取 "range-btn on" 那个 <a> 的内容，确认它就是目标窗）。
func activeRangeIs(body, key string) bool {
	i := strings.Index(body, `class="range-btn on"`)
	if i < 0 {
		return false
	}
	seg := body[i:]
	if j := strings.Index(seg, "</a>"); j >= 0 {
		seg = seg[:j]
	}
	return strings.Contains(seg, "range="+key+`"`)
}

// TestHomeOverviewRangeSwitch 总览卡的时间窗切换：?range= 选中的按钮高亮、
// 时段说明与两条曲线的数据都跟随所选窗口；长窗从 rollup 层读（不是截断）。
// 认不出的值退回 1 小时 —— 宁可按默认画，也不能因为一个错参数就空着。
func TestHomeOverviewRangeSwitch(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "测试节点 JP", "JP", "日本 · 东京", "")

	// 三小时前与两小时前各一个分钟（已闭合窗口，能进 1m 汇总表）：CPU 40%、内存 50%。
	// 两点跨一小时 —— 时段说明的 from/to 必须真的不同（桶序号伪装成时间戳的
	// bug 会让两端显示成同一分钟，2026-10-17 线上真出过）。
	now := time.Now().UnixMilli()
	for i := 0; i < 2; i++ {
		if err := st.InsertMetrics(n.ID, &model.Metrics{
			V: 1, Ts: now - int64(180-i*60)*60*1000, Seq: int64(i),
			CPU: model.CPUStat{Usage: 40},
			Mem: model.MemStat{Total: 4 << 30, Used: 2 << 30},
			Net: model.NetStat{Up: 1000, Down: 2000},
		}); err != nil {
			t.Fatalf("写入指标失败: %v", err)
		}
	}
	if err := st.AggregateRollups(); err != nil {
		t.Fatalf("生成 rollup 失败: %v", err)
	}

	// 默认：1 小时按钮亮；这段没有数据，走"采样点还不够"的空态
	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))
	if !activeRangeIs(body, "1h") {
		t.Error("默认应高亮 1 小时按钮")
	}
	if strings.Contains(body, `id="rate-data"`) {
		t.Error("1 小时窗内没有数据，不该渲染速率曲线")
	}
	// 档位齐全：每个 rangeOption 都要有按钮（含 2026-10-17 新增的 12 小时）
	if c := strings.Count(body, `class="range-btn`); c != len(rangeOptions) {
		t.Errorf("总览切换器有 %d 个按钮，应为 %d 个（rangeOptions）", c, len(rangeOptions))
	}
	if !strings.Contains(body, "range=12h") {
		t.Error("总览切换器缺少 12 小时档")
	}

	// 切到 6 小时：按钮亮、说明与图例都写"近 6 小时"，数据从 1m 汇总表来
	body6 := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/?range=6h", nil))
	if !activeRangeIs(body6, "6h") {
		t.Error("?range=6h 应高亮 6 小时按钮")
	}
	if !strings.Contains(body6, "近 6 小时 · ") {
		t.Error("时段说明应写「近 6 小时 · …」")
	}
	if !strings.Contains(body6, `id="rate-data"`) {
		t.Error("6 小时窗内应有数据：速率曲线数据块缺失（长窗没读 rollup？）")
	}
	if !strings.Contains(body6, `id="res-data"`) {
		t.Error("6 小时窗内应有数据：占用率曲线数据块缺失")
	}

	// 认不出的 range 值退回 1 小时
	bodyX := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/?range=999d", nil))
	if !activeRangeIs(bodyX, "1h") {
		t.Error("非法 range 值应退回 1 小时")
	}

	// 切换链接不许带 #锚点：带锚点时每次点按钮都会把总览滚到视口顶端，
	// 和初始状态的版面位置对不上（boss 2026-10-17 报过）。
	if strings.Contains(body6, "#speed") {
		t.Error("总览切换链接不应带 #speed 锚点（会引入滚动跳变）")
	}
}
