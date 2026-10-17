package hub

import "strings"

type routeBadge struct{ Text, Class string }

// Keep different observed backbones distinct; uncertainty remains in details.
func cardRouteBadges(value string) []routeBadge {
	value = cardBackboneName(strings.TrimSpace(value))
	carrier := ""
	for _, isp := range []string{"电信", "联通", "移动"} {
		if strings.HasPrefix(value, isp+" ") {
			carrier = isp
			value = strings.TrimSpace(strings.TrimPrefix(value, isp))
			break
		}
	}
	badges := []routeBadge{}
	if carrier != "" {
		badges = append(badges, routeBadge{carrier, "route-carrier"})
	}
	for _, part := range uniqStrings(strings.Split(value, " / ")) {
		class := dictionaryRouteClass(part)
		if part == "" || part == "—" || part == "-" || part == "无" || part == "不可用" || unknownCardRoute(part) {
			class = "route-unavailable"
		}
		badges = append(badges, routeBadge{part, class})
	}
	return badges
}
