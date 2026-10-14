package agent

// 三网探测：定期从小鸡出发，ping 各省电信/联通/移动的递归 DNS，
// 把延迟 / 抖动 / 丢包上报给 Hub。探测点是 internal/netprobe 里的内置表。
//
// 为什么低频（默认 30 分钟一轮）：一轮要打几十个目标，属于重活，
// 指标上报（2 秒一次）才是主线，不能让探测抢了资源或拖慢主循环。
// 探测失败不影响主循环，只记日志。
//
// 探测范围（2026-10-11 起）：默认只探三网基线（北京 × 电信/联通/移动），
// 分省由站长在后台按需勾选。Hub 通过 netq_targets 命令把范围推下来，
// agent 每条上报都带上自己当前用的范围供 Hub 比对（自愈：agent 重启后
// 第一次上报就会被纠正回站长的选择）。范围改变会**立刻**触发新一轮。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Vincentkeio/kokoro/internal/netprobe"
)

const (
	// netqBackoffSec 只在**本轮失败**时退避几秒再试。
	//
	// 为什么成功路径不等：一轮探测本身就要几秒（每目标 3 个包、
	// 来回 RTT），这个时长就是天然节奏；再叠加等待纯属浪费
	// （boss 2026-10-12：“一轮打完歇 5 秒就打下一轮。不要歇，
	// 要连续”）。退避只留给失败 —— 探测函数疯狂重试才会把
	// 机器和目标都打爆。
	netqBackoffSec = 5
	// netqTimeout 单轮探测的总时限。
	netqTimeout = 4 * time.Minute
	// netqCount 每个目标发几个包。
	netqCount = 3
	// netqPingTimeout 单个包的超时。
	netqPingTimeout = 800 * time.Millisecond
	// netqMaxTargets 单轮最多探多少个目标（内置表 100 出头，留点余量）。
	netqMaxTargets = 200
)

// netqLoop 是探测主循环。关闭时等待范围/间隔变化的唤醒，不能直接退出；
// 否则后台后来重新启用探测，agent 不重启就永远不会恢复。
//
// 默认**连续探测**（boss 2026-10-12）：一轮跑完立刻打下一轮，中间
// 不等 —— 小范围（几个省）一轮只要几百毫秒到几秒，这个时长就是
// 节奏；只有本轮**失败**才退避几秒，防止探测函数疯狂重试。
// 间隔可调是给"勾了几十个省"的场景留的：那时一轮几分钟，需要
// 间隔防止把机器和目标都打爆。
func (a *Agent) netqLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if a.netqInterval() < 0 {
			select {
			case <-ctx.Done():
				return
			case <-a.netqWake:
				continue
			}
		}

		// 范围为空（站长选了「全不选」）= 分省探测整体关闭：
		// 不跑、不上报空轮，只等范围命令来唤醒。
		var runErr error
		if sel := a.netqSelection(); len(sel) > 0 {
			runErr = a.runNetQOnce(ctx, sel)
			if runErr != nil {
				// 探测属于"尽力而为"，失败只记日志 + 退避几秒
				logger.Printf("三网探测本轮失败：%v", runErr)
			}
		} else {
			// 当前没有启用任何省份时挂起等配置更新，不循环空转。
			select {
			case <-ctx.Done():
				return
			case <-a.netqWake:
				continue
			}
		}

		// 两轮之间：默认**不等**（连续）；本轮失败退避 netqBackoffSec；
		// 配了正数间隔就睡对应分钟。每轮都重读 —— reconfig / 范围命令
		// 热改间隔要立刻生效（旧代码在循环外只读一次，改了要等重启）。
		wait := time.Duration(0)
		if runErr != nil {
			wait = netqBackoffSec * time.Second
		}
		if iv := a.netqInterval(); iv < 0 {
			// 关停之后保留循环，后台再次启用时仍可唤醒。
			select {
			case <-ctx.Done():
				return
			case <-a.netqWake:
				continue
			}
		} else if iv > 0 {
			wait = iv
		}
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-a.netqWake:
				// 范围/间隔变了，觉不睡了，立刻开跑
				timer.Stop()
			case <-timer.C:
			}
		} else {
			// 连续模式：不等，但 ctx 取消和范围唤醒还是要响应
			select {
			case <-ctx.Done():
				return
			case <-a.netqWake:
			default:
			}
		}
	}
}

