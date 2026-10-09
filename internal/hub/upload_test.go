package hub

// 配图上传：按内容判类型、按哈希命名、不许穿越。

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/store"
)

// tinyPNG 造一张真的 1x1 PNG（校验用得到 image.DecodeConfig，
// 所以必须是真图，不能塞几个魔数字节糊弄）。
func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
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
