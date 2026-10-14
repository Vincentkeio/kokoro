package hub

import (
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// securityHeaders adds browser protections to every Hub response and rejects
// cross-origin state-changing requests authenticated by the admin cookie.
func (h *Hub) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Content-Security-Policy", "base-uri 'self'; object-src 'none'; frame-ancestors 'none'; form-action 'self'")
		header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if h.cfg.BehindProxy || r.TLS != nil || h.cfg.TLSMode == "auto" {
			header.Set("Strict-Transport-Security", "max-age=31536000")
		}

		if isUnsafeMethod(r.Method) && isAdminMutationPath(r.URL.Path) {
			_, cookieErr := r.Cookie(adminCookie)
			// Browser login submissions need protection before a session exists.
			browserLogin := r.URL.Path == "/admin" && (r.Header.Get("Origin") != "" || r.Header.Get("Referer") != "" || r.Header.Get("Sec-Fetch-Site") == "cross-site")
			if (cookieErr == nil || browserLogin) && !sameOriginRequest(r,
				h.cfg.BehindProxy || h.cfg.TLSMode == "proxy" || h.cfg.TLSMode == "auto") {
				drainBody(w, r)
				http.Error(w, "跨站请求已拒绝", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isUnsafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

func isAdminMutationPath(path string) bool {
	return path == "/admin" || strings.HasPrefix(path, "/admin/") ||
		strings.HasPrefix(path, "/theme/")
}

// sameOriginRequest requires a browser-supplied Origin or Referer whose host
// matches the request host. Missing and opaque origins are rejected for
// authenticated mutations; ordinary GET navigation remains unaffected.
func sameOriginRequest(r *http.Request, proxyHTTPS bool) bool {
	requestScheme := "http"
	if r.TLS != nil || proxyHTTPS {
		requestScheme = "https"
	} else if r.URL.Scheme == "https" {
		requestScheme = "https"
	}
	if requestHost := canonicalHost(r.Host, requestScheme); requestHost == "" {
		return false
	} else if origin := strings.TrimSpace(r.Header.Get("Origin")); origin != "" {
		if origin == "null" {
			return false
		}
		u, err := url.Parse(origin)
		return err == nil && u.User == nil && u.Path == "" && u.RawQuery == "" &&
			u.Fragment == "" && u.Scheme == requestScheme &&
			canonicalHost(u.Host, u.Scheme) == requestHost
	} else if referer := strings.TrimSpace(r.Header.Get("Referer")); referer != "" {
		u, err := url.Parse(referer)
		return err == nil && u.User == nil && u.Scheme == requestScheme &&
			canonicalHost(u.Host, u.Scheme) == requestHost
	}
	return false
}

func canonicalHost(hostport, scheme string) string {
	u, err := url.Parse("//" + hostport)
	if err != nil || u.User != nil || u.Path != "" {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return host + ":" + port
}

// safeAuthority accepts only an IP or DNS authority with an optional numeric
// port. The install-script endpoint embeds this value in a shell variable.
func safeAuthority(authority string) bool {
	if strings.HasPrefix(authority, "[") && strings.HasSuffix(authority, "]") &&
		net.ParseIP(strings.TrimSuffix(strings.TrimPrefix(authority, "["), "]")) != nil {
		return true
	}
	host := authority
	if name, port, err := net.SplitHostPort(authority); err == nil {
		if port == "" {
			return false
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return false
		}
		host = name
	} else if strings.Contains(authority, ":") {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	if host == "" || len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') &&
				!(c >= '0' && c <= '9') && c != '-' {
				return false
			}
		}
	}
	return true
}
