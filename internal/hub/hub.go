// Package hub 是 Kokoro 的主控端：HTTP 服务 + 页面渲染 + 实时推送。
//
// 设计原则：
//  1. 只用标准库 + 已经在 store 里落地的纯 Go SQLite，避免体积膨胀；
//  2. 前端资源用 go:embed 打进二进制，部署时只有一个文件；
//  3. Hub 可以跑在 Nginx 后面（--tls proxy），也可以自己起 TLS。
package hub

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Vincentkeio/kokoro/internal/alert"
	"github.com/Vincentkeio/kokoro/internal/flags"
	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/store"
	"github.com/Vincentkeio/kokoro/internal/theme"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// Hub 是一个运行中的主控实例。
type Hub struct {
	cfg   *model.HubConfig
	store *store.Store
	mux   *http.ServeMux
	tmpl  *template.Template
	srv   *http.Server

	broker  *broker
	limiter *limiter
	alerts  *alert.Manager

	// themes 是主题注册表（内置 + 导入），theme 缓存当前主题的渲染产物。
	// 后台切主题只改内存 + settings 表，不需要重启。
	themes *theme.Registry
	theme  themeCache
	// themeAssets 缓存各主题包里的资源字节，供 /_theme-assets/ 路由直接吐出。
	themeAssets *themeAssetCache

	// done 在开始关闭时被 close，用来通知 SSE 之类的长连接主动退出。
	//
	// 为什么需要它：http.Server.Shutdown **不会取消请求的 context**，
	// 它只是停止接受新连接、然后等已有 handler 返回。
	// 而 SSE 的循环是 `for { select { case <-ctx.Done(): ... } }`，
	// 那个 ctx 只在**客户端断开**时才会 Done。
	// 于是 Shutdown 每次都必然等到自己的 5 秒 deadline，
	// 让一次完全正常的重启在 systemd 里记成 `status=1/FAILURE`，
	// 并且每次重启都白等 5 秒。
	done     chan struct{}
	doneOnce sync.Once
}

// Alerts 暴露告警引擎，供外部（比如测试）取用。
func (h *Hub) Alerts() *alert.Manager { return h.alerts }

// limiter 是最朴素的限流器：记住每个 key 上一次动作的时间，间隔不够就拒绝。
// 只用来防手滑连点和低级刷屏，不做精确配额。
type limiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newLimiter() *limiter { return &limiter{last: make(map[string]time.Time)} }

func (l *limiter) allow(key string, gap time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if t, ok := l.last[key]; ok && now.Sub(t) < gap {
		return false
	}
	l.last[key] = now
	if len(l.last) > 4096 {
		for k, t := range l.last {
			if now.Sub(t) > 10*time.Minute {
				delete(l.last, k)
			}
		}
	}
	return true
}

