// Package flags 提供国家/地区旗帜的内联 SVG，不依赖任何 CDN。
//
// 数据来源（不可省略的署名）：
//  1. 项目：flag-icons —— https://github.com/flag-icons/flag-icons
//     原始目录：flags/4x3/*.svg（4:3 版，另含少量 1:1 的圆形变体未收录）
//     许可证：MIT License，Copyright (c) 2013 Panayiotis Lipiridis
//     （允许再分发，须保留版权与许可声明 —— 即本段注释）
//     抓取日期：2026-10-05
//     本地处理：仅去掉换行与缩进（minify），未改动任何图形数据
//  2. 文件：svg/tw.svg（中国台湾）**不来自上面的数据集**，单独取自
//     Wikimedia Commons 的 File:Flag of Chinese Taipei for Olympic Games.svg，
//     许可为 Public domain，抓取日期 2026-10-06，理由见下。
//
// 合规说明（不可变更）：TW / HK / MO 对应中国的台湾地区、香港特别行政区、
// 澳门特别行政区。代码层面保留 ISO 3166-1 代码只是为了匹配数据文件，
// 中文名称一律写作「中国台湾」「中国香港」「中国澳门」，不得与主权国家并列表述。
//
// 其中 **TW 的旗帜必须是「中华台北奥委会旗」（梅花 + 青天白日 + 奥运五环），
// 绝不能用任何带有「中华民国」意涵的旗帜**。上游 flag-icons 数据集给的是后者，
// 所以这里单独替换掉了。修改本包时请勿把 tw.svg 换回数据集版本。
package flags

import (
	"embed"
	"html/template"
	"io/fs"
	"sort"
	"strings"
	"sync"
)

//go:embed svg/*.svg
var svgFS embed.FS

var (
	mu    sync.RWMutex
	cache = map[string]string{}

	// reverseZH：中文名 → 代码；reverseEN：英文名（小写）→ 代码。
	reverseZH map[string]string
	reverseEN map[string]string
	// zhByLen：中文名按长度倒序，用于「从一句话里认出国家」时优先匹配长名。
	zhByLen []string
	// available：本地确实存在的代码集合。
	available map[string]bool

	once sync.Once
)

// prepare 建索引。数据量很小（几百条），首次用到时一次做完成本可忽略。
func prepare() {
	once.Do(func() {
		available = make(map[string]bool, 256)
		entries, err := fs.ReadDir(svgFS, "svg")
		if err == nil {
			for _, e := range entries {
				name := e.Name()
				if !strings.HasSuffix(name, ".svg") {
					continue
				}
				code := strings.ToUpper(strings.TrimSuffix(name, ".svg"))
				available[code] = true
			}
		}

		reverseZH = make(map[string]string, len(zhNames)*2)
		for code, zh := range zhNames {
			reverseZH[zh] = code
		}
		for alias, code := range zhAliases {
			if _, ok := reverseZH[alias]; !ok {
				reverseZH[alias] = code
			}
		}
		// 补几个常用简称与合规写法
		reverseZH["中国"] = "CN"
		reverseZH["中国台湾"] = "TW"
		reverseZH["台湾"] = "TW"
		reverseZH["中国香港"] = "HK"
		reverseZH["香港"] = "HK"
		reverseZH["中国澳门"] = "MO"
		reverseZH["澳门"] = "MO"

		// enNames 的键已经是「小写 + 去重音 + 去非字母数字」的折叠形式，
		// 所以查询前先用同样的规则折叠输入即可。
		reverseEN = make(map[string]string, len(enNames)+24)
		for folded, code := range enNames {
			reverseEN[folded] = code
		}
		for folded, code := range map[string]string{
			"usa": "US", "unitedstates": "US", "america": "US",
			"uk": "GB", "britain": "GB", "greatbritain": "GB",
			"russia": "RU", "southkorea": "KR", "korea": "KR", "northkorea": "KP",
			"vietnam": "VN", "czechrepublic": "CZ", "czechia": "CZ",
			"taiwan": "TW", "taiwanchina": "TW",
			"hongkong": "HK", "hongkongchina": "HK",
			"macao": "MO", "macau": "MO", "macaochina": "MO",
			"la": "US", "losangeles": "US", "siliconvalley": "US",
		} {
			if _, ok := reverseEN[folded]; !ok {
				reverseEN[folded] = code
			}
		}

		zhByLen = make([]string, 0, len(reverseZH))
		for k := range reverseZH {
			zhByLen = append(zhByLen, k)
		}
		sort.Slice(zhByLen, func(i, j int) bool {
			if len(zhByLen[i]) != len(zhByLen[j]) {
				return len(zhByLen[i]) > len(zhByLen[j])
			}
			return zhByLen[i] < zhByLen[j]
		})
	})
}

