package hub

// 文章配图的上传与回读。
//
// 只做「管理员上传图片 → 拿一个站内 URL」这一件事。
// 安全上按三条来设计：
//
//  1. **文件名不用用户给的**，用内容的 sha256。用户的文件名里可能有
//     ../ 或者 index.html 之类的东西，一旦进到磁盘路径就是灾难。
//     用哈希还有个附带好处：同一张图重复上传不会占两份空间。
//  2. **按魔数判类型**，不信文件名后缀，也不信 Content-Type ——
//     那两个都是客户端说了算的。后缀写 .png 实际是 html 的话，
//     存下来就是一个可被浏览器当页面渲染的同源文件。
//  3. **不接受 SVG**。SVG 是 XML，能带 <script>；同源下发等于给自己开后门。
//     （imageExt 那层已经拦了，这里再做一次内容校验兜底。）

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	// ⚠️⚠️ 这三个**空导入不能删**。
	//
	// image.DecodeConfig 只认识「通过 import 注册过」的解码器。
	// 只 import "image"（接口）是不够的 —— 那样它对任何图片都返回
	// "image: unknown format"，而我们的错误文案是"图片损坏或格式不被支持"，
	// 看起来像用户传了坏图。
	//
	// 这个 bug 一度被测试盖住了：upload_test.go 里 import 了 image/png
	// （用来造测试图），Go 把同包文件链在一起，**解码器被测试注册了**，
	// 于是测试全绿而线上必挂。所以测试现在也改成不 import 任何解码器。
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 上传限制。取向偏保守 —— 正文配图 2MB 足够，
// 再大应该先压一下再传（也能省访客的流量）。
const (
	uploadMaxBytes = 2 << 20
	uploadDirName  = "images"
)

// imgMagic 是允许的几种图片格式的魔数前缀。
//
// 顺序有讲究：PNG 和 JPEG 的前两字节不同，但都要在 GIF 之前判 ——
// 不过这里其实是精确前缀，顺序不影响正确性，只影响可读性。
var imgMagic = []struct {
	ext   string // 存盘用的后缀
	ctype string
	// head 是文件开头必须匹配的字节
	head []byte
}{
	{".png", "image/png", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}},
	{".jpg", "image/jpeg", []byte{0xFF, 0xD8, 0xFF}},
	{".gif", "image/gif", []byte{'G', 'I', 'F', '8'}},
	{".webp", "image/webp", []byte{'R', 'I', 'F', 'F'}},
}

// sniffImage 按内容判类型。认不出返回空。
func sniffImage(b []byte) (ext, ctype string) {
	for _, m := range imgMagic {
		if bytes.HasPrefix(b, m.head) {
			// WebP 还要看第 8~12 字节是不是 "WEBP"，
			// 否则任何 RIFF 文件（比如 wav）都会被当成图片。
			if m.ext == ".webp" && !bytes.HasPrefix(b[8:], []byte("WEBP")) {
				continue
			}
			return m.ext, m.ctype
		}
	}
	return "", ""
}

// imagesDir 是配图的落盘目录。
func (h *Hub) imagesDir() string {
	if h.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(h.cfg.DataDir, uploadDirName)
}

