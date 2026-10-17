package hub

import "regexp"

type routeNameRule struct {
	name    string
	pattern *regexp.Regexp
	class   string
}

var routePathSeparator = regexp.MustCompile(`\s*(?:→|->|⇒|=>)\s*`)

func routeName(name, pattern string, style ...string) routeNameRule {
	class := "route-standard"
	if len(style) > 0 {
		class = style[0]
	}
	return routeNameRule{name, regexp.MustCompile(pattern), class}
}

// Display precedence, not a network quality rating. The first matching rule
// wins across the complete observed path; moving a rule changes its priority.
// Specific product names precede their generic ASN names. ASN alone never
// upgrades CN2 to GIA/GT or CTGNet to GIA.
// Sources and the editable order are documented in docs/ROUTE-DICTIONARY.md.
var routeNamePriority = []routeNameRule{
	routeName("CN2 GIA", `(?i)\bCN2[\s_-]*GIA\b`, "route-cn2"),
	routeName("CTG GIA", `(?i)\bCTG(?:NET)?[\s_-]*GIA\b`, "route-cn2"),
	routeName("CN2 GT", `(?i)\bCN2[\s_-]*GT\b`, "route-cn2"),
	routeName("CMIN2", `(?i)\b(?:CMIN2|AS\s*58807)\b`, "route-cmin2"),
	routeName("9929", `(?i)\b(?:9929|AS\s*9929|CUII)\b`, "route-9929"),
	routeName("CN2（GT/GIA未确认）", `(?i)CN2[（(]GT/GIA未确认[）)]`, "route-cn2"),
	routeName("CTGNet", `(?i)\b(?:CTGNET|AS\s*23764)\b`, "route-cn2"),
	routeName("CN2", `(?i)\b(?:CN2|AS\s*4809)\b`, "route-cn2"),
	routeName("163", `(?i)\b(?:163|AS\s*4134|CHINANET)\b`),
	routeName("NTT", `(?i)\b(?:NTT|AS\s*2914)\b`),
	routeName("4837", `(?i)\b(?:4837(?:/169)?|AS\s*4837|CHINA169)\b`),
	routeName("CMI", `(?i)\b(?:CMI|AS\s*58453)\b`),
	routeName("CMNET", `(?i)\b(?:CMNET|AS\s*9808)\b`),
	routeName("CUG", `(?i)\b(?:CUG|AS\s*10099)\b|联通国际`),
	routeName("CERNET", `(?i)\b(?:CERNET|AS\s*4538)\b`),
	routeName("CSTNET", `(?i)\b(?:CSTNET|AS\s*7497)\b`),
	routeName("Softbank", `(?i)\b(?:SOFTBANK|AS\s*17676)\b`),
	routeName("IIJ", `(?i)\b(?:IIJ|AS\s*2497)\b`),
	routeName("PCCW", `(?i)\b(?:PCCW|CONSOLE\s+CONNECT|AS\s*3491)\b`),
	routeName("Telstra", `(?i)\b(?:TELSTRA|AS\s*4637)\b`),
	routeName("Tata", `(?i)\b(?:TATA|AS\s*6453)\b`),
	routeName("Arelion", `(?i)\b(?:ARELION|TELIA|AS\s*1299)\b`),
	routeName("GTT", `(?i)\b(?:GTT|AS\s*3257)\b`),
	routeName("Level3", `(?i)\b(?:LEVEL\s*3|LUMEN|CENTURYLINK|AS\s*3356)\b`),
	routeName("Cogent", `(?i)\b(?:COGENT|AS\s*174)\b`),
	routeName("HE", `(?i)\b(?:HE|HURRICANE\s+ELECTRIC|AS\s*6939)\b`),
	routeName("Telxius", `(?i)\b(?:TELXIUS|TELEFONICA|AS\s*12956)\b`),
	routeName("Orange", `(?i)\b(?:ORANGE|OPENTRANSIT|AS\s*5511)\b`),
	routeName("Zayo", `(?i)\b(?:ZAYO|ABOVENET|AS\s*6461)\b`),
	routeName("Verizon", `(?i)\b(?:VERIZON|UUNET|AS\s*701)\b`),
}

func dictionaryRouteName(path string) string {
	for _, rule := range routeNamePriority {
		if rule.pattern.MatchString(path) {
			return rule.name
		}
	}
	return ""
}

func dictionaryRouteClass(name string) string {
	for _, rule := range routeNamePriority {
		if rule.name == name {
			return rule.class
		}
	}
	return "route-standard"
}
