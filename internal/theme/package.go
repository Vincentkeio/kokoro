package theme

// .kokoro-theme 标准主题包的读写与安全校验。
//
// 为什么要有「包」这一层：裸 theme.json 只能换配色与布局偏好，而一套完整的
// 皮肤往往还需要自定义字体、背景图、预览图。这些二进制资源没法塞进 JSON，
// 于是定义一个 zip 容器——但 zip 天生是攻击面最大的格式之一：
//
//   - 路径穿越（zip-slip）：条目名写 ../../etc/cron.d/x 就能写到任意路径；
//   - 符号链接：解压出指向 /etc/passwd 的链接，后续读取等于任意文件读；
//   - 炸弹：几 KB 压缩包解开几百 MB，把磁盘和内存打满；
//   - 数量爆炸：几万个空条目耗尽文件句柄与 inode；
//   - 远程资源：主题 CSS 里挂 url(https://evil.tld/x.js) 让访客浏览器去拉。
//
// 所以这里的每个环节都有硬上限，并且**校验全部通过之后才允许登记**。
// 包只是「内容」，真正的能力边界还是 tokens 白名单 + layout 枚举：
// 包里即便带了 css/overlay.css，Hub 也只把它当不可执行资源存放，
// 绝不注入到页面里。

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// 包格式硬限制。数值取向偏保守：自用主题包通常 < 2MB。
const (
	// PackageExt 是标准主题包扩展名。
	PackageExt = ".kokoro-theme"
	// MaxPackageBytes 是压缩包体积上限。
	MaxPackageBytes = 8 << 20 // 8MB
	// MaxPackageFiles 是条目数上限（防几万个空条目耗句柄）。
	MaxPackageFiles = 64
	// MaxFileBytes 是单个文件解出后的体积上限。
	MaxFileBytes = 2 << 20 // 2MB
	// MaxTotalBytes 是全部文件解压后体积合计上限（防压缩炸弹）。
	MaxTotalBytes = 12 << 20 // 12MB
	// MaxCompressionRatio 是压缩比上限，100 是极宽松的门槛。
	MaxCompressionRatio = 100
)

// 包内固定路径。刻意收得很窄：只认这几个，多余的一律拒绝而不是忽略。
const (
	manifestName = "theme.json"
	assetsDir    = "assets/"
	previewName  = "preview.png"
	licenseName  = "LICENSE"
	readmeName   = "README.md"
)

// Package 描述一份主题包的完整性信息。
//
// SHA256 是「除 theme.json 外全部条目按路径排序后逐个哈希再汇总」的结果，
// 因此它能覆盖 assets 里的任何一个字节；而 theme.json 本身因为要读出来解析，
// 不能自包含哈希，改由 Hub 在导入时计算并记录到 themes.checksum 列。
type Package struct {
	Format string   `json:"format"`           // 固定 "kokoro-theme"
	Files  []string `json:"files,omitempty"`  // 相对路径清单
	SHA256 string   `json:"sha256,omitempty"` // 内容汇总哈希（小写 hex）
	Signed bool     `json:"signed,omitempty"` // 是否附带签名（当前仅记录，不强校验）
	Bundle string   `json:"bundle,omitempty"` // 预留：签名者 ID
}

// Asset 是包内的一个附加资源。
type Asset struct {
	Name string // 归一化后的相对路径，如 assets/bg.png
	Data []byte
}

// PackageBundle 是一份解包后的主题包。
type PackageBundle struct {
	Manifest    *Manifest
	ManifestRaw []byte
	Assets      []Asset
	Preview     []byte
	License     string
	Readme      string
	// SHA256 是全部资源条目按路径排序汇总出的哈希。
	SHA256 string
	// VerifiedFiles 是实际读到并纳入校验的资源条目名（已排序）。
	VerifiedFiles []string
	// DeclaredFiles / DeclaredSHA 是 theme.json 里声明的值，已与实际核对过。
	DeclaredFiles []string
	DeclaredSHA   string
	// Signed 记录作者是否声称包已签名。当前只展示不校验，
	// 因为没有可信的公钥分发渠道，假装校验比不校验更危险。
	Signed bool
}

// parsePackageManifest 只用来在解包时读取 package 段做一致性核对。
type parsePackageManifest struct {
	Package struct {
		Format string   `json:"format"`
		Files  []string `json:"files"`
		SHA256 string   `json:"sha256"`
		Signed bool     `json:"signed"`
		Bundle string   `json:"bundle"`
	} `json:"package"`
}

