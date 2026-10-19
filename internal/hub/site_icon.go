package hub

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"time"
)

// handleSiteIcon exposes fixed embedded icons at conventional public paths.
func (h *Hub) handleSiteIcon(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "只接受 GET 或 HEAD", http.StatusMethodNotAllowed)
		return
	}
	var file, contentType string
	switch r.URL.Path {
	case "/favicon.ico":
		file, contentType = "favicon.ico", "image/x-icon"
	case "/favicon.png":
		file, contentType = "favicon.png", "image/png"
	case "/favicon.svg":
		file, contentType = "favicon.svg", "image/svg+xml"
	case "/apple-touch-icon.png":
		file, contentType = "apple-touch-icon.png", "image/png"
	case "/logo.webp":
		file, contentType = "logo.webp", "image/webp"
	default:
		http.NotFound(w, r)
		return
	}
	data, err := staticFS.ReadFile("static/" + file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	// Root URLs are stable, unlike versioned static assets: revalidate after
	// an hour so a future logo change is not cached permanently by clients.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	w.Header().Set("ETag", fmt.Sprintf(`"%x"`, sha256.Sum256(data)))
	http.ServeContent(w, r, file, time.Time{}, bytes.NewReader(data))
}