// New 构造 Hub。不会监听端口，调用 Run 才真正启动。
func New(cfg *model.HubConfig, st *store.Store) (*Hub, error) {
	if cfg == nil {
		return nil, errors.New("hub: 配置为空")
	}
	if st == nil {
		return nil, errors.New("hub: store 为空")
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:8799"
	}
	if cfg.SiteName == "" {
		cfg.SiteName = "Kokoro"
	}

	tmpl, err := template.New("").Funcs(template.FuncMap{
		"fmtBytes":   FmtBytes,
		"fmtRate":    FmtRate,
		"fmtPercent": FmtPercent,
		"fmtDur":     FmtDuration,
		"pct":        Pct,
		// 续费费用：库里存 JSON，显示和编辑都要拆/拼
		"priceText":     priceText,
		"priceAmount":   priceAmount,
		"priceCur":      priceCur,
		"pricePer":      pricePer,
		"priceCursList": priceCursList,
		"pricePersList": pricePersList,
		"safeURL":       SafeURL,
		"md":            RenderMarkdown,
		"ts":            FmtTime,
		"tsShort":       FmtTimeShort,
		"metricName":    MetricName,
		"flag":          flags.Inline,
		"flagName":      flags.Name,
		"latClass":      LatClass,
		"fmtMS":         FmtMS,
		"fmtTime":       FmtTime,
		"hasPrefix":     hasPrefix,
		// joinTags 供后台把 []string 回填进输入框。
		"joinTags": func(ts []string) string { return strings.Join(ts, ", ") },
		"specCPU":  specCPU,
		"specMem":  specMem,
		"specDisk": specDisk,
		// sparkDelay / sparkDelayTail 给 sparkline 上的粒子动画算相位偏移。
		// 用节点 ID 算一个 0~3s 内的起点，让十几张卡片上的粒子**不齐**地跑，
		// 否则同步扫过会显得假。SMIL 的 begin 单位是秒。
		// FNV-1a 哈希 + 简单 mod。不追求均匀——只要不同即可。
		"sparkDelay": func(id string) string {
			h := fnvNew32a(id)
			return fmt.Sprintf("%dms", int(h%3200))
		},
		"sparkDelayTail": func(id string) string {
			h := fnvNew32a(id)
			v := (h%3200 + 400) % 3200
			return fmt.Sprintf("%dms", int(v))
		},
	}).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("hub: 解析模板失败: %w", err)
	}

	h := &Hub{
		cfg:         cfg,
		store:       st,
		mux:         http.NewServeMux(),
		tmpl:        tmpl,
		broker:      newBroker(),
		limiter:     newLimiter(),
		themeAssets: newThemeAssetCache(),
		// 在这里就建好，保证 handleStream 拿到的一定不是 nil
		// （select 里对 nil channel 会永久阻塞，那就等于没接上关闭信号）。
		done: make(chan struct{}),
	}
	hubURL := ""
	if cfg.Domain != "" {
		hubURL = "https://" + cfg.Domain
	}
	h.alerts = alert.New(st, hubURL)
	h.loadTheme()
	h.routes()
	h.srv = &http.Server{
		Addr:         cfg.Listen,
		Handler:      h.mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 0, // SSE 需要长连接，不设写超时
		IdleTimeout:  120 * time.Second,
	}
	return h, nil
}

