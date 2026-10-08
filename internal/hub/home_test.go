package hub

// 首页信息面板的回归测试：时钟 / 统计卡 / 点阵地球 / 富卡片。
//
// 这些功能横跨「Go 组数据 → 模板渲染 → 前端脚本消费」三段，
// 只测 Go 侧或只看页面都可能漏（比如数据给了但模板没引用）。
// 所以这里统一断言**最终 HTML**。

import (
	"encoding/json"
	"fmt"
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

// TestHomeHasClockAndStats 时钟条与统计卡必须渲染出来。
func TestHomeHasClockAndStats(t *testing.T) {
	h, st := newTestHub(t)
	mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

	body := renderBody(t, h, httptest.NewRequest(http.MethodGet, "/", nil))

	// 时钟现在只显示**主机自己的时间**：用 data-offset（固定 UTC 偏移）算，
	// 不再是本地/UTC/东京…那一排多时区。断言改成认这个。
	for _, want := range []string{"hub-clock", "主机时间", "data-offset="} {
		if !strings.Contains(body, want) {
			t.Errorf("首页缺少主机时钟的 %s", want)
		}
	}
	// 数字现在的归属：节点总数/在线/离线/覆盖地区 -> 总览，
	// 今日流量/本月流量 -> 全网速率。**整排统计卡已拆掉**。
	for _, want := range []string{
		"节点总数", "在线", "离线", "覆盖地区", // 总览
		"今日流量", "本月流量", // 全网速率
	} {
		if !strings.Contains(body, want) {
			t.Errorf("首页缺少「%s」", want)
		}
	}
	// 探测点按 boss 要求删掉了
	if strings.Contains(body, "探测点") {
		t.Error("探测点应该已删除")
	}
	// ⚠️ 这些数字只能在**一处**出现。以前总览和统计卡各显示一遍
	// 「节点总数/在线/覆盖地区」，同一屏看两遍同样的数字，
	// 访客只会犹豫哪个才是准的。
	for _, label := range []string{"节点总数", "覆盖地区"} {
		if n := strings.Count(body, label); n != 1 {
			t.Errorf("「%s」出现了 %d 次，应该只有 1 处", label, n)
		}
	}
	// hero 里不该再出现「N 台小鸡 / M 在线」
	if strings.Contains(body, "台小鸡</span>") {
		t.Error("hero 里不该再显示「台小鸡」，它和总览重复了")
	}
}

// TestHomeRichCard 富卡片：资源条带配色档位、规格表、标签。
func TestHomeRichCard(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
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
	node := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")
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
	mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

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
	n := mkNode(t, st, "东京 zouter", "JP", "日本 · 东京", "")

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
