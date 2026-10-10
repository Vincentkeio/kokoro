package hub

// 站点壁纸。
//
// 和头像同一套路：上传的图落在数据目录（data/wallpaper.<ext>），
// 由 /wallpaper 路由吐出去；URL 形式则只把地址写进设置。
//
// ⚠️ **为什么不走主题的 tokens**：主题 tokens 的值有严格校验
// （禁 `;` `{` `}` `<` `>` `\` `@`，禁外链 `url()`），那是**沙箱边界** ——
// 主题是"别人写的、可能不可信"的东西，不该为了一个壁纸把闸门放开。
// 所以壁纸做成**站点级设置**（只有管理员能动），在
// `<style id="kokoro-tokens">` **之后**再注入一段 `:root` 覆盖：
// 后定义的赢，站点设置压过主题默认。
//
// ⚠️ **填 URL 的代价**：访客浏览器会直接去请求那个外站，等于把访客 IP
// 交给第三方。后台 UI 里必须把这点讲清楚，别让人以为它和上传一样私密。

import (
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	settingWallpaperKind = "wallpaper_kind" // "" | "local" | "url"
	settingWallpaperURL  = "wallpaper_url"
)

// wallpaperExts 是允许的位图格式。
//
// **刻意不含 svg**：它是唯一能在"图片"位置执行脚本的格式
// （头像那条路也是这么处理的）。
var wallpaperExts = []struct{ ext, ctype string }{
	{".png", "image/png"},
	{".jpg", "image/jpeg"},
	{".webp", "image/webp"},
	{".gif", "image/gif"},
}

// maxWallpaperBytes 比头像宽松得多 —— 壁纸是整屏铺的，2MB 常常不够。
const maxWallpaperBytes = 6 << 20

// safeWallpaperURL 校验管理员填的壁纸地址。
//
// 只放行 http(s)，并且**拒掉任何能跳出 `url()` 的字符**（引号、圆括号、
// 分号、反斜杠、空白）。这段 CSS 是直接拼进 <style> 的，
// 一个引号就能把声明拆开、往页面里塞任意规则 —— 而它只该是张图。
//
// 空白一律拒绝（含空格）：未编码的空格在 URL 里本来就不合法，
// 让人填 %20 比放行一个含糊的东西更不容易出事。
func safeWallpaperURL(raw string) bool {
	u := strings.TrimSpace(raw)
	if u == "" || len(u) > 500 {
		return false
	}
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return false
	}
	return !strings.ContainsAny(u, "\"'()\\;{}<>\n\r\t ")
}