// routes 注册全部路由。
func (h *Hub) routes() {
	// 静态资源（带版本号，交给 Nginx 缓存）
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Printf("[hub] 挂载静态资源失败: %v", err)
	} else {
		h.mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	}

	// agent 侧 API
	h.mux.HandleFunc("/api/v1/register", h.handleRegister)
	h.mux.HandleFunc("/api/v1/report", h.handleReport)
	h.mux.HandleFunc("/api/v1/command/result", h.handleCommandResult)
	h.mux.HandleFunc("/api/v1/netq", h.handleNetQ)
	h.mux.HandleFunc("/api/v1/dl/", h.handleDownload)

	// 实时推送
	h.mux.HandleFunc("/api/v1/stream", h.handleStream)

	// 页面
	h.mux.HandleFunc("/", h.handleHome)
	h.mux.HandleFunc("/search", h.handleSearch)
	h.mux.HandleFunc("/n/", h.handleNodePage)
	h.mux.HandleFunc("/i/", h.handleInstallScript)
	h.mux.HandleFunc("/avatar", h.handleAvatar)
	// 站点壁纸：上传的那份落在数据目录，不在 embed 里，所以也要单独开路由。
	h.mux.HandleFunc("/wallpaper", h.handleWallpaper)
	// 文章配图：/img/<哈希>。<name> 只允许「32 位十六进制 + 已知后缀」，
	// 见 handleImage 里的形状校验 —— 不接受用户给的文件名。
	h.mux.HandleFunc("/img/", h.handleImage)
	// 「我想买它」：公开接口（访客未登录）。内部自己限流。
	h.mux.HandleFunc("/api/v1/buy", h.handleBuy)

	// 主题：导出与切换。三个读取端点都公开（主题就是给人抄的）：
	// /theme.json 给清单，/theme-bundle/<id> 给完整包，/theme-export/<id> 给下载。
	// 切换走 adminOnlyPage 在内部再判一次，两处都留是为了路由表读起来完整。
	h.mux.HandleFunc("/theme.json", h.handleThemeManifest)
	h.mux.HandleFunc("/theme-export/", h.handleThemeExport)
	h.mux.HandleFunc("/theme-bundle/", h.handleThemeBundle)
	// 给 AI 写主题用的提示词，后台「主题」页有下载入口。
	// 公开：规范不是秘密，公开才好分享给别人。
	h.mux.HandleFunc("/theme-ai-prompt.md", h.handleThemeAIPrompt)
	h.mux.HandleFunc("/theme/", h.handleThemeSwitch)
	// 访客自选主题：无鉴权，只写 cookie，不影响站点设置。
	h.mux.HandleFunc("/pick/", h.handleThemePick)
	// 主题包里的资源（背景图、字体）。只吐白名单类型，SVG 一律不给。
	h.mux.HandleFunc("/_theme-assets/", h.handleThemeAssets)

	// 后台（M0 只做最小可用：节点列表与安装令牌）
	h.mux.HandleFunc("/admin", h.handleAdmin)
	h.mux.HandleFunc("/admin/nodes", h.handleAdminNodes)
	h.mux.HandleFunc("/admin/tokens", h.handleAdminTokens)
	h.mux.HandleFunc("/admin/comments", h.handleAdminComments)
	h.mux.HandleFunc("/admin/settings", h.handleAdminSettings)
	h.mux.HandleFunc("/admin/profile", h.handleAdminProfile)
	h.mux.HandleFunc("/admin/account", h.handleAdminAccount)
	h.mux.HandleFunc("/admin/logout", h.handleAdminLogout)
	// 主题面板已经并进 /admin 的「主题」页签。老地址 /admin/themes
	// 重定向过去 —— 收藏过的书签、或者别人分享的链接都不该变成 404。
	h.mux.HandleFunc("/admin/themes", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin#themes", http.StatusSeeOther)
	})
	// 壁纸设置（上传 / 填 URL / 清除）。它挂在主题页上，
	// 因为壁纸就是"站点外观"的一部分，另开一页反而找不着。
	h.mux.HandleFunc("/admin/wallpaper", h.handleAdminWallpaper)
	h.mux.HandleFunc("/admin/post", h.adminOnlyPage(h.handleAdminPost))
	h.mux.HandleFunc("/admin/tasks", h.adminOnlyPage(h.handleAdminTasks))
	// 文章配图上传。返回 JSON（不是整页跳转）—— 前端在编辑器里异步传，
	// 整页刷新会把还没保存的正文冲掉。
	h.adminOnly("/admin/upload", h.handleUpload)
	// 编辑器的 Markdown 预览。走服务端渲染，保证预览和前台完全一致。
	h.adminOnly("/admin/preview", h.handlePreview)
	h.adminOnly("/admin/themes/import", h.handleThemeImport)
	h.adminOnly("/admin/themes/grab", h.handleThemeGrab)
	h.adminOnly("/admin/themes/delete", h.handleThemeDelete)

	// 告警与通知：底层 handler 不做鉴权，这里统一套一层管理员判断
	if h.alerts != nil {
		h.adminOnly("/admin/alerts", h.alerts.HandleRules)
		h.adminOnly("/admin/alerts/test", h.alerts.HandleTest)
		h.adminOnly("/admin/notify", h.alerts.HandleNotifyConfig)
	}

	h.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, "ok")
	})
}

// denyUnauthed 拒绝一个未通过管理员校验的请求。
//
// 为什么不在 handler 里直接 http.Error 就完事：那样写的话，
// **带请求体的 POST 会收到 RST 而不是可读的 401**。
//
// 原因在 TCP 层：客户端已经把 body 发出来了，服务端却没读它，
// 就直接写响应并关闭连接。内核 socket 缓冲区里那半截未读数据会触发
// RST，把已经写好的响应报文一起丢掉——客户端只看到
// "远程主机强迫关闭了一个现有的连接"，拿不到状态码也拿不到正文。
// 空body 的请求不会这样，所以只有 POST 会中招。
//
// 先把 body 抽干（限量，不必读干净）再写响应，RST 就不会发生。
// 这条对所有「未登录 401」的分支都适用，不只是主题那几处。
func denyUnauthed(w http.ResponseWriter, r *http.Request) {
	drainBody(w, r)
	http.Error(w, "未登录", http.StatusUnauthorized)
}