// ExtractPackage 解析一份 .kokoro-theme（zip）包。
//
// 顺序刻意是「先限流 → 再校验路径 → 最后才解压」：
// 每一步都在尽量早的位置把攻击面切掉。
func ExtractPackage(raw []byte) (*PackageBundle, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("主题包为空")
	}
	if len(raw) > MaxPackageBytes {
		return nil, fmt.Errorf("主题包 %d 字节，超过上限 %d", len(raw), MaxPackageBytes)
	}
	// 魔数先验一把，避免把任意文件当 zip 硬解。
	if len(raw) < 4 || !bytes.Equal(raw[:2], []byte("PK")) {
		return nil, fmt.Errorf("不是有效的 %s 包（缺少 zip 魔数）", PackageExt)
	}

	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("解压失败: %w", err)
	}
	if len(zr.File) == 0 {
		return nil, fmt.Errorf("主题包里没有任何文件")
	}
	if len(zr.File) > MaxPackageFiles {
		return nil, fmt.Errorf("主题包有 %d 个条目，超过上限 %d", len(zr.File), MaxPackageFiles)
	}

	var (
		manifestRaw []byte
		preview     []byte
		licenseTxt  string
		readmeTxt   string
		assets      []Asset
		total       int64
		sum         = sha256.New()
		names       []string
	)

	for _, f := range zr.File {
		name, err := safeEntryName(f.Name)
		if err != nil {
			return nil, err
		}
		// 目录条目：只允许 assets/ 下的，且不落盘。
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		if f.UncompressedSize64 > MaxFileBytes {
			return nil, fmt.Errorf("条目 %s 解压后 %d 字节，超过单文件上限", name, MaxFileBytes)
		}
		// 压缩炸弹：解压后比压缩包大两个数量级以上，几乎必然是炸弹。
		if f.CompressedSize64 > 0 && f.UncompressedSize64/f.CompressedSize64 > MaxCompressionRatio {
			return nil, fmt.Errorf("条目 %s 压缩比 %d:1 超过上限，说明是压缩炸弹",
				name, f.UncompressedSize64/max64(f.CompressedSize64, 1))
		}
		total += int64(f.UncompressedSize64)
		if total > MaxTotalBytes {
			return nil, fmt.Errorf("解压后合计超过 %d 字节上限", MaxTotalBytes)
		}

		data, err := readZipEntry(f, MaxFileBytes)
		if err != nil {
			return nil, err
		}

		switch {
		case name == manifestName:
			manifestRaw = data
		case name == previewName:
			if len(preview) == 0 {
				preview = data
			}
		case name == licenseName:
			licenseTxt = limitText(data)
		case name == readmeName:
			readmeTxt = limitText(data)
		case strings.HasPrefix(name, assetsDir):
			assets = append(assets, Asset{Name: name, Data: data})
			names = append(names, name)
			// 汇总哈希只覆盖附件与资源，theme.json 自身由 Hub 另行计算。
			fmt.Fprintf(sum, "%s\x00%d\x00", name, len(data))
			sum.Write(data)
		default:
			return nil, fmt.Errorf("主题包含不允许的条目 %q", name)
		}
	}

	if manifestRaw == nil {
		return nil, fmt.Errorf("主题包里缺少 %s", manifestName)
	}

	var pm parsePackageManifest
	if err := json.Unmarshal(manifestRaw, &pm); err != nil {
		return nil, fmt.Errorf("%s 解析失败: %w", manifestName, err)
	}
	if pm.Package.Format != "" && pm.Package.Format != "kokoro-theme" {
		return nil, fmt.Errorf("不支持的包格式 %q", pm.Package.Format)
	}

	b := &PackageBundle{
		ManifestRaw: manifestRaw,
		Preview:     preview,
		License:     licenseTxt,
		Readme:      readmeTxt,
		Assets:      assets,
		SHA256:      hex.EncodeToString(sum.Sum(nil)),
	}
	sort.Strings(names)
	b.VerifiedFiles = names

	// 声明与实际必须一致：不一致意味着包被改过或传输损坏，
	// 静默接受会让「校验和」变成一句空话。
	if len(pm.Package.Files) > 0 {
		declared := append([]string(nil), pm.Package.Files...)
		sort.Strings(declared)
		if strings.Join(declared, ",") != strings.Join(b.VerifiedFiles, ",") {
			return nil, fmt.Errorf("包内清单与实际文件不一致：声明 %d 个，实际 %d 个",
				len(declared), len(b.VerifiedFiles))
		}
	}
	if want := strings.ToLower(strings.TrimSpace(pm.Package.SHA256)); want != "" {
		if want != b.SHA256 {
			return nil, fmt.Errorf("校验和不匹配：声明 %s，实际 %s", short(want), short(b.SHA256))
		}
	}
	b.DeclaredFiles = pm.Package.Files
	b.DeclaredSHA = pm.Package.SHA256
	b.Signed = pm.Package.Signed
	return b, nil
}

