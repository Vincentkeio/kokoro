package panelupdate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type cacheTestTransport func(*http.Request) (*http.Response, error)

func (f cacheTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func resetReleaseCache() {
	releaseResponseCache.Lock()
	releaseResponseCache.entries = make(map[string]releaseCacheEntry)
	releaseResponseCache.limitedUntil = time.Time{}
	releaseResponseCache.limitedStatus = 0
	releaseResponseCache.Unlock()
}

func TestReleaseListCacheAvoidsPerVersionAndConcurrentRequests(t *testing.T) {
	resetReleaseCache()
	t.Cleanup(resetReleaseCache)
	var calls atomic.Int32
	original := http.DefaultTransport
	http.DefaultTransport = cacheTestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/releases") {
			return nil, fmt.Errorf("unexpected tag lookup: %s", r.URL)
		}
		body := `[{"tag_name":"v1.0.0","published_at":"2026-10-18T12:00:00Z"},{"tag_name":"v0.2.0-beta.99","published_at":"2026-10-19T12:00:00Z","prerelease":true}]`
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			release, err := Latest(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			if release.Tag != "v0.2.0-beta.99" {
				t.Errorf("publication order was lost: %s", release.Tag)
			}
		}()
	}
	workers.Wait()
	release, err := Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if newer, err := IsNewerRelease(context.Background(), release, "v1.0.0"); err != nil || !newer {
		t.Fatalf("cached publication comparison: %v, %v", newer, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected one combined release request, got %d", calls.Load())
	}
}

func TestRateLimitStopsFurtherRequestsUntilReset(t *testing.T) {
	resetReleaseCache()
	t.Cleanup(resetReleaseCache)
	var calls atomic.Int32
	original := http.DefaultTransport
	http.DefaultTransport = cacheTestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		header := make(http.Header)
		header.Set("X-RateLimit-Remaining", "0")
		header.Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10))
		return &http.Response{StatusCode: 403, Header: header, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = original })
	if _, err := Latest(context.Background()); err == nil || !strings.Contains(err.Error(), "请求次数已达上限") {
		t.Fatalf("missing useful limit error: %v", err)
	}
	if _, err := ByTag(context.Background(), "v1.0.0"); err == nil || isReleaseNotFound(err) {
		t.Fatalf("rate limit must not become a version fallback: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("kept requesting while rate-limited: %d", calls.Load())
	}
}
