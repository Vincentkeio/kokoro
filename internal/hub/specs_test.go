package hub

// 卡片规格行：值要短，长解释挪到悬停提示。

import (
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// TestNATNotInSpecs NAT 不该再出现在规格表的「网络」项里。
//
// boss 2026-10-11："NAT 不要显示在网络里，网络这一项删掉。
// 如果是 NAT，就把 NAT 作为固定 tag 显示。"
//
// 规格表只留"这台机器有多大、多快"的硬指标；NAT 是机器属性，
// 跟自定义标签排在一起（渲染在 home.html 的 .tags，端到端测试在
// home_test.go 的 TestNATShowsAsTag）。
func TestNATNotInSpecs(t *testing.T) {
	n := &model.Node{NAT: true}
	items := nodeSpecs(*n, nil)

	for _, it := range items {
		if it.Label == "网络" {
			t.Error("「网络」规格项已删，不该再出现 —— NAT 挪去标签行了")
		}
		if it.Value == "NAT" {
			t.Errorf("NAT 不该出现在规格值里，实际在「%s」项", it.Label)
		}
	}
	// 顺带确认原来的悬停解释也没留在规格表里（内容整体搬去了标签的 title）
	for _, it := range items {
		if strings.Contains(it.Value, "共享公网") {
			t.Error("规格值里不该再带 NAT 的解释")
		}
	}

	// 非 NAT 的机器本来就没有这一项，删掉后依然没有
	plain := nodeSpecs(model.Node{}, nil)
	for _, it := range plain {
		if it.Label == "网络" || it.Value == "NAT" {
			t.Error("非 NAT 机器不该有 NAT 相关内容")
		}
	}
}

// TestSpecValuesAreShort 所有规格值都不该长到撑变形。
//
// 半栏宽度大约放得下 20 个字符；超过就该考虑用 Wide 独占整行，
// 或者把解释挪到 Tip。
func TestSpecValuesAreShort(t *testing.T) {
	items := nodeSpecs(model.Node{
		NAT: true, Virt: "kvm", TCPCC: "bbr",
		OS: "Debian GNU/Linux 13 (trixie)",
	}, nil)
	for _, it := range items {
		// 系统名天然会长，模板里它是独占整行的，放过
		if it.Label == "系统" {
			continue
		}
		if !it.Wide && !it.Full && len([]rune(it.Value)) > 24 {
			t.Errorf("「%s」的值有 %d 字（%q）—— 半栏放不下，该用 Wide 或缩短",
				it.Label, len([]rune(it.Value)), it.Value)
		}
	}
}

// TestCPUModelHasNoLabel CPU 型号一行只留型号本身，不带"CPU"标签。
//
// boss 2026-10-11："CPU 这几个字删了，这一行去掉，但内容型号保留。"
// 型号独占一行本身就是小标题，再挂个"CPU"是同一句话说两遍。
// ⚠️ 值必须还在 —— 只删标签、别把型号一起删了（模板靠 Bare 跳过 <dt>）。
func TestCPUModelHasNoLabel(t *testing.T) {
	items := nodeSpecs(model.Node{
		CPUModel: "Intel(R) Xeon(R) Platinum 8272CL CPU @ 2.60GHz",
		CPUCores: 4,
	}, nil)

	if len(items) == 0 {
		t.Fatal("有 CPU 型号时应该有规格项")
	}
	first := items[0]
	if first.Label != "" {
		t.Errorf("型号行不该有标签，实际 %q", first.Label)
	}
	if !first.Bare {
		t.Error("型号行应该是 Bare（模板据此跳过 <dt>）")
	}
	if !first.Full {
		t.Error("型号行仍要独占整行 —— 它最长，塞半栏会被裁")
	}
	if !strings.Contains(first.Value, "Xeon") {
		t.Errorf("型号内容要保留，实际 %q —— 只删标签别删内容", first.Value)
	}
	// 核心数那项照旧带标签（"核心数"），不受 Bare 影响
	var cores *specItem
	for i := range items {
		if items[i].Label == "核心数" {
			cores = &items[i]
		}
	}
	if cores == nil || cores.Bare {
		t.Error("其他规格项不该被 Bare 波及")
	}
}
