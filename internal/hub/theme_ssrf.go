package hub

// 「一键获取主题」的网络出口防护（SSRF）。
//
// 背景：抓取主题本质上是一个「让 Hub 按管理员给的 URL 发 HTTP 请求」的能力。
// 哪怕它要管理员会话，一次 SSRF 也足以把一台公网小鸡变成内网跳板：
// 攻击者拿到管理员会话（或干脆就是管理员自己在钓鱼页上粘贴了地址）后，
// 可以让 Hub 去读 169.254.169.254 的云元数据、或者内网里那台没设密码的数据库。
//
// 只做「校验 URL 字符串」是不够的，因为 DNS 可以在校验之后改指向
//（DNS rebinding）。所以这里做两层：
//
//	第一层  safeThemeURL：解析 URL，拒绝明显的非 http(s)、拒绝字面量上的内网地址；
//	第二层  safeThemeDialer：真正建连时再校验一次 IP，且把结果钉死给这次连接，
//	                      避免「校验用 A 地址、连接用 B 地址」的时间差。
//
// ── 关于回环地址（127.0.0.1 / ::1）────────────────────────────────
//
// 早期版本一律放行回环，理由是「不少站架在自己机器或反代后面，抓自己站很正常」。
// 这个理由站不住：回环段不是「自己的站」，而是**这台机器上的所有服务**。
// 一旦拿到管理员会话，一个 SSRF 就能把 127.0.0.1:8799 的后台、本机Docker
// 映射出来的数据库、以及任何监听在本地端口上的管理面板全读一遍。
//
// 「抓自己站」这个正当需求，正确做法是让管理员显式声明域名，而不是把整个
// 回环段敞开。现在的策略是「默认拒绝 + 按主机名精确放行」：
//
//   - 默认连回环都拒绝；
//   - 目标主机名在白名单里时才放行。白名单来源有二：
//     ①启动参数 --domain（本站自己的域名，反代后走回环是常态）；
//     ② 管理员在后台填的 theme_fetch_hosts（逗号分隔）。
//
// 精确匹配，不做后缀匹配——「evil-vps.mjfuns.lat」不该因为「mjfuns.lat」
// 在列表里就能打本机回环。

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// settingThemeFetchHosts 是「允许抓回环」的主机名白名单（settings 键）。
//
// 存逗号分隔的域名，供管理员显式放开某些本机/反代后的名字。
const settingThemeFetchHosts = "theme_fetch_hosts"

// themeLoopbackPolicy 决定回环该怎么处理。
//
// 由 Hub 构造时注入：站点自己的域名 + 管理员在后台显式填的白名单。
type themeLoopbackPolicy struct {
	// hosts 是允许连回环的主机名（小写）。
	hosts map[string]bool
}

// allow 判断某个主机名是否可以走回环。
func (p themeLoopbackPolicy) allow(host string) bool {
	if len(p.hosts) == 0 {
		return false
	}
	return p.hosts[strings.ToLower(strings.TrimSpace(host))]
}

// parseThemeFetchHosts 把逗号分隔的域名列表解析成查表结构。
//
// 容错优先：管理员手滑粘了「https://a.com/x」这种整条 URL、
// 或者结尾多逗号，都不该让整个功能失效——解析不出主机名的项直接丢掉。
//
// 但校验必须真的做。早期版本只挡了含空格/斜杠的输入，结果 "http"
// 这种碎片能被收进白名单。错收一条等于把回环放开给一个意外的主机名，
// 所以这里用 validFetchHostname 按形态双重把关。
func parseThemeFetchHosts(raw string) map[string]bool {
	out := make(map[string]bool)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// 允许直接粘整条 URL：取其中的主机名部分。
		if strings.Contains(part, "//") {
			u, err := url.Parse(part)
			if err != nil {
				continue
			}
			part = u.Hostname()
		}
		// 允许带端口。IPv6 字面量要连端口一起剥掉。
		if h, _, err := net.SplitHostPort(part); err == nil {
			part = h
		}
		part = strings.ToLower(strings.Trim(part, "."))
		part = strings.Trim(part, "[]") // [::1] -> ::1
		if part == "" {
			continue
		}
		if !validFetchHostname(part) {
			continue
		}
		out[part] = true
	}
	return out
}

