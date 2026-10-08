package theme

// .kokoro-theme 包的往返与攻击面测试。
//
// 这里的原则是：每个安全断言都要有一个「真的构造出攻击样本」的用例，
// 否则测试只是确认代码能跑，没确认它挡得住东西。

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleManifest() *Manifest {
	return &Manifest{
		SchemaVersion:       ManifestVersion,
		ID:                  "acme.midnight-rain",
		Name:                "午夜雨",
		Version:             "1.0.0",
		Description:         "测试用主题",
		Author:              "acme",
		License:             "MIT",
		TokensSchemaVersion: TokensSchemaVersion,
		Tokens: map[string]string{
			"--kokoro-color-primary": "#7c5cff",
			"--kokoro-color-bg":      "#0d1117",
		},
		LayoutSchemaVersion: LayoutSchemaVersion,
		Layout: Layout{
			Charts: ChartsLayout{Type: "line"},
		},
	}
}

// ---- 往返 ----

func TestPackageRoundTrip(t *testing.T) {
	m := sampleManifest()
	assets := map[string][]byte{
		"bg.png":     []byte("\x89PNG fake background"),
		"font.woff2": []byte("fake font"),
	}
	raw, err := BuildPackage(m, assets, []byte("fake preview"), "MIT License", "# 午夜雨")
	if err != nil {
		t.Fatalf("BuildPackage: %v", err)
	}
	if len(raw) < 4 || !bytes.Equal(raw[:2], []byte("PK")) {
		t.Fatal("生成的包不是 zip")
	}

	b, err := ExtractPackage(raw)
	if err != nil {
		t.Fatalf("ExtractPackage: %v", err)
	}
	// 清单必须能原样解析回来。
	got, err := Parse(b.ManifestRaw)
	if err != nil {
		t.Fatalf("包内清单解析失败: %v", err)
	}
	if got.ID != m.ID || got.Name != m.Name {
		t.Fatalf("往返后身份不对: %s / %s", got.ID, got.Name)
	}
	if got.Tokens["--kokoro-color-primary"] != "#7c5cff" {
		t.Fatalf("tokens 丢失: %+v", got.Tokens)
	}
	// 包元数据应被 BuildPackage 自动填好。
	if got.Package == nil {
		t.Fatal("BuildPackage 没有回填 package 段")
	}
	if got.Package.SHA256 != b.SHA256 {
		t.Fatalf("声明哈希 %q 与实际 %q 不一致", got.Package.SHA256, b.SHA256)
	}
	if len(got.Package.Files) != 2 {
		t.Fatalf("files 清单应有 2 项，实际 %d: %v", len(got.Package.Files), got.Package.Files)
	}
	if err := b.ValidateAgainstPackage(); err != nil {
		t.Fatalf("一致性核对失败: %v", err)
	}
	if len(b.Assets) != 2 {
		t.Fatalf("资源应有 2 个，实际 %d", len(b.Assets))
	}
	if string(b.Preview) != "fake preview" {
		t.Fatalf("preview 读错: %q", b.Preview)
	}
	if b.License != "MIT License" {
		t.Fatalf("LICENSE 读错: %q", b.License)
	}
	if b.Readme != "# 午夜雨" {
		t.Fatalf("README 读错: %q", b.Readme)
	}
}

