package hub

// 卡片规格行：值要短，长解释挪到悬停提示。

import (
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// TestNATSpecIsShort NAT 在卡片上只写「NAT」，完整解释进悬停提示。
//
// 原来写的是「NAT（共享公网 IP）」—— 塞在指标格子里会把那一行撑变形。
func TestNATSpecIsShort(t *testing.T) {
	n := &model.Node{NAT: true}
	items := nodeSpecs(*n, nil)

	var nat *specItem
	for i := range items {
		if items[i].Label == "网络" {
			nat = &items[i]
			break
		}
	}
	if nat == nil {
		t.Fatal("NAT 机器应该有「网络」这一项")
	}
	if nat.Value != "NAT" {
		t.Errorf("卡片上应只写「NAT」，实际 %q", nat.Value)
	}
	if strings.Contains(nat.Value, "（") || strings.Contains(nat.Value, "(") {
		t.Error("值里不该带括号解释 —— 那正是撑变形的原因")
	}
	// 但解释不能丢：挪到悬停提示
	if !strings.Contains(nat.Tip, "共享公网 IP") {
		t.Errorf("悬停提示里要保留完整解释，实际 %q", nat.Tip)
	}
	// 不是 NAT 的机器不该多这一项
	plain := nodeSpecs(model.Node{}, nil)
	for _, it := range plain {
		if it.Label == "网络" {
			t.Error("非 NAT 机器不该有「网络」项")
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
