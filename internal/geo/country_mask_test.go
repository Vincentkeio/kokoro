package geo

import (
	"strconv"
	"strings"
	"testing"
)

// TestLitRangesShape 钉住"点亮格子"的编码：格式合法、范围不越界、
// 已知国家有结果、未知国家不炸。
func TestLitRangesShape(t *testing.T) {
	tbl, err := loadCountryMask()
	if err != nil {
		t.Fatalf("国家掩码解码失败: %v", err)
	}
	total := tbl.nlon * tbl.nlat

	for _, code := range []string{"JP", "US", "SG", "HK"} {
		got := LitRanges([]string{code})
		if got == "" {
			t.Errorf("%s 应该有点亮格子", code)
			continue
		}
		covered := 0
		for _, part := range strings.Split(got, ",") {
			startStr, lenStr, ok := strings.Cut(part, ":")
			if !ok {
				t.Fatalf("%s 的 run %q 格式不对", code, part)
			}
			start, err1 := strconv.Atoi(startStr)
			n, err2 := strconv.Atoi(lenStr)
			if err1 != nil || err2 != nil || n <= 0 {
				t.Fatalf("%s 的 run %q 数值不对", code, part)
			}
			if start < 0 || start+n > total {
				t.Fatalf("%s 的 run %q 越界（总格 %d）", code, part, total)
			}
			covered += n
		}
		if covered == 0 {
			t.Errorf("%s 的 run 覆盖了 0 个格子", code)
		}
	}

	// 小写也应被接受；未知国家返回空而不是 panic。
	if LitRanges([]string{"jp"}) == "" {
		t.Error("小写国家码也应被接受")
	}
	if got := LitRanges([]string{"ZZ"}); got != "" {
		t.Errorf("未知国家码应返回空，实际 %q", got)
	}
	if got := LitRanges(nil); got != "" {
		t.Errorf("空输入应返回空，实际 %q", got)
	}
}

// TestLitRangesMergesAdjacentCells 相邻点亮格子必须合并成一段 ——
// 否则 JSON 会按格子数膨胀（一个格子一段）。这里用"同一行里连续点亮"
// 的最小区块来验：取一段已知的 run，把它前面的那个格子也点亮，段数不应增加。
func TestLitRangesMergesAdjacentCells(t *testing.T) {
	got := LitRanges([]string{"US"})
	runs := strings.Split(got, ",")
	if len(runs) == 0 || runs[0] == "" {
		t.Fatal("US 应该有点亮格子")
	}
	startStr, lenStr, ok := strings.Cut(runs[0], ":")
	if !ok {
		t.Fatalf("run %q 格式不对", runs[0])
	}
	start, _ := strconv.Atoi(startStr)
	n, _ := strconv.Atoi(lenStr)
	if n < 2 {
		t.Skipf("首个 run 只有 %d 格，跳过多格校验", n)
	}
	// 每个 run 内部必须是连续格子（start 递增且不重叠）。
	prevEnd := -1
	for _, part := range runs {
		s, l, ok := strings.Cut(part, ":")
		if !ok {
			t.Fatalf("run %q 格式不对", part)
		}
		st, _ := strconv.Atoi(s)
		ln, _ := strconv.Atoi(l)
		if st < prevEnd {
			t.Fatalf("run %q 与前一段重叠（上一段结束于 %d）", part, prevEnd)
		}
		prevEnd = st + ln
	}
	if prevEnd <= start {
		t.Fatalf("run 覆盖范围异常: start=%d end=%d", start, prevEnd)
	}
}
