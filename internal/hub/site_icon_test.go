package hub

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicSiteIconDiscovery(t *testing.T) {
	h, _ := newTestHub(t)
	for _, item := range []struct{ path, mime string }{
		{"/favicon.ico", "image/x-icon"},
		{"/favicon.png", "image/png"},
		{"/favicon.svg", "image/svg+xml"},
		{"/apple-touch-icon.png", "image/png"},
		{"/logo.webp", "image/webp"},
		{"/logo.png", "image/png"},
		{"/icon-192.png", "image/png"},
		{"/icon-512.png", "image/png"},
		{"/icon-maskable-512.png", "image/png"},
		{"/manifest.webmanifest", "application/manifest+json"},
		{"/site.webmanifest", "application/manifest+json"},
	} {
		t.Run(item.path, func(t *testing.T) {
			get := httptest.NewRecorder()
			h.ServeHTTP(get, httptest.NewRequest(http.MethodGet, item.path, nil))
			if get.Code != http.StatusOK || get.Header().Get("Content-Type") != item.mime || get.Body.Len() == 0 {
				t.Fatalf("public icon not fetchable: status=%d mime=%q bytes=%d", get.Code, get.Header().Get("Content-Type"), get.Body.Len())
			}
			if get.Body.Len() > 8<<10 {
				t.Fatalf("icon unexpectedly large: %d bytes", get.Body.Len())
			}
			head := httptest.NewRecorder()
			h.ServeHTTP(head, httptest.NewRequest(http.MethodHead, item.path, nil))
			if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != get.Header().Get("Content-Length") {
				t.Fatalf("HEAD cannot discover icon metadata: status=%d headers=%v", head.Code, head.Header())
			}
			req := httptest.NewRequest(http.MethodGet, item.path, nil)
			req.Header.Set("If-None-Match", get.Header().Get("ETag"))
			cached := httptest.NewRecorder()
			h.ServeHTTP(cached, req)
			if cached.Code != http.StatusNotModified {
				t.Fatalf("icon revalidation status=%d", cached.Code)
			}
		})
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/", nil))
	for _, html := range []string{
		`href="/favicon.ico?v=70"`,
		`href="/favicon.png?v=70"`,
		`href="/icon-192.png?v=70"`,
		`href="/icon-512.png?v=70"`,
		`href="/favicon.svg?v=70"`,
		`href="/apple-touch-icon.png?v=70"`,
		`href="/manifest.webmanifest?v=70"`,
		`property="og:image" content="https://example.com/icon-512.png?v=70"`,
		`name="twitter:image" content="https://example.com/icon-512.png?v=70"`,
	} {
		if !strings.Contains(page.Body.String(), html) {
			t.Errorf("HTML icon discovery missing %s", html)
		}
	}
}

func TestWebManifestDeclaresSizedIcons(t *testing.T) {
	h, _ := newTestHub(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/manifest.webmanifest", nil))
	var m struct {
		Name  string `json:"name"`
		Icons []struct {
			Src     string `json:"src"`
			Sizes   string `json:"sizes"`
			Type    string `json:"type"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	if m.Name == "" || len(m.Icons) != 3 {
		t.Fatalf("unexpected manifest: %+v", m)
	}
	got := map[string]string{}
	for _, icon := range m.Icons {
		got[icon.Src] = icon.Sizes
	}
	if got["/icon-192.png"] != "192x192" || got["/icon-512.png"] != "512x512" || got["/icon-maskable-512.png"] != "512x512" {
		t.Fatalf("manifest icons missing sizes: %+v", m.Icons)
	}
	for _, icon := range m.Icons {
		if icon.Type != "image/png" {
			t.Errorf("manifest icon %s has type %q", icon.Src, icon.Type)
		}
	}
}

func TestFaviconContainsSmallPNGFrames(t *testing.T) {
	data, err := staticFS.ReadFile("static/favicon.ico")
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 6 || binary.LittleEndian.Uint16(data[:2]) != 0 || binary.LittleEndian.Uint16(data[2:4]) != 1 {
		t.Fatal("invalid ICO header")
	}
	sizes := []int{16, 32, 48, 64}
	if int(binary.LittleEndian.Uint16(data[4:6])) != len(sizes) || len(data) < 6+16*len(sizes) {
		t.Fatal("missing ICO sizes")
	}
	for i, size := range sizes {
		entry := data[6+16*i : 6+16*(i+1)]
		length, offset := int(binary.LittleEndian.Uint32(entry[8:12])), int(binary.LittleEndian.Uint32(entry[12:16]))
		if int(entry[0]) != size || int(entry[1]) != size || offset < 6+16*len(sizes) || length < 24 || offset > len(data)-length {
			t.Fatalf("invalid %dpx ICO entry", size)
		}
		frame := data[offset : offset+length]
		if !bytes.HasPrefix(frame, []byte("\x89PNG\r\n\x1a\n")) || int(binary.BigEndian.Uint32(frame[16:20])) != size || int(binary.BigEndian.Uint32(frame[20:24])) != size {
			t.Fatalf("invalid %dpx PNG frame", size)
		}
	}
}