// Normalize 把各种输入归一成 ISO 3166-1 alpha-2 大写代码。
// 认识：alpha-2（"us"/"US"）、中英文国名（"美国"/"China"/"USA"）、
// 混在长句里的地名（"美国 · 洛杉矶"、"Los Angeles, US"）。认不出返回 ""。
func Normalize(s string) string {
	prepare()
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	// 1. 直接就是两位代码
	if len(s) == 2 {
		up := strings.ToUpper(s)
		if available[up] {
			return up
		}
	}

	// 2. 精确命中中文名
	if code, ok := reverseZH[s]; ok && available[code] {
		return code
	}

	// 3. 精确命中英文名（折叠后比对）
	if code, ok := reverseEN[foldEN(s)]; ok && available[code] {
		return code
	}

	// 4. 长句里找中文国名（长名优先，避免「中国」抢在「中国台湾」前面）
	for _, name := range zhByLen {
		if strings.Contains(s, name) {
			if code := reverseZH[name]; available[code] {
				return code
			}
		}
	}

	// 5. "Los Angeles, US" 这种尾部两位代码
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '，' || r == '/' || r == '|' || r == '(' || r == ')'
	})
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) == 2 {
			if up := strings.ToUpper(p); available[up] {
				return up
			}
		}
	}

	// 6. 长句里找英文国名（只用长度 >= 4 的折叠名，避免 "la"、"us" 这类误判）
	low := foldEN(s)
	best := ""
	for en, code := range reverseEN {
		if len(en) < 4 || !available[code] {
			continue
		}
		if strings.Contains(low, en) && len(en) > len(best) {
			best = en
		}
	}
	if best != "" {
		return reverseEN[best]
	}
	return ""
}

// foldEN 把英文输入折叠成 enNames 用的键形式：小写、只留字母数字。
// 中文会被整体丢掉，交给中文分支处理。
func foldEN(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + 32)
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Has 判断某个代码有没有对应国旗。code 可以是任意写法，内部会先归一。
func Has(code string) bool {
	prepare()
	up := Normalize(code)
	return up != "" && available[up]
}

// SVG 返回国旗的内联 SVG 源码；无法识别时返回空串。
// 返回的内容不带 XML 声明，可以直接插进 HTML。
func SVG(code string) string {
	up := Normalize(code)
	if up == "" {
		return ""
	}

	mu.RLock()
	cached, ok := cache[up]
	mu.RUnlock()
	if ok {
		return cached
	}

	raw, err := svgFS.ReadFile("svg/" + strings.ToLower(up) + ".svg")
	if err != nil {
		return ""
	}
	out := minify(string(raw))

	mu.Lock()
	cache[up] = out
	mu.Unlock()
	return out
}

// Inline 返回可直接插进模板的 SVG。调用方不需要再转义。
func Inline(code string) template.HTML {
	return template.HTML(SVG(code))
}

// Name 返回中文名称；未知代码返回原字符串。
func Name(code string) string {
	prepare()
	up := Normalize(code)
	if up == "" {
		return code
	}
	if zh, ok := zhNames[up]; ok {
		return zh
	}
	return code
}

// Codes 返回本地可用的全部代码，按字母升序。
func Codes() []string {
	prepare()
	out := make([]string, 0, len(available))
	for c := range available {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// minify 去掉 SVG 的换行与行首缩进，能省两成左右体积，且不影响渲染。
// 国旗 SVG 里没有文本节点，空白折叠是安全的。
func minify(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		b.WriteString(line)
	}
	return b.String()
}
