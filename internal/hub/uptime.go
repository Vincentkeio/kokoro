package hub

// 状态时间轴：一天一格，一眼看出这台机器这一个月稳不稳。
//
// 这是探针圈最经典的"可信度背书" —— 别人问你机器稳不稳，
// 你说 99.9% 不如直接把 30 个格子摆出来。

import (
	"fmt"
	"time"

	"github.com/Vincentkeio/kokoro/internal/store"
)

// uptimeCell 是时间轴上的一格（一天）。
type uptimeCell struct {
	Label   string // 10-06
	Pct     string // 99.9%
	Level   string // ok | warn | bad | none
	Tip     string // 悬停提示
	HasData bool
	Partial bool
}

// uptimeSummary 是时间轴上方那句话。
type uptimeSummary struct {
	Days  int
	Pct   string // 99.95%
	Level string
	Cells []uptimeCell
	First string // 第一个格子的日期标签，给轴用
	Last  string // 最后一个
	// DataDays 是**真正有数据**的天数。
	//
	// ⚠️ 必须显示出来。百分比是按"有数据的天"算的，所以一台今天刚接入的
	// 机器会算出很高的在线率（只统计今天，当然接近 100%），
	// 而前面 29 格是灰的（无数据）。只写"近 30 天"就是在虚报 ——
	// 看着像"稳定跑了 30 天"，其实是"今天才认识它"。
	// boss 就是这么发现不对劲的。
	DataDays int
}

// hubTZOffsetMin 取面板所在时区的 UTC 偏移（分钟）。
//
// 切"天"要用面板自己的时区 —— 跟首页那个主机时钟保持一致，
// 不然站长会看到"昨天的在线率"跟自己的日期对不上。
func hubTZOffsetMin() int {
	_, off := time.Now().Zone()
	return off / 60
}

// uptimeDays 默认看多少天。
//
// 30 天：一个月是"稳不稳"最自然的时间尺度，格子数也刚好排得下。
const uptimeDays = 30

// loadUptime 取某台机器的状态时间轴。
func (h *Hub) loadUptime(nodeID string) *uptimeSummary {
	days, err := h.store.DailyUptime(nodeID, uptimeDays, hubTZOffsetMin())
	if err != nil || len(days) == 0 {
		return nil
	}

	sum := &uptimeSummary{Days: len(days)}
	// 总在线率：把有数据的天按样本数加权，而不是简单平均 ——
	// 否则"跑了一小时的 100%"和"跑了整天的 100%"权重一样。
	var totalSamples, totalExpected float64
	for _, d := range days {
		cell := uptimeCell{
			Label:   d.Label,
			HasData: d.HasData,
			Partial: d.Partial,
		}
		switch {
		case !d.HasData:
			cell.Level = "none"
			cell.Pct = "无数据"
			// 灰格子有两种，得分开说 —— 否则站长看到一片灰会以为
			// "一直在掉线"，其实只是这台机器那会儿还没接入。
			if d.BeforeStart {
				cell.Tip = d.Date + " 这台机器还没接入"
			} else {
				cell.Tip = d.Date + " 没有采集到数据"
			}
		default:
			sum.DataDays++
			cell.Level = uptimeLevel(d.Pct)
			cell.Pct = fmt.Sprintf("%.2f%%", d.Pct)
			cell.Tip = fmt.Sprintf("%s 在线率 %s", d.Date, cell.Pct)
			if d.Partial {
				cell.Tip += "（这天只有部分数据）"
			}
			totalSamples += float64(d.Samples)
			// 反推这一天的期望样本数，用于加权
			if d.Pct > 0 {
				totalExpected += float64(d.Samples) / (d.Pct / 100)
			}
		}
		sum.Cells = append(sum.Cells, cell)
	}

	if len(sum.Cells) > 0 {
		sum.First = sum.Cells[0].Label
		sum.Last = sum.Cells[len(sum.Cells)-1].Label
	}
	if totalExpected > 0 {
		pct := totalSamples / totalExpected * 100
		if pct > 100 {
			pct = 100
		}
		sum.Pct = fmt.Sprintf("%.2f%%", pct)
		sum.Level = uptimeLevel(pct)
	} else {
		sum.Pct = "—"
		sum.Level = "none"
	}
	return sum
}

// uptimeLevel 把在线率分档。阈值偏严：探针的在线率本来就该接近 100%，
// 99% 以下说明确实掉过线，不该显示成绿色。
func uptimeLevel(pct float64) string {
	switch {
	case pct >= 99.5:
		return "ok"
	case pct >= 95:
		return "warn"
	default:
		return "bad"
	}
}

// 编译期确认 store 的类型被用到（避免 import 被误删）
var _ = store.DayUptime{}