func (h *Hub) wallpaperKind() string {
	v, err := h.store.GetSetting(settingWallpaperKind)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// wallpaperCSS 生成要注入 <style> 的声明（不含选择器）。空串 = 没壁纸。
//
// 顺手把 bg-size / bg-repeat / bg-attachment 一起写死成"整屏铺满"——
// 这三个变量在**主题 tokens 里写不出有效值**（校验按长度类型走，
// 不认 cover / no-repeat 这类关键字），但在站点壁纸这条路上我们是直接
// 输出 CSS、不受那层校验约束，正好把这个缺口补上。
func (h *Hub) wallpaperCSS() string {
	const tail = `--kokoro-bg-size:cover;--kokoro-bg-repeat:no-repeat;--kokoro-bg-attachment:fixed;`
	switch h.wallpaperKind() {
	case "local":
		return `--kokoro-bg-image:url("/wallpaper");` + tail
	case "url":
		u, err := h.store.GetSetting(settingWallpaperURL)
		u = strings.TrimSpace(u)
		if err != nil || !safeWallpaperURL(u) {
			return ""
		}
		return `--kokoro-bg-image:url("` + u + `");` + tail
	}
	return ""
}

// wallpaperView 是后台面板要用的那点状态。
type wallpaperView struct {
	Kind    string // "" | local | url
	URL     string // 编辑框回显
	Preview string // 有壁纸时的预览地址（带版本参数绕缓存）
	Err     string // 最近一次操作的错误提示
}

// wallpaperFor 组装后台面板的数据。errCode 来自 ?wperr=，映射成人话。
func (h *Hub) wallpaperFor(errCode string) wallpaperView {
	v := wallpaperView{Kind: h.wallpaperKind()}
	if v.Kind == "url" {
		if raw, err := h.store.GetSetting(settingWallpaperURL); err == nil {
			v.URL = strings.TrimSpace(raw)
		}
		v.Preview = v.URL
	}
	if v.Kind == "local" {
		if ver, err := h.store.GetSetting("wallpaper_ver"); err == nil && ver != "" {
			v.Preview = "/wallpaper?v=" + ver
		} else {
			v.Preview = "/wallpaper"
		}
	}
	switch errCode {
	case "url":
		v.Err = "地址要填 http:// 或 https:// 开头的，且不能带空格、引号、括号 —— 它会被写进样式表里的 url()。"
	case "type":
		v.Err = "只支持 png / jpg / webp / gif。svg 不能用：它是唯一能在图片位置执行脚本的格式。"
	case "big":
		v.Err = "图太大了，上限 6 MB。"
	case "file":
		v.Err = "没选到文件。"
	case "save":
		v.Err = "写入失败，看看数据目录的权限。"
	}
	return v
}

// handleWallpaper 把上传的壁纸吐回去。
//
// 内容寻址那套（/img/<hash>）不适用：壁纸是**单文件覆盖式**的，
// 名字固定，靠 ?v=<时间戳> 绕浏览器缓存，和头像完全一致。
func (h *Hub) handleWallpaper(w http.ResponseWriter, r *http.Request) {
	if h.cfg.DataDir == "" || h.wallpaperKind() != "local" {
		http.NotFound(w, r)
		return
	}
	for _, p := range wallpaperExts {
		b, err := os.ReadFile(filepath.Join(h.cfg.DataDir, "wallpaper"+p.ext))
		if err != nil {
			continue
		}
		w.Header().Set("Content-Type", p.ctype)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(b)
		return
	}
	http.NotFound(w, r)
}

// handleAdminWallpaper 处理后台的壁纸表单。
//
// 一个端点三种动作（upload / url / clear）：它们都在同一个面板上，
// 拆成三个路由只会让表单和路由两处都得对齐，多一处能写错的地方。
func (h *Hub) handleAdminWallpaper(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		drainBody(w, r)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if err := r.ParseMultipartForm(maxWallpaperBytes + (1 << 20)); err != nil {
		http.Redirect(w, r, "/admin?wperr=big#themes", http.StatusSeeOther)
		return
	}

	switch r.FormValue("action") {
	case "clear":
		_ = h.store.SetSetting(settingWallpaperKind, "")
		_ = h.store.SetSetting(settingWallpaperURL, "")
		_ = h.removeWallpaper()

	case "url":
		u := strings.TrimSpace(r.FormValue("wallpaper_url"))
		if !safeWallpaperURL(u) {
			http.Redirect(w, r, "/admin?wperr=url#themes", http.StatusSeeOther)
			return
		}
		_ = h.store.SetSetting(settingWallpaperKind, "url")
		_ = h.store.SetSetting(settingWallpaperURL, u)
		// 切到 URL 就把本地那份删掉：留着两份只会让"现在到底用哪个"变得含糊。
		_ = h.removeWallpaper()

	case "upload":
		_, hdr, err := r.FormFile("wallpaper")
		if err != nil || hdr == nil || hdr.Size == 0 {
			http.Redirect(w, r, "/admin?wperr=file#themes", http.StatusSeeOther)
			return
		}
		if hdr.Size > maxWallpaperBytes {
			http.Redirect(w, r, "/admin?wperr=big#themes", http.StatusSeeOther)
			return
		}
		ext, ok := imageExt(hdr.Filename)
		if !ok {
			http.Redirect(w, r, "/admin?wperr=type#themes", http.StatusSeeOther)
			return
		}
		if err := h.saveWallpaper(hdr, ext); err != nil {
			log.Printf("[hub] 保存壁纸失败: %v", err)
			http.Redirect(w, r, "/admin?wperr=save#themes", http.StatusSeeOther)
			return
		}
		_ = h.store.SetSetting(settingWallpaperKind, "local")
		_ = h.store.SetSetting(settingWallpaperURL, "")
		// 版本参数绕浏览器缓存：壁纸是覆盖式的、文件名不变，
		// 不换 URL 的话访客会一直看到上一张。
		_ = h.store.SetSetting("wallpaper_ver",
			strconv.FormatInt(time.Now().UnixMilli(), 10))
	}
	http.Redirect(w, r, "/admin#themes", http.StatusSeeOther)
}

// saveWallpaper 把上传的壁纸写进数据目录，旧文件先删掉。
func (h *Hub) saveWallpaper(fh *multipart.FileHeader, ext string) error {
	if h.cfg.DataDir == "" {
		return errors.New("未配置数据目录")
	}
	f, err := fh.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	if err := os.MkdirAll(h.cfg.DataDir, 0o755); err != nil {
		return err
	}
	_ = h.removeWallpaper()
	out, err := os.Create(filepath.Join(h.cfg.DataDir, "wallpaper"+ext))
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, f); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func (h *Hub) removeWallpaper() error {
	if h.cfg.DataDir == "" {
		return nil
	}
	var firstErr error
	for _, p := range wallpaperExts {
		if err := os.Remove(filepath.Join(h.cfg.DataDir, "wallpaper"+p.ext)); err != nil &&
			!os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
