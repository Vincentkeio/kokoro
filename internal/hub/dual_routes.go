package hub

import "strings"

func cardBackboneName(line string) string {
	line = strings.TrimSpace(line)
	if line == "" {
		return ""
	}
	// Match before collapsing cities or stripping the transit chain. Otherwise
	// a higher-priority network in a later segment can never be selected.
	if name := dictionaryRouteName(line); name != "" {
		return name
	}
	line = compactCardRoute(line)
	for _, city := range []string{"北京 ", "上海 ", "深圳 ", "广东 "} {
		line = strings.TrimPrefix(line, city)
	}
	for _, isp := range []string{"电信", "联通", "移动"} {
		line = strings.TrimSpace(strings.TrimPrefix(line, isp))
	}
	line = singleCardRouteName(line)
	if index := strings.Index(line, "（AS"); index >= 0 {
		line = line[:index]
	}
	if line == "163 骨干" {
		return "163"
	}
	return strings.TrimSpace(line)
}

// The first path segment is only a fallback when the dictionary has no match.
func singleCardRouteName(line string) string {
	line = strings.TrimSpace(line)
	if index := routePathSeparator.FindStringIndex(line); index != nil {
		line = strings.TrimSpace(line[:index[0]])
	}
	if index := strings.Index(line, "（AS"); index >= 0 {
		line = strings.TrimSpace(line[:index])
	}
	return line
}

// Legacy route reports contain only IPv4. Never reuse them as IPv6 evidence.
func collectCardRoutes(rep *benchReport, families map[string]map[string]string) {
	for _, item := range rep.Items {
		family := ""
		switch item.Test {
		case "route", "route4":
			family = "IPv4"
		case "route6":
			family = "IPv6"
		default:
			continue
		}
		if _, exists := families[family]; exists {
			continue
		}
		families[family] = nil
		if !item.OK || item.Data == nil || item.Data["available"] == false {
			continue
		}
		values := map[string]string{}
		for _, isp := range []string{"电信", "联通", "移动"} {
			sub, _ := item.Data[isp].(map[string]any)
			line := strings.TrimSpace(strOf(sub, "line"))
			if line != "" {
				values[isp] = cardBackboneName(line)
			}
		}
		families[family] = values
	}
}

func dualRouteCardRow(row string) (taskSummaryRow, bool) {
	for _, isp := range []string{"电信", "联通", "移动"} {
		prefix := "回程 " + isp + " V4 "
		if !strings.HasPrefix(row, prefix) {
			continue
		}
		parts := strings.SplitN(strings.TrimPrefix(row, prefix), " V6 ", 2)
		if len(parts) != 2 {
			return taskSummaryRow{}, false
		}
		v4 := compactRouteLabel(parts[0])
		v6 := compactRouteLabel(parts[1])
		badges := []routeBadge{{"V4", "route-family"}}
		badges = append(badges, cardRouteBadges(v4)...)
		badges = append(badges, routeBadge{"V6", "route-family"})
		badges = append(badges, cardRouteBadges(v6)...)
		return taskSummaryRow{Label: isp, Value: "V4 " + v4 + " V6 " + v6, Badges: badges, Route: true}, true
	}
	return taskSummaryRow{}, false
}

func compactRouteLabel(value string) string {
	return cardBackboneName(strings.TrimSpace(value))
}
