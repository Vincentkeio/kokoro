package agent

// 三网探测：定期从小鸡出发，ping 全国各省电信/联通/移动的递归 DNS，
// 把延迟 / 抖动 / 丢包上报给 Hub。探测点是 internal/netprobe 里的内置表。
//
// 为什么低频（默认 30 分钟一轮）：一轮要打 100 多个目标，属于重活，
// 指标上报（2 秒一次）才是主线，不能让探测抢了资源或拖慢主循环。
// 探测失败不影响主循环，只记日志。

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
	// netqDefaultIntervalMin 未配置时的默认间隔。
	netqDefaultIntervalMin = 30
	// netqMinIntervalMin 最小间隔，防止有人把它配成 1 分钟把机器打爆。
	netqMinIntervalMin = 5
	// netqWarmupDelay 启动后先等一会儿再探测，避开注册与首轮上报。
	netqWarmupDelay = 45 * time.Second
	// netqTimeout 单轮探测的总时限。
	netqTimeout = 4 * time.Minute
	// netqCount 每个目标发几个包。
	netqCount = 3
	// netqPingTimeout 单个包的超时。
	netqPingTimeout = 800 * time.Millisecond
	// netqMaxTargets 单轮最多探多少个目标（内置表 100 出头，留点余量）。
	netqMaxTargets = 200
)

// netqLoop 是探测主循环。配置为 -1 时直接返回，不占资源。
func (a *Agent) netqLoop(ctx context.Context) {
	iv := a.netqInterval()
	if iv <= 0 {
		logger.Printf("三网探测已关闭（netq_interval_min = -1）")
		return
	}

	if !sleepCtx(ctx, netqWarmupDelay) {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := a.runNetQOnce(ctx); err != nil {
			// 探测属于"尽力而为"，失败只记日志，不触发退避重试
			logger.Printf("三网探测本轮失败：%v", err)
		}

		if !sleepCtx(ctx, iv) {
			return
		}
	}
}

// netqInterval 解析配置：-1 关闭，0 用默认，其余不得小于下限。
func (a *Agent) netqInterval() time.Duration {
	m := a.cfg.NetQIntervalMin
	if m < 0 {
		return 0
	}
	if m == 0 {
		m = netqDefaultIntervalMin
	}
	if m < netqMinIntervalMin {
		m = netqMinIntervalMin
	}
	return time.Duration(m) * time.Minute
}

// runNetQOnce 跑一轮探测并上报。
func (a *Agent) runNetQOnce(ctx context.Context) error {
	prober, err := netprobe.New()
	if err != nil {
		return fmt.Errorf("创建探测器失败：%w", err)
	}

	targets := netprobe.Targets
	if len(targets) > netqMaxTargets {
		targets = targets[:netqMaxTargets]
	}
	if len(targets) == 0 {
		return fmt.Errorf("探测目标表为空")
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
	logger.Printf("三网探测完成：%d/%d 个点有响应，用时 %.1fs，模式 %s",
		ok, len(results), time.Since(start).Seconds(), prober.Mode())
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
