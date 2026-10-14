package hub

// 系统图标：按 /etc/os-release 的 PRETTY_NAME 认发行版。

import (
	"strings"
	"testing"
)

// TestOsIconMatchesRealNames 真实的 PRETTY_NAME 都要认得出。
func TestOsIconMatchesRealNames(t *testing.T) {
	cases := map[string]bool{
		"Debian GNU/Linux 13 (trixie)": true,
		"Debian 12":                    true,
		"Ubuntu 22.04.3 LTS":           true,
		"Alpine Linux v3.19":           true,
		"CentOS Stream 9":              true,
		"Rocky Linux 9.3 (Blue Onyx)":  true,
		"AlmaLinux 9.3 (Shamrock)":     true,
		"Fedora Linux 39":              true,
		"Arch Linux":                   true,
		"openSUSE Leap 15.5":           true,
		"FreeBSD 14.0-RELEASE":         true,
		"Red Hat Enterprise Linux 9.3": true,
	}
	for name := range cases {
		if osIcon(name) == "" {
			t.Errorf("认不出 %q", name)
		}
	}
	// 认不出来要返回空串，**不能**硬塞一个通用图标 ——
	// 那会让人以为"认出来了，就是 Linux"，实际是认错了。
	for _, name := range []string{"", "Plan 9", "Haiku OS"} {
		if got := osIcon(name); got != "" {
			t.Errorf("%q 不该匹配到图标，实际匹配到了", name)
		}
	}
}

// TestOsIconRockyBeatsLinux 关键字顺序要能挡住"更泛的规则先命中"。
//
// "Rocky Linux" 里既有 "rocky" 也有 "linux"。如果匹配表里有 "linux"
// 而且在前面，Rocky 就会被认成通用 Linux —— 图标就错了。
// 这类 bug 不报错，只是图标画得不对，很难发现。
func TestOsIconRockyBeatsLinux(t *testing.T) {
	rocky := osIcon("Rocky Linux 9.3")
	debian := osIcon("Debian GNU/Linux 13 (trixie)")
	if rocky == "" || debian == "" {
		t.Fatal("两个都该有图标")
	}
	if rocky == debian {
		t.Error("Rocky Linux 和 Debian 的图标不该一样 —— 关键字顺序可能错了")
	}
	// 带 "Linux" 字样的专属发行版，不能落到通用 Linux 图标上
	for _, name := range []string{"Arch Linux", "Alpine Linux v3.19", "Gentoo Linux"} {
		if osIcon(name) == osIcon("Linux") {
			t.Errorf("%q 落到了通用 Linux 图标，专属图标没生效", name)
		}
	}
}

// TestOsIconPathsSane 图标数据本身要是合法 path。
func TestOsIconPathsSane(t *testing.T) {
	if len(osIconPaths) < 10 {
		t.Fatalf("图标只有 %d 个，太少了", len(osIconPaths))
	}
	for name, d := range osIconPaths {
		if len(d) < 20 {
			t.Errorf("%s 的 path 太短（%d 字符），可能是空的", name, len(d))
		}
		// 大写 M 是绝对 moveto，小写 m 是相对 —— 两个都合法
		// （Simple Icons 里 raspberrypi 就是小写 m）。
		if !strings.HasPrefix(d, "M") && !strings.HasPrefix(d, "m") {
			t.Errorf("%s 的 path 不是以 moveto 开头：%.40s", name, d)
		}
	}
	// 匹配表里的每个 key 都要有对应的图标文件，否则会静默返回空
	for key := range osIconFile {
		if osIconPaths[osIconFile[key]] == "" {
			t.Errorf("关键字 %q 指向的图标 %q 不存在", key, osIconFile[key])
		}
	}
}
