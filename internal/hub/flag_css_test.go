package hub

// 国旗：盒子必须是正方形。
//
// 图标源（flag-icons）的 viewBox 是 `0 0 512 512`，每面旗铺满这个正方形。
// 给它一个非正方的盒子，浏览器按 preserveAspectRatio 默认的 "meet" 缩放，
// 内容会在**较短的那一边**留空档 —— 外面那圈 1px 描边就把空档框出来了，
// 看起来像"国旗旁边有一条白边"。而且**不报错**，只能靠肉眼发现。
//
// 这个坑踩过两次（第二次是 boss 说"又有白边了"），所以钉进测试。

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

func TestFlagBoxIsSquare(t *testing.T) {
	b, err := fs.ReadFile(staticFS, "static/style.css")
	if err != nil {
		t.Fatalf("读样式表失败: %v", err)
	}
	css := string(b)

	// 抓 .flag svg 的 width / height
	re := regexp.MustCompile(`\.flag svg \{[^}]*\}`)
	m := re.FindString(css)
	if m == "" {
		t.Fatal("找不到 .flag svg 规则")
	}
	w := regexp.MustCompile(`width:[[:space:]]*([0-9.]+)em`).FindStringSubmatch(m)
	h := regexp.MustCompile(`height:[[:space:]]*([0-9.]+)em`).FindStringSubmatch(m)
	if w == nil || h == nil {
		t.Fatalf("解析不出宽高: %q", m)
	}
	if w[1] != h[1] {
		t.Errorf(".flag svg 的宽高必须相等（viewBox 是正方形），实际 %sem × %sem —— "+
			"不等就会有一边留空档，看起来像白边", w[1], h[1])
	}

	// lg 版本同理
	if ml := regexp.MustCompile(`\.flag\.lg svg \{[^}]*\}`).FindString(css); ml != "" {
		lw := regexp.MustCompile(`width:[[:space:]]*([0-9.]+)em`).FindStringSubmatch(ml)
		lh := regexp.MustCompile(`height:[[:space:]]*([0-9.]+)em`).FindStringSubmatch(ml)
		if lw != nil && lh != nil && lw[1] != lh[1] {
			t.Errorf(".flag.lg svg 宽高也必须相等，实际 %sem × %sem", lw[1], lh[1])
		}
	}

	// 图标本身的 viewBox 也得是正方形 —— 万一换了图标集，这条会提醒
	if !strings.Contains(css, ".flag svg") {
		t.Error("样式表里找不到 .flag svg")
	}
}