// validFetchHostname 判断一个字符串是否长得像主机名。
//
// 规则刻意偏严——宁可漏掉一条管理员手滑写错的项，也不能错收：
// 错收 = 把回环放开给一个意料之外的主机名。
//
//   - 字符集：字母数字、连字符、点；IPv6 额外允许冒号（可能是地址字面量）。
//   - 形态：至少含一个点，或者是 "localhost"，或者能被解析成 IP。
//
// 最后那条形态要求正是为了挡掉 "http"、"abc" 这类碎片——
// 它们没有点、不是 localhost、也不是 IP。
func validFetchHostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-' || c == '.' || c == ':':
		default:
			return false
		}
	}
	if net.ParseIP(s) != nil {
		return true // IP 字面量（IPv4 / IPv6）
	}
	if s == "localhost" {
		return true
	}
	// 域名必须至少有一个点，且不能以点或连字符开头结尾。
	if !strings.Contains(s, ".") {
		return false
	}
	if strings.HasPrefix(s, "-") || strings.HasPrefix(s, ".") ||
		strings.HasSuffix(s, "-") || strings.HasSuffix(s, ".") {
		return false
	}
	return true
}

// safeThemeURL 校验一个用于抓主题的 URL 是否可以发起请求。
//
// 拦掉的：
//
//	-非 http/https（file://、ftp://、gopher:// 等一律不接）
//	- 没有主机名
//	- 字面量 IP 落在私有段/链路本地/组播等（见 forbiddenThemeIP）
//
// **回环字面量不在这里拒绝**，而是交给拨号层：那里才知道主机名，
// 而「这个回环是否被显式放行」正是按主机名判断的。
// 同理这里也不做 DNS 解析——真正的把关在 safeThemeDialer，解析结果随时会变。
func safeThemeURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("地址不合法: %w", err)
	}
	switch u.Scheme {
	case "http", "https":
	case "":
		return nil, fmt.Errorf("请填写 http:// 或 https:// 开头的地址")
	default:
		return nil, fmt.Errorf("只支持 http/https，收到的是 %s://", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("地址里没有主机名")
	}
	// 字面量 IP 先查一遍。域名走不到这条分支，由拨号时再查。
	// 回环跳过：它由拨号层按主机名放行。
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() && forbiddenThemeIP(ip) {
		return nil, fmt.Errorf("%s 是内网/保留地址，不允许从这里抓主题", ip)
	}
	return u, nil
}

// forbiddenThemeIP 判断一个 IP 是否禁止作为抓取目标。
//
// 放行：公网可路由地址。
// 拒绝：回环、私有段、链路本地（含云元数据 169.254.169.254）、组播、
// 未指定、CGNAT 共享段（RFC 6598）、以及 IPv6 的本地地址。
//
// ⚠️ 这个函数**不看主机名**，所以回环一律算禁止。想放行回环必须在拨号层
// 判断，因为只有那里才知道主机名——同一个 127.0.0.1，既可能是本站反代，
// 也可能是本机那台没设密码的数据库。
func forbiddenThemeIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if ip.IsLoopback() {
		return true
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || !ip.IsGlobalUnicast() {
		return true
	}
	// CGNAT 共享地址段 100.64.0.0/10：Go 的 IsPrivate() 不覆盖它，
	// 但它在运营商网络里指向的是这一片用户的内网设备——同样不能打。
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
	}
	return false
}

// safeThemeDialer 返回一个只连「非禁止 IP」的拨号器。
//
// 防的是 DNS rebinding：safeThemeURL 校验时域名可能解析到公网 IP，
// 真正建连时又被改到 127.0.0.1。这里在 DialContext 里重新解析并检查，
// 检查通过后把 net.Dialer 的目标直接换成那个已验证的 IP，
// 于是「验证结果」和「实际连接对象」是同一个，不存在时间差。
//
// 回环的处理是这里的关键一环：forbiddenThemeIP 把回环判为禁止，
// 但如果**主机名**在白名单里，就允许连回环。这个判断必须放在拨号层，
// 因为它需要同时知道主机名和解析出来的 IP。
func safeThemeDialer(timeout time.Duration, policy themeLoopbackPolicy) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: timeout}
	allowLoop := policy.allow
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("地址不合法: %w", err)
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("解析 %s 失败: %w", host, err)
		}
		var lastErr error
		for _, ip := range ips {
			if forbiddenThemeIP(ip.IP) {
				// 回环只有主机名被显式放行时才允许；
				// 私有段/链路本地等即便放行也不打。
				if !(ip.IP.IsLoopback() && allowLoop(host)) {
					lastErr = fmt.Errorf("%s 解析到内网/保留地址 %s，已拒绝", host, ip.IP)
					continue
				}
			}
			// 用已验证的 IP 直连，绕开第二次解析。
			conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if derr == nil {
				return conn, nil
			}
			lastErr = derr
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("%s 没有可用地址", host)
		}
		return nil, lastErr
	}
}