// 资源哈希必须真的覆盖内容：改一个字节就该对不上。
func TestPackageChecksumDetectsTamper(t *testing.T) {
	m := sampleManifest()
	raw, err := BuildPackage(m, map[string][]byte{"bg.png": []byte("original")}, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// 重打包一个 assets 内容不同、但保留原 theme.json 的包。
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("theme.json")
	// 从原包里取出 theme.json 原文以保留声明的哈希。
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var manifestRaw []byte
	for _, f := range zr.File {
		if f.Name == "theme.json" {
			rc, _ := f.Open()
			var b bytes.Buffer
			b.ReadFrom(rc)
			rc.Close()
			manifestRaw = b.Bytes()
		}
	}
	w.Write(manifestRaw)
	w2, _ := zw.Create("assets/bg.png")
	w2.Write([]byte("TAMPERED"))
	zw.Close()

	if _, err := ExtractPackage(buf.Bytes()); err == nil {
		t.Fatal("资源被篡改后校验和应当不匹配，但通过了")
	} else if !strings.Contains(err.Error(), "校验和不匹配") {
		t.Fatalf("错误信息应说明校验和不匹配，实际: %v", err)
	}
}

// ---- 攻击面 ----

func TestPackageRejectsNonZip(t *testing.T) {
	if _, err := ExtractPackage([]byte("这不是 zip")); err == nil {
		t.Fatal("非 zip 内容应当被拒绝")
	}
	if _, err := ExtractPackage(nil); err == nil {
		t.Fatal("空内容应当被拒绝")
	}
}

// zip-slip：条目名试图跳出包目录。
func TestPackageRejectsZipSlip(t *testing.T) {
	cases := []struct {
		name  string
		entry string
	}{
		{"父目录", "../../../etc/cron.d/evil"},
		{"嵌入父目录", "assets/../../evil.txt"},
		{"绝对路径", "/etc/passwd"},
		{"windows反斜杠", `..\..\evil.txt`},
		{"盘符", "C:/windows/evil.txt"},
		{"UNC", `\\host\share\x`},
		{"非白名单目录", "js/evil.js"},
		{"css目录", "css/overlay.css"},
		{"根目录散文件", "evil.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := zipWithEntries(t, map[string]string{
				"theme.json": manifestJSON(t),
				tc.entry:     "payload",
			})
			if _, err := ExtractPackage(raw); err == nil {
				t.Fatalf("条目 %q 应当被拒绝，但通过了", tc.entry)
			}
		})
	}
}

// 压缩炸弹：声明解压后体积巨大。
func TestPackageRejectsZipBomb(t *testing.T) {
	raw := zipWithEntries(t, map[string]string{
		"theme.json": manifestJSON(t),
		// 4MB 的重复内容，压缩比极高但绝对体积也超过单文件上限。
		"assets/bomb.bin": strings.Repeat("A", 4<<20),
	})
	_, err := ExtractPackage(raw)
	if err == nil {
		t.Fatal("超大条目应当被拒绝")
	}
	// 单文件上限 2MB，压缩比上限 100 —— 命中哪个都算过。
	if !strings.Contains(err.Error(), "超过") && !strings.Contains(err.Error(), "炸弹") {
		t.Fatalf("错误信息应说明超限，实际: %v", err)
	}
}

func TestPackageRejectsEntryCount(t *testing.T) {
	files := map[string]string{"theme.json": manifestJSON(t)}
	for i := 0; i < MaxPackageFiles+10; i++ {
		files["assets/f"+itoa(i)+".txt"] = "x"
	}
	raw := zipWithEntries(t, files)
	if _, err := ExtractPackage(raw); err == nil {
		t.Fatalf("%d 个条目应超过上限 %d 而被拒绝", MaxPackageFiles+10, MaxPackageFiles)
	}
}

func TestPackageRejectsMissingManifest(t *testing.T) {
	raw := zipWithEntries(t, map[string]string{"assets/a.txt": "x"})
	if _, err := ExtractPackage(raw); err == nil {
		t.Fatal("缺少 theme.json 的包应当被拒绝")
	}
}

