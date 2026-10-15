package geo

import (
	_ "embed"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// 本文件读取 scripts/gen-country-mask.py 生成的国家栅格（country.bin）。
//
// 用途：首页地球要"点亮"有节点的国家/地区 —— 也就是把那片陆地的格子
// 换一个高亮色。判断"格子属于哪个国家"在浏览器里做不动（每帧 24 万次
// 多边形测试），所以离线栅格化成索引表，内嵌进二进制，运行时只查表。
//
// 客户端**不下载**这份表：Hub 只把它算出来的"点亮格子"压缩成
// "start:len,start:len" 塞进地球 JSON（几 KB），前端按这个把纹理染色。
// 表格体积随国家大小增长，跟访客数量无关。

//go:embed country.bin
var countryMaskBin []byte

type countryMaskTable struct {
	nlon, nlat int
	codeIdx    map[string]int
	idxCode    []string
	runLen     []int32
	runCode    []uint8
}

var (
	countryMaskOnce sync.Once
	countryMaskData countryMaskTable
	countryMaskErr  error
)

func loadCountryMask() (countryMaskTable, error) {
	countryMaskOnce.Do(func() {
		countryMaskData, countryMaskErr = decodeCountryMask(countryMaskBin)
	})
	return countryMaskData, countryMaskErr
}

func decodeCountryMask(blob []byte) (countryMaskTable, error) {
	var t countryMaskTable
	const headLen = 4 + 2 + 2 + 4 + 4 + 4 + 2
	if len(blob) < headLen || string(blob[:4]) != "KCM1" {
		return t, fmt.Errorf("国家掩码格式不对")
	}
	le := binary.LittleEndian
	// 头部布局（与 gen-country-mask.py 的 struct.pack("<4sHHfffH") 严格对应）：
	//   magic 0..3 | nlon 4..5 | nlat 6..7 | latMax 8..11 | latMin 12..15
	//   | step 16..19 | nCodes 20..21
	t.nlon = int(le.Uint16(blob[4:]))
	t.nlat = int(le.Uint16(blob[6:]))
	nCodes := int(le.Uint16(blob[20:]))
	pos := headLen
	if nCodes <= 0 || len(blob) < pos+nCodes*2 {
		return t, fmt.Errorf("国家掩码调色板损坏")
	}
	t.idxCode = make([]string, nCodes)
	for i := 0; i < nCodes; i++ {
		t.idxCode[i] = string(blob[pos : pos+2])
		pos += 2
	}
	t.codeIdx = make(map[string]int, nCodes)
	for i, c := range t.idxCode {
		t.codeIdx[c] = i + 1 // 0 表示"无归属"，所以调色板从 1 开始
	}

	// 变长整数 + 单字节码的 run 流
	for pos < len(blob) {
		var count int32
		var shift uint
		for {
			if pos >= len(blob) {
				return t, fmt.Errorf("国家掩码 run 截断")
			}
			b := blob[pos]
			pos++
			count |= int32(b&0x7F) << shift
			if b&0x80 == 0 {
				break
			}
			shift += 7
		}
		if pos >= len(blob) {
			return t, fmt.Errorf("国家掩码 run 缺码位")
		}
		t.runCode = append(t.runCode, blob[pos])
		t.runLen = append(t.runLen, count)
		pos++
	}
	if total := int64(t.nlon) * int64(t.nlat); int64(sumRuns(t.runLen)) != total {
		return t, fmt.Errorf("国家掩码格子数对不上：%d != %d", sumRuns(t.runLen), total)
	}
	return t, nil
}

func sumRuns(lens []int32) int32 {
	var n int32
	for _, v := range lens {
		n += v
	}
	return n
}

// LitRanges 返回这些国家（ISO 3166-1 alpha-2，大小写不敏感）占据的格子，
// 压缩成 "start:len,start:len" 的形式。
//
// 索引是**纹理序**（与 globe.js 的 landTextureData 一致：0 号格在西南角），
// 前端拿到就能直接写纹理，不需要再做一次行列换算。
// 相邻的点亮 run 会被合并，所以返回值长度只跟"点亮区域的形状"有关。
func LitRanges(codes []string) string {
	if len(codes) == 0 {
		return ""
	}
	t, err := loadCountryMask()
	if err != nil {
		return ""
	}
	want := make(map[uint8]bool, len(codes))
	for _, c := range codes {
		if idx, ok := t.codeIdx[strings.ToUpper(strings.TrimSpace(c))]; ok {
			want[uint8(idx)] = true
		}
	}
	if len(want) == 0 {
		return ""
	}

	var b strings.Builder
	pos := 0
	runStart, runLen := -1, 0
	flush := func() {
		if runStart < 0 || runLen == 0 {
			return
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%d:%d", runStart, runLen)
		runStart, runLen = -1, 0
	}
	for i, n := range t.runLen {
		n32 := int(n)
		if want[t.runCode[i]] {
			if runStart < 0 {
				runStart = pos
			}
			runLen += n32
		} else {
			flush()
		}
		pos += n32
	}
	flush()
	return b.String()
}

// LitCountries 返回掩码里已知的国家码（升序），供调试与测试使用。
func LitCountries() []string {
	t, err := loadCountryMask()
	if err != nil {
		return nil
	}
	out := append([]string(nil), t.idxCode...)
	sort.Strings(out)
	return out
}
