package hub

// 主题资源路由：GET /_theme-assets/<themeID>/<name>
//
// 为什么需要它：主题的 tokens 允许写 url(/_theme-assets/...)（见 theme 包的
// validateTokenValue），用来挂背景图与自定义字体。但包里的资源字节存在
// 数据库的 themes.bundle 列里，浏览器无从访问——没有这条路由，那些 url()
// 全是 404，主题看起来就是"背景图没生效"，而且失败得毫无提示。
//
// 安全边界（这是本文件存在的第二个理由）：
//   - 只吐 assets/ 下的资源，路径经 theme.SafeAssetPath 归一化，挡掉 zip-slip；
//   - 扩展名走白名单，**明确禁止 .svg**：SVG 是唯一能在"图片"位置执行脚本的
//     格式（<svg onload=...>、内嵌 <script>），而主题包来自第三方，不能赌它善意；
//   - 一律带 nosniff 与严格 CSP，杜绝浏览器"猜"出别的类型来渲染。
//
// 缓存：解包一次的开销不该按请求付。主题包通常只有几十 KB 资源，
// 但一个背景图会被每个访客请求一遍，所以资源表常驻内存，随安装/删除更新。

import (
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	"github.com/Vincentkeio/kokoro/internal/theme"
)

// themeAssetMIME 是允许直接下发给浏览器的资源类型。
//
// 用白名单而不是黑名单：黑名单永远漏（.svgz、.xht、.html…），
// 而主题资源本来就只有「图片」和「字体」两类，穷举得起。
var themeAssetMIME = map[string]string{
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".webp":  "image/webp",
	".gif":   "image/gif",
	".avif":  "image/avif",
	".bmp":   "image/bmp",
	".ico":   "image/x-icon",
	".woff2": "font/woff2",
	".woff":  "font/woff",
	".ttf":   "font/ttf",
	".otf":   "font/otf",
}

// themeAssetCache 缓存「主题 ID → 条目名 → 字节」。
type themeAssetCache struct {
	mu sync.RWMutex
	m  map[string]map[string][]byte
}

func newThemeAssetCache() *themeAssetCache {
	return &themeAssetCache{m: make(map[string]map[string][]byte)}
}

// set 用一份新解出的资源表替换某个主题的缓存。空资源表等于删除。
func (c *themeAssetCache) set(id string, assets []theme.Asset) {
	if c == nil {
		return
	}
	if len(assets) == 0 {
		c.drop(id)
		return
	}
	m := make(map[string][]byte, len(assets))
	for _, a := range assets {
		m[a.Name] = a.Data
	}
	c.mu.Lock()
	c.m[id] = m
	c.mu.Unlock()
}

// drop 丢掉某个主题的资源缓存（删除主题时调用）。
func (c *themeAssetCache) drop(id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.m, id)
	c.mu.Unlock()
}

// get 取一条资源。name 是完整条目名（含 assets/ 前缀）。
func (c *themeAssetCache) get(id, name string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	am, ok := c.m[id]
	if !ok {
		return nil, false
	}
	b, ok := am[name]
	return b, ok
}

// rememberThemeAssets 从一份原始主题包里解出资源并缓存。
//
// 解包失败只记日志不报错：资源缺失的后果是某张背景图 404，
// 而主题本身已经装好了——不该因为资源而把整个安装判失败。
func (h *Hub) rememberThemeAssets(id string, raw []byte) {
	if len(raw) == 0 || h.themeAssets == nil {
		return
	}
	b, err := theme.ExtractPackage(raw)
	if err != nil {
		return
	}
	h.themeAssets.set(id, b.Assets)
}

// handleThemeAssets 处理 GET /_theme-assets/<themeID>/<name>。
func (h *Hub) handleThemeAssets(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/_theme-assets/")
	id, name, ok := strings.Cut(rest, "/")
	if !ok || !theme.ValidID(id) {
		http.NotFound(w, r)
		return
	}
	full, err := theme.SafeAssetPath(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	mime, allowed := themeAssetMIME[strings.ToLower(path.Ext(full))]
	if !allowed {
		// 未列入白名单的类型（含 .svg）一律当作不存在。
		// 给 404 而不是 415：不必告诉对方"这个格式被特别拦了"。
		http.NotFound(w, r)
		return
	}
	data, ok := h.themeAssets.get(id, full)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// 即使类型已白名单，再加一层沙箱：即便将来白名单被放宽，
	// 资源也不可能以文档身份执行脚本或发起同源请求。
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}