// 官方前缀保护在包格式里同样生效。
func TestPackageRejectsOfficialPrefix(t *testing.T) {
	m := sampleManifest()
	m.ID = "kokoro.fake-official"
	raw, err := BuildPackage(m, nil, nil, "", "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ExtractPackage(raw)
	if err != nil {
		t.Fatalf("解包本身应成功: %v", err)
	}
	if _, err := Parse(b.ManifestRaw); err != nil {
		t.Fatalf("清单解析应成功: %v", err)
	}
	reg, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Import(b.ManifestRaw); err == nil {
		t.Fatal("冒用官方 kokoro. 前缀的主题必须被拒绝")
	}
}

// 包内清单非法时必须在登记前就被挡住。
func TestPackageRejectsBadManifest(t *testing.T) {
	bad := `{"schemaVersion":1,"id":"x.y","name":"X","version":"1.0.0",
	"tokens":{"--kokoro-color-primary":"red; background:url(https://evil.tld/x)"},
	"layout":{}}`
	raw := zipWithEntries(t, map[string]string{"theme.json": bad})
	b, err := ExtractPackage(raw)
	if err != nil {
		return // 解包阶段就拒了也算对
	}
	if _, err := Parse(b.ManifestRaw); err == nil {
		t.Fatal("含注入的 tokens 必须被拒绝")
	}
}

// ---- helper ----

func manifestJSON(t *testing.T) string {
	t.Helper()
	raw, err := json.Marshal(sampleManifest())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// zipWithEntries 造一个 zip。map 键是条目名，值是内容。
func zipWithEntries(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// ---- 真实世界用例：文档里的最小示例必须真能被打成包 ----

func TestBuiltinManifestsCanBePackaged(t *testing.T) {
	reg, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range reg.List() {
		raw, err := BuildPackage(m, nil, nil, m.License, "")
		if err != nil {
			t.Fatalf("内置主题 %s 打包失败: %v", m.ID, err)
		}
		b, err := ExtractPackage(raw)
		if err != nil {
			t.Fatalf("内置主题 %s 的包解不开: %v", m.ID, err)
		}
		if _, err := Parse(b.ManifestRaw); err != nil {
			t.Fatalf("内置主题 %s 的包内清单解析失败: %v", m.ID, err)
		}
	}
}

// 确认 docs 里给出的最小示例确实是合法的（避免文档与实现漂移）。
func TestDocMinimalExampleIsValid(t *testing.T) {
	for _, name := range []string{"minimal.json", "example-terminal.json", "example-paper.json"} {
		p := filepath.Join("..", "..", "docs", "themes", name)
		data, err := os.ReadFile(p)
		if err != nil {
			t.Errorf("示例文件不存在: %v", err)
			continue
		}
		if _, err := Parse(data); err != nil {
			t.Errorf("文档示例 %s 不合法: %v", name, err)
		}
	}
}

// 表面别名类变量（header-bg 等）必须接受 rgba()/color-mix() 这类值。
//
// 这条曾经是个真 bug：它们被判成「关键字」类型，而关键字类型拒绝一切括号，
// 于是 `rgba(250,247,240,.82)` 这种完全正常的半透明顶栏色被拒。
// 主题作者只会看到「关键字值里不应出现括号」，完全猜不到该改哪里。
func TestSurfaceAliasTokensAcceptColorValues(t *testing.T) {
	base := map[string]string{
		"--kokoro-color-primary": "#a8562f",
	}
	tokens := map[string]string{
		"--kokoro-header-bg":   "rgba(250, 247, 240, .82)",
		"--kokoro-footer-bg":   "color-mix(in srgb, #fff 70%, transparent)",
		"--kokoro-card-bg":     "#fffdf9",
		"--kokoro-card-border": "var(--kokoro-color-border)",
		"--kokoro-header-fg":   "var(--kokoro-color-text)",
		"--kokoro-code-bg":     "rgb(240, 235, 225)",
	}
	for k, v := range tokens {
		base[k] = v
	}
	m := &Manifest{
		SchemaVersion:       ManifestVersion,
		ID:                  "test.surface-alias",
		Name:                "别名",
		Version:             "1.0.0",
		TokensSchemaVersion: TokensSchemaVersion,
		Tokens:              base,
		LayoutSchemaVersion: LayoutSchemaVersion,
	}
	if err := m.Validate(); err != nil {
		t.Fatalf("表面别名类变量应接受颜色值，却报错: %v", err)
	}

	// 但仍然要守住注入面：不配对的括号必须被拒。
	bad := map[string]string{
		"--kokoro-color-primary": "#fff",
		"--kokoro-header-bg":     "rgba(250, 247, 240",
	}
	m2 := &Manifest{
		SchemaVersion:       ManifestVersion,
		ID:                  "test.bad-surface",
		Name:                "坏别名",
		Version:             "1.0.0",
		TokensSchemaVersion: TokensSchemaVersion,
		Tokens:              bad,
		LayoutSchemaVersion: LayoutSchemaVersion,
	}
	if err := m2.Validate(); err == nil {
		t.Error("括号不配对必须被拒绝")
	}
}