// ReadManifest 从包内取出清单原文，供 Parse 使用。
func (b *PackageBundle) ReadManifest() ([]byte, error) {
	if b == nil || len(b.ManifestRaw) == 0 {
		return nil, fmt.Errorf("包内没有 %s", manifestName)
	}
	return b.ManifestRaw, nil
}

// ValidateAgainstPackage 核对包内声明的 files/sha256 与实际内容是否一致。
//
// ExtractPackage 内部已经做过一次核对；这个方法留给「拿到包后想再验一遍」
// 的调用方（例如管理员手动上传后在后台展示校验状态）。
func (b *PackageBundle) ValidateAgainstPackage() error {
	if b == nil {
		return fmt.Errorf("theme: 包为空")
	}
	if len(b.DeclaredFiles) > 0 {
		declared := append([]string(nil), b.DeclaredFiles...)
		sort.Strings(declared)
		if strings.Join(declared, ",") != strings.Join(b.VerifiedFiles, ",") {
			return fmt.Errorf("theme: 包内清单与实际文件不一致")
		}
	}
	if want := strings.ToLower(strings.TrimSpace(b.DeclaredSHA)); want != "" && want != b.SHA256 {
		return fmt.Errorf("theme: 校验和不匹配")
	}
	return nil
}

// Meta 暴露包元数据给调用方做持久化与展示。
//
// 注意它返回的是按「实际内容」算出的值，而 Manifest.Package 里存的是
// 作者声明的值——两者经过 ExtractPackage 的核对才被允许共存。
func (b *PackageBundle) Meta() *Package {
	if b == nil {
		return nil
	}
	return &Package{
		Format: "kokoro-theme",
		Files:  b.VerifiedFiles,
		SHA256: b.SHA256,
		Signed: b.Signed,
	}
}

// safeEntryName 校验并归一化 zip 条目名，挡掉 zip-slip 与绝对路径。
//
// 判定顺序刻意这样：
//  1. 反斜杠一律拒绝（Windows 上 "\" 同样是分隔符）；
//  2. 拒绝盘符与 UNC 前缀（"C:"、"\\\\host"）；
//  3. 拒绝绝对路径；
//  4. 逐段拒绝 "." ".." 与空段；
//  5. 只允许出现在白名单前缀下。
func safeEntryName(raw string) (string, error) {
	if raw == "" {
		return "", fmt.Errorf("主题包含空条目名")
	}
	if strings.Contains(raw, "\\") {
		return "", fmt.Errorf("条目名 %q 含反斜杠，拒绝", raw)
	}
	if strings.ContainsAny(raw, "\x00") {
		return "", fmt.Errorf("条目名含空字节")
	}
	// Windows 盘符 / 冒号，都可能让拼接后的路径跳到别处。
	if i := strings.IndexByte(raw, ':'); i >= 0 {
		return "", fmt.Errorf("条目名 %q 含冒号，拒绝", raw)
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("条目名 %q 是绝对路径，拒绝", raw)
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("条目名 %q 试图跳出包目录（zip-slip），拒绝", raw)
	}
	if clean != strings.TrimSuffix(raw, "/") {
		return "", fmt.Errorf("条目名 %q 不是规范路径（应写成 %q）", raw, clean)
	}
	switch {
	case clean == manifestName, clean == previewName,
		clean == licenseName, clean == readmeName:
		return clean, nil
	case strings.HasPrefix(clean, assetsDir):
		rest := strings.TrimPrefix(clean, assetsDir)
		if rest == "" || strings.Contains(rest, "..") {
			return "", fmt.Errorf("资源路径 %q 非法", clean)
		}
		if len(rest) > 128 {
			return "", fmt.Errorf("资源文件名过长")
		}
		return clean, nil
	}
	return "", fmt.Errorf("主题包含不允许的条目 %q（只允许 %s、%s、%s、%s 与 %s*）",
		clean, manifestName, previewName, licenseName, readmeName, assetsDir)
}

