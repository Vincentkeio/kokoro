package hub

import "time"

type connectivityIndicator struct{ Family, State, Text string }

func (c nodeCard) ConnectivityIndicators() []connectivityIndicator {
	rows := []connectivityIndicator{{"IPv4", "unknown", "未探测"}, {"IPv6", "unknown", "未探测"}}
	for i := range rows {
		if !c.Online {
			rows[i].Text = "离线"
			continue
		}
		if c.Latest == nil || c.Latest.Connectivity == nil {
			continue
		}
		probe := c.Latest.Connectivity
		if probe.CheckedAt <= 0 || time.Now().UnixMilli()-probe.CheckedAt > 180000 {
			rows[i].Text = "待更新"
			continue
		}
		ok := probe.IPv4
		if i == 1 {
			ok = probe.IPv6
		}
		if ok {
			rows[i].State = "online"
			rows[i].Text = "连通"
		} else {
			rows[i].State = "unreachable"
			rows[i].Text = "无响应"
		}
	}
	return rows
}
