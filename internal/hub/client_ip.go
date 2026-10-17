package hub

import (
	"net"
	"net/http"
	"strings"
)

// Cloudflare publishes its reverse-proxy networks at these endpoints.
// Last checked 2026-10-18. No external lookup is needed on a report path.
// https://www.cloudflare.com/ips-v4/
// https://www.cloudflare.com/ips-v6/
var cloudflareProxyNetworks = func() []*net.IPNet {
	var networks []*net.IPNet
	for _, cidr := range []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22",
		"103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
		"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22",
		"198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
		"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32",
		"2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
	} {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(err)
		}
		networks = append(networks, network)
	}
	return networks
}()

func isCloudflareProxy(ip net.IP) bool {
	for _, network := range cloudflareProxyNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

// Only read Cloudflare headers after the immediate upstream hop has been
// verified. In particular, an arbitrary client cannot opt into this path by
// adding CF-Connecting-IP or a fabricated left-hand X-Forwarded-For prefix.
func cloudflareClientIP(r *http.Request) net.IP {
	ip := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IP")))
	// Only consult the IPv6 companion for Cloudflare's Class-E pseudo IPv4,
	// rather than trusting a stray companion header on an ordinary request.
	if v4 := ip.To4(); v4 != nil && v4[0] >= 240 {
		if v6 := net.ParseIP(strings.TrimSpace(r.Header.Get("CF-Connecting-IPv6"))); v6 != nil && v6.To4() == nil {
			return v6
		}
	}
	return ip
}

func (h *Hub) clientIP(r *http.Request) string {
	peer := remoteIP(r.RemoteAddr)
	if peer == nil {
		return r.RemoteAddr
	}
	if !h.cfg.BehindProxy {
		return peer.String()
	}
	cloudflare := isCloudflareProxy(peer)
	localProxy := peer.IsLoopback() || peer.IsPrivate() || peer.IsLinkLocalUnicast()
	if !cloudflare && !localProxy {
		return peer.String()
	}
	if cloudflare {
		if ip := cloudflareClientIP(r); ip != nil {
			return ip.String()
		}
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		if len(parts) > 64 {
			return peer.String()
		}
		for i := len(parts) - 1; i >= 0; i-- {
			ip := net.ParseIP(strings.TrimSpace(parts[i]))
			if ip == nil {
				return peer.String() // Do not cross an unparseable trust boundary.
			}
			if !isCloudflareProxy(ip) {
				return ip.String()
			}
			peer = ip
			if original := cloudflareClientIP(r); original != nil {
				return original.String()
			}
		}
		return peer.String()
	}
	if localProxy {
		if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
			return ip.String()
		}
	}
	return peer.String()
}
