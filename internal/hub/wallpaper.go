package hub

// 站点壁纸。
//
// 和头像同一套路：上传的图落在数据目录（data/wallpaper-<主题ID>.<ext>），
// 由 /wallpaper/<主题ID> 路由吐出去；URL 形式则只把地址写进设置。
//
// ⚠️ **壁纸按主题分开存**：每个主题一套 kind / url / 本地文件（设置名带主题后缀，
// 文件名带主题 ID）。后台面板编辑的是"当前启用主题"的那一套；换个主题就是另一套，
// 换回来还是原来那张。这样访客切主题时各用各的背景，也不会把别的主题顶掉。
//
// ⚠️ **为什么不走主题的 tokens**：主题 tokens 的值有严格校验
// （禁 `;` `{` `}` `<` `>` `\` `@`，禁外链 `url()`），那是**沙箱边界** ——
// 主题是"别人写的、可能不可信"的东西，不该为了一个壁纸把闸门放开。
// 所以壁纸做成**站点设置**（只有管理员能动），在
// `<style id="kokoro-tokens">` **之后**再注入一段 `:root` 覆盖：
// 后定义的赢 —— 站长设了就处处生效，没设时主题自带的背景图照常显示。
//
// ⚠️ **填 URL 的代价**：访客浏览器会直接去请求那个外站，等于把访客 IP
// 交给第三方。后台 UI 里必须把这点讲清楚，别让人以为它和上传一样私密。

import (
	"errors"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	settingWallpaperKind = "wallpaper_kind" // 后面拼 ":<主题ID>"；值 "" | "local" | "url"
	settingWallpaperURL  = "wallpaper_url"  // 同上，值为外部图片地址
	settingWallpaperVer  = "wallpaper_ver"  // 同上，值为上传时间戳，用来绕浏览器缓存
)

// wallpaperSettingKey 把设置名按主题分开：换主题不互相覆盖。
func wallpaperSettingKey(base, themeID string) string {
	return base + ":" + themeID
}

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

// maxWallpaperBytes 限制每张本地上传壁纸的体积。
const maxWallpaperBytes = 500 << 10

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

// safeThemeFileID 把主题 ID 洗成能安全拼进文件名的片段。
//
// 主题 ID 本身经过主题系统校验，但设置里的值可能被手改过，而它会进文件路径，
// 所以这里再洗一遍：只留字母数字和 . - _，其余一律换成下划线。
func safeThemeFileID(themeID string) string {
	var b strings.Builder
	for _, r := range themeID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), ".")
	if out == "" || len(out) > 64 {
		return ""
	}
	return out
}