// SafeAssetPath 校验并归一化一个主题资源的相对路径（不含 assets/ 前缀）。
//
// 入参是 URL 里的那段（如 "bg/hero.png"），返回可直接查表的完整条目名
// （"assets/bg/hero.png"）。校验强度与 safeEntryName 对齐：
// 拒绝反斜杠、空字节、绝对路径、以及任何形式的目录回退。
//
// 这条函数是 /_theme-assets/ 路由的第一道闸——URL 是用户可控输入，
// 而拼出来的路径要拿去查包内资源表，不校验就等于把 zip-slip 从包内
// 搬到了 HTTP 层。
func SafeAssetPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("资源名为空")
	}
	if strings.Contains(name, "\\") || strings.ContainsAny(name, "\x00") {
		return "", fmt.Errorf("资源名含非法字符")
	}
	if strings.Contains(name, ":") {
		return "", fmt.Errorf("资源名含冒号")
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("资源名不能是绝对路径")
	}
	if len(name) > 160 {
		return "", fmt.Errorf("资源名过长")
	}
	clean := path.Clean(name)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("资源名试图跳出包目录")
	}
	if clean != name {
		return "", fmt.Errorf("资源名不是规范路径")
	}
	return assetsDir + clean, nil
}

// readZipEntry 读一个条目，带体积上限与符号链接拒绝。
func readZipEntry(f *zip.File, limit int64) ([]byte, error) {
	// 符号链接在 zip 里通过 mode 位标记；Go 的 zip 包不直接暴露，
	// 但外部属性里的 unix mode 高位能看出 S_IFLNK。这里宁可误杀：
	// 主题包本来也不该带链接。
	if f.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("条目 %s 是符号链接，拒绝", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("打开条目 %s 失败: %w", f.Name, err)
	}
	defer rc.Close()
	// 多读 1 字节用于识别「声明大小与实际不符」，防止 zip 谎报体积绕过上限。
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, fmt.Errorf("读取条目 %s 失败: %w", f.Name, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("条目 %s 解压后超过 %d 字节", f.Name, limit)
	}
	return data, nil
}

// limitText 把文本类附件裁到合理长度，避免一个 2MB 的 README 塞进内存。
func limitText(b []byte) string {
	const max = 16 << 10
	if len(b) > max {
		b = b[:max]
	}
	return string(b)
}

func max64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func short(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12] + "…"
}

// BuildPackage 把一份清单与资源打成 .kokoro-theme 包。
//
// 这是给主题作者用的正向工具，也是内置主题导出成可分发包的那条路径；
// 与 ExtractPackage 成对，保证「能打的包一定能解」。
//
// 组装顺序上有个必须注意的点：汇总哈希要写进 theme.json 的 package.sha256，
// 而 theme.json 又是包里的第一个条目。所以只能「先在内存里把 assets 哈希算完 →
// 填进清单 → 再一次性写出所有条目」，不能边写边算（那样清单已经写出去了，
// 哈希无处安放）。这也正是下面先把 assets 收集成列表、再统一 write 的原因。
func BuildPackage(m *Manifest, assets map[string][]byte, preview []byte, license, readme string) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("theme: 主题为空")
	}

	// 1. 资源按路径排序，汇总哈希。
	names := make([]string, 0, len(assets))
	for n := range assets {
		names = append(names, n)
	}
	sort.Strings(names)

	sum := sha256.New()
	fullNames := make([]string, 0, len(names))
	for _, n := range names {
		full := assetsDir + n
		fullNames = append(fullNames, full)
		fmt.Fprintf(sum, "%s\x00%d\x00", full, len(assets[n]))
		sum.Write(assets[n])
	}
	checksum := hex.EncodeToString(sum.Sum(nil))

	// 2. 把包元数据填回清单（保留作者可能已写的 signed/bundle）。
	pkg := m.Package
	if pkg == nil {
		pkg = &Package{}
	}
	pkg.Format = "kokoro-theme"
	if len(fullNames) > 0 {
		pkg.Files = fullNames
		pkg.SHA256 = checksum
	} else {
		pkg.Files = nil
		pkg.SHA256 = ""
	}
	m.Package = pkg

	// 3. 序列化清单，然后一次性写完所有条目。
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("theme: 序列化清单失败: %w", err)
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	write := func(name string, data []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if err := write(manifestName, raw); err != nil {
		return nil, fmt.Errorf("theme: 写 %s 失败: %w", manifestName, err)
	}
	if len(preview) > 0 {
		if err := write(previewName, preview); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(license) != "" {
		if err := write(licenseName, []byte(license)); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(readme) != "" {
		if err := write(readmeName, []byte(readme)); err != nil {
			return nil, err
		}
	}
	for _, n := range names {
		if err := write(assetsDir+n, assets[n]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("theme: 关闭 zip 失败: %w", err)
	}
	return buf.Bytes(), nil
}

// AssetNames 返回包内资源名（不含 assets/ 前缀）。
func (b *PackageBundle) AssetNames() []string {
	out := make([]string, 0, len(b.Assets))
	for _, a := range b.Assets {
		out = append(out, strings.TrimPrefix(a.Name, assetsDir))
	}
	return out
}