// handleUpload 处理 POST /admin/upload：收一张图，返回它的站内 URL。
//
// 返回 JSON（不是整页跳转）—— 前端是在编辑器里异步传的，
// 整页刷新会把没保存的正文冲掉。
func (h *Hub) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		drainBody(w, r)
		writeErr(w, http.StatusUnauthorized, "未登录")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	if h.cfg.DataDir == "" {
		writeErr(w, http.StatusInternalServerError, "服务端没配置数据目录，传不了图")
		return
	}
	// MaxBytesReader 在超限时会让后面的读直接报错，比读完再判更省事，
	// 也避免有人拿超大文件把我们读爆。
	r.Body = http.MaxBytesReader(w, r.Body, uploadMaxBytes+(64<<10))
	if err := r.ParseMultipartForm(uploadMaxBytes); err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "图片不能超过 2MB")
		return
	}
	f, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "没收到文件")
		return
	}
	defer f.Close()

	// 先读前 512 字节判类型 —— 一次读全再判也无所谓（有 2MB 上限），
	// 但先判能省掉把非图片读进内存那一步。
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]

	// 后缀只是"用户说的"，这里用来做**第一道粗筛**，
	// 真正说了算的是下面的魔数。两个都对才收。
	if _, ok := imageExt(hdr.Filename); !ok {
		writeErr(w, http.StatusBadRequest, "只支持 png / jpg / webp / gif")
		return
	}
	ext, ctype := sniffImage(head)
	if ext == "" {
		writeErr(w, http.StatusBadRequest,
			"这不是图片（按内容判断的）。只支持 png / jpg / webp / gif，不接受 svg。")
		return
	}

	// 把整份读进来算哈希。2MB 上限之内，内存里放得下。
	body, err := io.ReadAll(io.LimitReader(io.MultiReader(bytes.NewReader(head), f), uploadMaxBytes+1))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取上传内容失败")
		return
	}
	if len(body) > uploadMaxBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "图片不能超过 2MB")
		return
	}
	// 声称是图片但要**真能解出来** —— 挡住"前 8 字节是 PNG 魔数、后面是垃圾"。
	// 不做这一步的话，浏览器加载时才报错，而那时站长已经以为传成功了。
	//
	// ⚠️ WebP 例外：Go 标准库**没有** WebP 解码器（要加 golang.org/x/image）。
	// 本项目坚持零外部依赖，所以对 WebP 只做魔数 + RIFF/WEBP 签名校验，
	// 不做深解。代价是"签名对但内容是坏的 webp"能传上来 ——
	// 影响很小（那张图加载不出来而已），比引一个依赖划算。
	if ext != ".webp" {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(body)); err != nil || cfg.Width <= 0 {
			writeErr(w, http.StatusBadRequest, "图片损坏或格式不被支持")
			return
		}
	}

	sum := sha256.Sum256(body)
	name := hex.EncodeToString(sum[:])[:32] + ext // 32 位十六进制够用，文件名也短些
	dir := h.imagesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, "创建图片目录失败")
		return
	}
	// 内容寻址：同一个文件重复传会写同一个名字，直接覆盖，不占两份。
	dst := filepath.Join(dir, name)
	if _, err := os.Stat(dst); err != nil {
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			writeErr(w, http.StatusInternalServerError, "保存图片失败")
			return
		}
	}
	_ = h.store.AddAudit("admin", "upload.image", name,
		fmt.Sprintf("%d bytes %s", len(body), ctype))

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":   true,
		"url":  "/img/" + name,
		"size": len(body),
	})
}

// handleImage 处理 GET /img/<name>：把上传的配图发回去。
//
// 名字只允许「十六进制 + 已知后缀」这一种形状 ——
// 用 filepath.Base 再拼路径，避免 ../ 之类的穿越。
func (h *Hub) handleImage(w http.ResponseWriter, r *http.Request) {
	dir := h.imagesDir()
	if dir == "" {
		http.NotFound(w, r)
		return
	}
	name := filepath.Base(strings.TrimPrefix(r.URL.Path, "/img/"))
	if name == "" || name == "." || name == string(filepath.Separator) {
		http.NotFound(w, r)
		return
	}
	// 只接受 <32 位十六进制><后缀>
	stem, ext := name[:len(name)-len(filepath.Ext(name))], filepath.Ext(name)
	if len(stem) != 32 || !isHex(stem) {
		http.NotFound(w, r)
		return
	}
	ctype := ""
	for _, m := range imgMagic {
		if m.ext == ext {
			ctype = m.ctype
			break
		}
	}
	if ctype == "" {
		http.NotFound(w, r)
		return
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	// 内容寻址 → 名字变了内容才变，可以放心长缓存
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// handlePreview 处理 POST /admin/preview：把一段 Markdown 渲染成 HTML。
//
// ⚠️ **刻意走服务端渲染，不在浏览器里用 JS 库渲染**。
// 两个理由：
//  1. 用和前台**同一个** RenderMarkdown，预览即所得。
//     换成前端库的话，两边对 Markdown 的支持范围必然不同步
//     （表格、任务列表、自动链接…），预览会骗人。
//  2. 零 CDN 约定 —— 不为一个预览再引一个前端库。
//
// 顺便：RenderMarkdown 自带消毒（去掉 script/on* 之类），
// 所以这里返回的 HTML 是安全的，前端可以放心塞进 innerHTML。
func (h *Hub) handlePreview(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		drainBody(w, r)
		writeErr(w, http.StatusUnauthorized, "未登录")
		return
	}
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "只接受 POST")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 正文上限 1MB

	// ⚠️ **ParseForm 对 multipart/form-data 不解析 body** ——
	// 那样 FormValue("md") 永远是空串，预览渲染出一片空白，
	// 而且**不报错**（看起来像"Markdown 没渲染"，而不是"没收到内容"）。
	// multipart 必须显式 ParseMultipartForm。
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "multipart/form-data") {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			writeErr(w, http.StatusBadRequest, "内容太大或格式不对")
			return
		}
	} else {
		if err := r.ParseForm(); err != nil {
			writeErr(w, http.StatusBadRequest, "内容太大或格式不对")
			return
		}
	}
	md := r.FormValue("md")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":   true,
		"html": string(RenderMarkdown(md)),
	})
}