func (h *Hub) wallpaperKind(themeID string) string {
	v, err := h.store.GetSetting(wallpaperSettingKey(settingWallpaperKind, themeID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

// wallpaperSrcPath 是 <img>/CSS 里引用这张壁纸用的地址（带版本参数绕缓存）。
func (h *Hub) wallpaperSrcPath(themeID string) string {
	src := "/wallpaper/" + url.PathEscape(themeID)
	if ver, err := h.store.GetSetting(wallpaperSettingKey(settingWallpaperVer, themeID)); err == nil {
		if ver = strings.TrimSpace(ver); ver != "" {
			src += "?v=" + url.QueryEscape(ver)
		}
	}
	return src
}

// wallpaperCSS 生成要注入 <style> 的声明（不含选择器）。空串 = 这个主题没设壁纸。
//
// 顺手把 bg-size / bg-repeat / bg-attachment 一起写死成"整屏铺满"——
// 这三个变量在**主题 tokens 里写不出有效值**（校验按长度类型走，
// 不认 cover / no-repeat 这类关键字），但在站点壁纸这条路上我们是直接
// 输出 CSS、不受那层校验约束，正好把这个缺口补上。
func (h *Hub) wallpaperCSS(themeID string) string {
	const tail = `--kokoro-bg-size:cover;--kokoro-bg-repeat:no-repeat;--kokoro-bg-attachment:fixed;`
	switch h.wallpaperKind(themeID) {
	case "local":
		return `--kokoro-bg-image:url("` + h.wallpaperSrcPath(themeID) + `");` + tail
	case "url":
		u, err := h.store.GetSetting(wallpaperSettingKey(settingWallpaperURL, themeID))
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
	ThemeID   string // 这一套设置属于哪个主题（面板跟着当前启用主题走）
	ThemeName string // 面板上显示的"正在给哪个主题设壁纸"
	Kind      string // "" | local | url
	URL       string // 编辑框回显
	Preview   string // 有壁纸时的预览地址（带版本参数绕缓存）
	Err       string // 最近一次操作的错误提示
}

// wallpaperFor 组装后台面板的数据。errCode 来自 ?wperr=，映射成人话。
func (h *Hub) wallpaperFor(themeID, errCode string) wallpaperView {
	v := wallpaperView{ThemeID: themeID, Kind: h.wallpaperKind(themeID)}
	if h.themes != nil {
		if m := h.themes.Get(themeID); m != nil {
			v.ThemeName = m.Name
		}
	}
	if v.Kind == "url" {
		if raw, err := h.store.GetSetting(wallpaperSettingKey(settingWallpaperURL, themeID)); err == nil {
			v.URL = strings.TrimSpace(raw)
		}
		v.Preview = v.URL
	}
	if v.Kind == "local" {
		v.Preview = h.wallpaperSrcPath(themeID)
	}
	switch errCode {
	case "url":
		v.Err = "地址要填 http:// 或 https:// 开头的，且不能带空格、引号、括号 —— 它会被写进样式表里的 url()。"
	case "type":
		v.Err = "只支持 png / jpg / webp / gif。svg 不能用：它是唯一能在图片位置执行脚本的格式。"
	case "big":
		v.Err = "图太大了，上限 500KB。"
	case "file":
		v.Err = "没选到文件。"
	case "theme":
		v.Err = "找不到要设置壁纸的主题，请刷新后台页重试。"
	case "form":
		v.Err = "表单没解析成功，请重试。"
	case "save":
		v.Err = "写入失败，看看数据目录的权限。"
	}
	return v
}

// handleWallpaper 把某个主题上传的壁纸吐回去。
//
// 内容寻址那套（/img/<hash>）不适用：壁纸是**单文件覆盖式**的，
// 文件名固定为 wallpaper-<主题ID>，靠 ?v=<时间戳> 绕浏览器缓存。
//
// 兼容旧地址：/wallpaper（不带主题）按当前站点主题处理 ——
// 访客浏览器里可能还缓存着改版前的页面。
func (h *Hub) handleWallpaper(w http.ResponseWriter, r *http.Request) {
	if h.cfg.DataDir == "" {
		http.NotFound(w, r)
		return
	}
	themeID := strings.TrimPrefix(r.URL.Path, "/wallpaper/")
	if themeID == "/wallpaper" || themeID == "" {
		themeID = h.ActiveThemeID()
	}
	if themeID == "" || strings.Contains(themeID, "/") || h.wallpaperKind(themeID) != "local" {
		http.NotFound(w, r)
		return
	}
	id := safeThemeFileID(themeID)
	if id == "" {
		http.NotFound(w, r)
		return
	}
	for _, p := range wallpaperExts {
		b, err := os.ReadFile(filepath.Join(h.cfg.DataDir, "wallpaper-"+id+p.ext))
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
//
// 表单必须带上要改的主题（当前启用主题）；只接受**已注册**的主题 ID ——
// 它会被拼进设置名和文件名，不能让任意值进来。
func (h *Hub) handleAdminWallpaper(w http.ResponseWriter, r *http.Request) {
	if !h.adminAuthed(r) {
		drainBody(w, r)
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "只接受 POST", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWallpaperBytes+(64<<10))
	// ⚠️ 这个端点有两种编码：本地上传是 multipart，填地址/清除是
	// application/x-www-form-urlencoded。以前无条件 ParseMultipartForm，
	// urlencoded 请求会拿到 ErrNotMultipart，却被当成"图太大" ——
	// 于是"填地址"永远存不进去，还弹一条牛头不对马嘴的提示。
	// 反过来也不能一律 ParseForm：那样 multipart 的 body 根本不会被读。
	// 按 Content-Type 分开走。
	switch {
	case strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/form-data"):
		if err := r.ParseMultipartForm(maxWallpaperBytes + (64 << 10)); err != nil {
			http.Redirect(w, r, "/admin?wperr=big#themes", http.StatusSeeOther)
			return
		}
	default:
		if err := r.ParseForm(); err != nil {
			http.Redirect(w, r, "/admin?wperr=form#themes", http.StatusSeeOther)
			return
		}
	}

	themeID := strings.TrimSpace(r.FormValue("theme"))
	if themeID == "" || h.themes == nil || h.themes.Get(themeID) == nil {
		http.Redirect(w, r, "/admin?wperr=theme#themes", http.StatusSeeOther)
		return
	}

	switch r.FormValue("action") {
	case "clear":
		// 停用：清掉这个主题的壁纸设置与文件，页面回落到主题自带的背景图。
		_ = h.store.SetSetting(wallpaperSettingKey(settingWallpaperKind, themeID), "")
		_ = h.store.SetSetting(wallpaperSettingKey(settingWallpaperURL, themeID), "")
		_ = h.removeWallpaper(themeID)

	case "url":
		u := strings.TrimSpace(r.FormValue("wallpaper_url"))
		if !safeWallpaperURL(u) {
			http.Redirect(w, r, "/admin?wperr=url#themes", http.StatusSeeOther)
			return
		}
		_ = h.store.SetSetting(wallpaperSettingKey(settingWallpaperKind, themeID), "url")
		_ = h.store.SetSetting(wallpaperSettingKey(settingWallpaperURL, themeID), u)
		// 切到 URL 就把本地那份删掉：留着两份只会让"现在到底用哪个"变得含糊。
		_ = h.removeWallpaper(themeID)

	case "upload":
		file, hdr, err := r.FormFile("wallpaper")
		if err != nil || hdr == nil || hdr.Size == 0 {
			http.Redirect(w, r, "/admin?wperr=file#themes", http.StatusSeeOther)
			return
		}
		_ = file.Close()
		if hdr.Size > maxWallpaperBytes {
			http.Redirect(w, r, "/admin?wperr=big#themes", http.StatusSeeOther)
			return
		}
		ext, ok := imageExt(hdr.Filename)
		if !ok {
			http.Redirect(w, r, "/admin?wperr=type#themes", http.StatusSeeOther)
			return
		}
		if err := h.saveWallpaper(hdr, ext, themeID); err != nil {
			log.Printf("[hub] 保存壁纸失败 theme=%s: %v", themeID, err)
			http.Redirect(w, r, "/admin?wperr=save#themes", http.StatusSeeOther)
			return
		}
		_ = h.store.SetSetting(wallpaperSettingKey(settingWallpaperKind, themeID), "local")
		_ = h.store.SetSetting(wallpaperSettingKey(settingWallpaperURL, themeID), "")
		// 版本参数绕浏览器缓存：壁纸是覆盖式的、文件名不变，
		// 不换 URL 的话访客会一直看到上一张。
		_ = h.store.SetSetting(wallpaperSettingKey(settingWallpaperVer, themeID),
			strconv.FormatInt(time.Now().UnixMilli(), 10))
	}
	http.Redirect(w, r, "/admin#themes", http.StatusSeeOther)
}

// saveWallpaper 把上传的壁纸写进数据目录，旧文件先删掉。
func (h *Hub) saveWallpaper(fh *multipart.FileHeader, ext, themeID string) error {
	if h.cfg.DataDir == "" {
		return errors.New("未配置数据目录")
	}
	id := safeThemeFileID(themeID)
	if id == "" {
		return errors.New("主题 ID 不能作为文件名")
	}
	f, err := fh.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	if err := os.MkdirAll(h.cfg.DataDir, 0o755); err != nil {
		return err
	}
	out, err := os.CreateTemp(h.cfg.DataDir, ".wallpaper-upload-*")
	if err != nil {
		return err
	}
	tmp := out.Name()
	defer os.Remove(tmp)
	if _, err := copyUploadBounded(out, f, maxWallpaperBytes); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := h.removeWallpaper(themeID); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(h.cfg.DataDir, "wallpaper-"+id+ext))
}

func (h *Hub) removeWallpaper(themeID string) error {
	if h.cfg.DataDir == "" {
		return nil
	}
	id := safeThemeFileID(themeID)
	if id == "" {
		return nil
	}
	var firstErr error
	for _, p := range wallpaperExts {
		if err := os.Remove(filepath.Join(h.cfg.DataDir, "wallpaper-"+id+p.ext)); err != nil &&
			!os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
