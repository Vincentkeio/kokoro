package hub

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"time"
)

// siteIconAssets are fixed public paths third-party software probes for a
// site's logo. Bookmark managers and dashboards often skip HTML discovery and
// request /favicon.ico, /logo.png or a manifest directly.
var siteIconAssets = map[string]struct {
	file        string
	contentType string
}{
	"/favicon.ico":           {"favicon.ico", "image/x-icon"},
	"/favicon.png":           {"favicon.png", "image/png"},
	"/favicon.svg":           {"favicon.svg", "image/svg+xml"},
	"/apple-touch-icon.png":  {"apple-touch-icon.png", "image/png"},
	"/logo.webp":             {"logo.webp", "image/webp"},
	"/logo.png":              {"icon-512.png", "image/png"},
	"/icon-192.png":          {"icon-192.png", "image/png"},
	"/icon-512.png":          {"icon-512.png", "image/png"},
	"/icon-maskable-512.png": {"icon-maskable-512.png", "image/png"},
	"/manifest.webmanifest":  {"manifest.webmanifest", "application/manifest+json"},
	"/site.webmanifest":      {"manifest.webmanifest", "application/manifest+json"},
}

func (h *Hub) handleSiteIcon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "只接受 GET 或 HEAD", http.StatusMethodNotAllowed)
		return
	}
	asset, ok := siteIconAssets[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/" + asset.file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", asset.contentType)
	// Root URLs are stable, unlike versioned static assets: revalidate after
	// an hour so a future logo change is not cached permanently by clients.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sha256.Sum256(data)))
	http.ServeContent(w, r, asset.file, time.Time{}, bytes.NewReader(data))
}
