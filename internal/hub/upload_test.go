package hub

// 配图上传：按内容判类型、按哈希命名、不许穿越。

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/store"
)

// tinyPNG 是一张**硬编码**的 1×1 红色 PNG（69 字节）。
//
// ⚠️ **故意不用 image/png 现编码**。
//
// 原因：image.DecodeConfig 只认识「被 import 注册过」的解码器。
// 如果这里 import 了 image/png 来造图，Go 会把同包文件链在一起 ——
// **测试文件就成了那个"注册解码器的人"**，于是生产代码漏了
// `_ "image/png"` 也照样绿。这个 bug 真的发生过：
// 测试全绿、线上每次上传都报"图片损坏或格式不被支持"。
//
// 硬编码字节之后，解码器只可能来自生产代码，测试才测的是真东西。
const tinyPNGB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(tinyPNGB64)
	if err != nil {
		t.Fatalf("内置 PNG 解不开（测试自身坏了）: %v", err)
	}
	return b
}

// uploadReq 拼一个带文件的 multipart 请求。
func uploadReq(t *testing.T, h *Hub, st *store.Store, field, filename string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(loginCookie(t, h, st)) // 不带会话会 401，断言就测不到真正的逻辑
	return r
}

func TestUploadAcceptsRealPNG(t *testing.T) {
	h, st := newTestHub(t)
	w := httptest.NewRecorder()
	h.handleUpload(w, uploadReq(t, h, st, "file", "我的截图.png", tinyPNG(t)))
	if w.Code != http.StatusOK {
		t.Fatalf("上传真 PNG 应成功，实际 %d: %s", w.Code, w.Body.String())
	}
	var d struct {
		OK  bool   `json:"ok"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if !d.OK || !strings.HasPrefix(d.URL, "/img/") {
		t.Fatalf("返回不对: %+v", d)
	}
	// 名字必须是「32 位十六进制 + 后缀」—— 用户给的「我的截图」不该出现在里面
	name := strings.TrimPrefix(d.URL, "/img/")
	if len(name) != 32+len(".png") || !isHex(name[:32]) {
		t.Errorf("文件名应是内容哈希，实际 %q", name)
	}
}

// TestUploadRejectsFakeImage 后缀写着 .png、内容不是图片 → 必须拒绝。
//
// 只信后缀的话，存下来就是一个能被浏览器当页面渲染的**同源文件** ——
// 等于给自己开了个存储型 XSS 的入口。
func TestUploadRejectsFakeImage(t *testing.T) {
	h, st := newTestHub(t)
	w := httptest.NewRecorder()
	h.handleUpload(w, uploadReq(t, h, st, "file", "x.png",
		[]byte("<html><script>alert(1)</script></html>")))
	if w.Code == http.StatusOK {
		t.Fatal("内容不是图片却上传成功了 —— 只信了后缀")
	}
}

// TestUploadRejectsSVG SVG 是 XML、能带 <script>，同源下发等于后门。
func TestUploadRejectsSVG(t *testing.T) {
	h, st := newTestHub(t)
	w := httptest.NewRecorder()
	h.handleUpload(w, uploadReq(t, h, st, "file", "x.svg",
		[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)))
	if w.Code == http.StatusOK {
		t.Fatal("SVG 不该被接受")
	}
}

// TestUploadRejectsBrokenPNG 前几字节像 PNG、整体不是合法图片，也要拒。
//
// 不拒的话站长以为传成功了，浏览器加载时才报错。
func TestUploadRejectsBrokenPNG(t *testing.T) {
	h, st := newTestHub(t)
	fake := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A},
		[]byte("这不是真的PNG数据")...)
	w := httptest.NewRecorder()
	h.handleUpload(w, uploadReq(t, h, st, "file", "x.png", fake))
	if w.Code == http.StatusOK {
		t.Fatal("只有魔数、解不出图的内容不该被接受")
	}
}

// TestImagePathTraversalBlocked 取图接口不许被穿越。
func TestImagePathTraversalBlocked(t *testing.T) {
	h, _ := newTestHub(t)
	for _, p := range []string{
		"/img/../../etc/passwd",
		"/img/..%2f..%2fetc%2fpasswd",
		"/img/avatar.png",                          // 形状不对（不是 32 位哈希）
		"/img/" + strings.Repeat("a", 32) + ".sh",  // 后缀不在白名单
		"/img/" + strings.Repeat("a", 31) + ".png", // 哈希长度不对
	} {
		w := httptest.NewRecorder()
		h.handleImage(w, httptest.NewRequest(http.MethodGet, p, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s 应 404，实际 %d", p, w.Code)
		}
	}
}

// TestUploadSameImageSameName 内容寻址：同一张图重复传不会占两份。
func TestUploadSameImageSameName(t *testing.T) {
	h, st := newTestHub(t)
	data := tinyPNG(t)
	urlOf := func() string {
		w := httptest.NewRecorder()
		h.handleUpload(w, uploadReq(t, h, st, "file", "a.png", data))
		var d struct{ URL string }
		_ = json.Unmarshal(w.Body.Bytes(), &d)
		return d.URL
	}
	if u1, u2 := urlOf(), urlOf(); u1 != u2 {
		t.Errorf("同一张图应得到同一个 URL，实际 %q vs %q", u1, u2)
	}
}

// TestPostPageHasMarkdownToolbar 写文章那页要有工具栏，且常见功能都在。
//
// 漏一个按钮不会报错，只是"用的时候发现没有" —— 所以要钉住。
func TestPostPageHasMarkdownToolbar(t *testing.T) {
	h, st := newTestHub(t)
	n := mkNode(t, st, "东京", "JP", "日本", "")
	sess := loginCookie(t, h, st)

	r := httptest.NewRequest(http.MethodGet, "/admin/post?node="+n.ID, nil)
	r.AddCookie(sess)
	w := httptest.NewRecorder()
	h.handleAdminPost(w, r, true)
	page := w.Body.String()

	if !strings.Contains(page, `class="mdbar"`) {
		t.Fatal("写文章那页没有 Markdown 工具栏")
	}
	// 常用的几个都要在：加粗 / 斜体 / 链接 / 图片 / 列表 / 引用 / 代码
	for _, want := range []string{
		`data-md="**|**"`, `data-md="*|*"`,
		"data-md-link", "data-md-img",
		`data-md="- |"`, `data-md="&gt; |"`, "`|`",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("工具栏缺少 %s", want)
		}
	}
	// 粘贴用的 file input 要在（paste 事件靠它兜底不了，得有真 input 走按钮那条路）
	if !strings.Contains(page, `id="md-file"`) {
		t.Error("缺 md-file —— 点「上传图片」按钮会没有反应")
	}
}

// TestPreviewRendersMarkdown 预览要把 Markdown 渲染成 HTML。
func TestPreviewRendersMarkdown(t *testing.T) {
	h, st := newTestHub(t)
	sess := loginCookie(t, h, st)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("md", "# 标题\n\n这是**粗体**。")
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/admin/preview", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(sess)
	w := httptest.NewRecorder()
	h.handlePreview(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("预览应成功，实际 %d: %s", w.Code, w.Body.String())
	}
	var d struct {
		OK   bool   `json:"ok"`
		HTML string `json:"html"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if !d.OK {
		t.Fatal("返回 ok=false")
	}
	// 标题和粗体都要真的渲染出来。
	//
	// 注意是 `<h3` 不是 `<h1`：正文标题从 **h3** 起，把 h1/h2 留给页面
	// （页面标题是 h1、卡片标题是 h2），免得文章里的标题跟页面结构抢层级。
	// 这个约定是有意的，不是渲染坏了。
	for _, want := range []string{"<h3", "<strong"} {
		if !strings.Contains(d.HTML, want) {
			t.Errorf("预览里缺少 %s（实际: %s）", want, d.HTML[:min(len(d.HTML), 120)])
		}
	}
}

// TestPreviewMatchesFrontend 预览必须和前台**同一个渲染器**。
//
// 这一点很重要：换成前端库渲染的话，两边对 Markdown 的支持范围
// 迟早不同步，预览就会骗人。所以这里直接对比两者的输出。
func TestPreviewMatchesFrontend(t *testing.T) {
	h, st := newTestHub(t)
	sess := loginCookie(t, h, st)

	md := "# T\n\n- a\n- b\n\n`code`"
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("md", md)
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/admin/preview", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(sess)
	w := httptest.NewRecorder()
	h.handlePreview(w, r)

	var d struct{ HTML string }
	_ = json.Unmarshal(w.Body.Bytes(), &d)
	if d.HTML != string(RenderMarkdown(md)) {
		t.Error("预览的输出和 RenderMarkdown 不一致 —— 预览会骗人")
	}
}

// TestPreviewNeedsAuth 预览接口必须登录。
func TestPreviewNeedsAuth(t *testing.T) {
	h, _ := newTestHub(t)
	r := httptest.NewRequest(http.MethodPost, "/admin/preview", strings.NewReader("md=x"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.handlePreview(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("未登录却能预览 —— 等于把渲染接口开放给所有人")
	}
}