// drainBody 读掉请求体里最多 limit 字节，让连接可以正常收尾。
//
// 限量而非全读：恶意客户端可以挂一个 1GB 的 body 上来，
// 为了回一句"未登录"而把它全吃下来是自找麻烦。
// 剩下的部分交给 http.Server 在handler 返回后的连接清理里处理。
func drainBody(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return
	}
	const limit = 64 << 10
	_, _ = io.CopyN(io.Discard, r.Body, limit)
}

// adminOnly 把一个不自带鉴权的 handler 包上管理员校验。
func (h *Hub) adminOnly(path string, fn http.HandlerFunc) {
	h.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if !h.adminAuthed(r) {
			denyUnauthed(w, r)
			return
		}
		fn(w, r)
	})
}

// adminOnlyPage 是给「GET 需要登录、POST 自己再判」的页面路由用的包装。
// 页面 handler 里通常还会根据请求方法分派，所以这里不吞掉未登录的跳转逻辑。
func (h *Hub) adminOnlyPage(fn func(http.ResponseWriter, *http.Request, bool)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fn(w, r, h.adminAuthed(r))
	}
}

// Run 启动服务并阻塞，直到 ctx 取消。
func (h *Hub) Run(ctx context.Context) error {
	// 首次启动时备好管理员口令与安装令牌
	h.ensureBootstrap()
	// 后台：定期向浏览器广播快照
	go h.startBroadcastLoop(ctx)
	// 后台：把长时间没上报的节点标记为离线
	h.startStatusWatch(ctx)
	// 后台：告警引擎（含 Telegram / Webhook 通知）
	if h.alerts != nil {
		go h.alerts.Start(ctx)
	}

	ln, err := net.Listen("tcp", h.cfg.Listen)
	if err != nil {
		return fmt.Errorf("hub: 监听 %s 失败: %w", h.cfg.Listen, err)
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("[hub] 开始服务 %s", h.cfg.Listen)
		if err := h.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Printf("[hub] 正在关闭")
		// 先唤醒长连接（SSE），让它们的 handler 尽快返回。
		// 顺序很重要：必须在 Shutdown 之前 close，否则 Shutdown
		// 会一直等到 deadline，把正常重启变成 status=1/FAILURE。
		h.signalDone()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.srv.Shutdown(shutdownCtx); err != nil {
			// 兜底：仍有个别连接赖着不走（客户端半开等），强制收尾。
			// 这里**不**把错误上抛——重启是正常运维动作，
			// 不该因为一个卡住的浏览器连接就让 systemd 记一次 FAILURE。
			log.Printf("[hub] 优雅关闭超时，强制收尾: %v", err)
			_ = h.srv.Close()
		}
		return nil
	}
}

// signalDone 通知所有监听 done 的 goroutine 退出（可重复调用）。
func (h *Hub) signalDone() {
	if h.done == nil {
		return
	}
	h.doneOnce.Do(func() { close(h.done) })
}

// SiteName 返回站点名，供模板使用。
func (h *Hub) SiteName() string { return h.cfg.SiteName }

// ServeHTTP 让 Hub 本身成为 http.Handler。
//
// 主要给测试用：httptest.NewServer(h) 就能起一个真实的 Hub，不用真的监听端口。
// 生产路径走 Run 里的 h.srv，不经过这里。
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mux.ServeHTTP(w, r)
}

// FNV-1a 哈希小工具——sparkDelay 用，节点 ID 算 32 位。
// 没装进 hash/fnv 标准库是因为只调一次。
func fnvNew32a(s string) uint32 {
	var h uint32 = 2166136261
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}