// netqCatalogVer 返回当前探测点目录的版本号（内容哈希），随上报带给 Hub。
// Hub 靠它判断要不要把后台更新的目录推下来。
func (a *Agent) netqCatalogVer() string { return netprobe.CatalogVersion() }

// netqSelection 返回当前探测范围（省份名列表的拷贝，调用方改不出事）。
func (a *Agent) netqSelection() []string {
	a.netqSelMu.Lock()
	defer a.netqSelMu.Unlock()
	return append([]string(nil), a.netqSel...)
}

// setNetQSelection 设置探测范围并唤醒探测循环。
// provinces 为空 = 关闭分省探测。调用方是命令通道，串行执行，不会并发。
func (a *Agent) setNetQSelection(provinces []string) {
	a.netqSelMu.Lock()
	a.netqSel = append([]string(nil), provinces...)
	a.netqSelMu.Unlock()
	select {
	case a.netqWake <- struct{}{}:
	default: // 已经有一个待处理的唤醒了，不堆积
	}
}

// netqInterval 解析配置：-1 关闭；0（未配）= 连续模式；正数 = 间隔分钟。
//
// 0 = 连续是 2026-10-12 起的新默认（boss："打完一轮就上报"）——
// 旧默认 30 分钟是给全量 100+ 目标留的，范围收窄后纯属浪费。
func (a *Agent) netqInterval() time.Duration {
	a.mu.Lock()
	m := a.cfg.NetQIntervalMin
	a.mu.Unlock()
	if m < 0 {
		return -1
	}
	if m == 0 {
		return 0
	}
	// 配了正数就不防下限了 —— 勾几十个省时调大间隔是**为了**机器好，
	// 而配个 0.2 分钟（12 秒）也远打不爆，让站长自己决定。
	return time.Duration(m) * time.Minute
}

// setNetQInterval 热改探测间隔（reconfig / netq_targets 命令调用）：
// 写内存 + 唤醒循环（循环每轮重读间隔，下次计时就生效）。
func (a *Agent) setNetQInterval(min int) {
	a.mu.Lock()
	a.cfg.NetQIntervalMin = min
	a.mu.Unlock()
	select {
	case a.netqWake <- struct{}{}:
	default: // 已有一个待处理的唤醒，不堆积
	}
}

// runNetQOnce 跑一轮探测并上报。sel 是省份范围（保证非空）。
func (a *Agent) runNetQOnce(ctx context.Context, sel []string) error {
	prober, err := netprobe.New()
	if err != nil {
		return fmt.Errorf("创建探测器失败：%w", err)
	}

	// 只探选中的省份（× 全部运营商）。范围由 Hub 的 netq_targets
	// 命令维护，默认是「北京」三网基线。
	targets := netprobe.Filter(sel, nil)
	if len(targets) == 0 {
		return fmt.Errorf("省份范围 %v 在内置探测表里匹配不到目标", sel)
	}
	if len(targets) > netqMaxTargets {
		targets = targets[:netqMaxTargets]
	}

	pctx, cancel := context.WithTimeout(ctx, netqTimeout)
	defer cancel()

	start := time.Now()
	results := prober.Probe(pctx, targets, netqCount, netqPingTimeout)

	ok := 0
	for _, r := range results {
		if r.OK {
			ok++
		}
	}

	payload := netprobe.ResultsPayload{
		NodeID:  a.cfg.NodeID,
		Ts:      time.Now().UnixMilli(),
		Mode:    prober.Mode(),
		Results: results,
	}
	if err := a.sendNetQ(ctx, &payload); err != nil {
		return err
	}
	logger.Printf("三网探测完成：%d/%d 个点有响应（范围：%v），用时 %.1fs，模式 %s",
		ok, len(results), sel, time.Since(start).Seconds(), prober.Mode())
	return nil
}

// sendNetQ 把探测结果 POST 到 Hub。
func (a *Agent) sendNetQ(ctx context.Context, p *netprobe.ResultsPayload) error {
	body, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("序列化探测结果失败：%w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.hub+"/api/v1/netq", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.token())

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("上报探测结果失败：%w", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("上报探测结果 HTTP %d：%s", resp.StatusCode, truncate(string(b), 200))
	}
	return nil
}
