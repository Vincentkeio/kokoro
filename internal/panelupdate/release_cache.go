package panelupdate

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type releaseCacheEntry struct {
	body    []byte
	expires time.Time
}

var releaseRequestGate = make(chan struct{}, 1)
var releaseResponseCache = struct {
	sync.Mutex
	entries       map[string]releaseCacheEntry
	limitedUntil  time.Time
	limitedStatus int
}{entries: make(map[string]releaseCacheEntry)}

// Cached metadata never stores binaries, credentials or tokens. Bound the cache
// so looking up many historical tags cannot grow memory without limit.
func cacheReleaseResponse(endpoint string, body []byte) {
	ttl := 5 * time.Minute
	if strings.HasPrefix(endpoint, "releases/tags/") {
		ttl = 24 * time.Hour
	}
	releaseResponseCache.Lock()
	defer releaseResponseCache.Unlock()
	if _, exists := releaseResponseCache.entries[endpoint]; !exists && len(releaseResponseCache.entries) >= 256 {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range releaseResponseCache.entries {
			if oldestKey == "" || entry.expires.Before(oldest) {
				oldestKey, oldest = key, entry.expires
			}
		}
		delete(releaseResponseCache.entries, oldestKey)
	}
	releaseResponseCache.entries[endpoint] = releaseCacheEntry{body: body, expires: time.Now().Add(ttl)}
}

func cachedReleaseResponse(endpoint string) ([]byte, error, bool) {
	releaseResponseCache.Lock()
	defer releaseResponseCache.Unlock()
	now := time.Now()
	if entry, ok := releaseResponseCache.entries[endpoint]; ok {
		if now.Before(entry.expires) {
			return entry.body, nil, true
		}
		delete(releaseResponseCache.entries, endpoint)
	}
	if now.Before(releaseResponseCache.limitedUntil) {
		return nil, releaseHTTPError{status: releaseResponseCache.limitedStatus, retryAt: releaseResponseCache.limitedUntil}, true
	}
	return nil, nil, false
}

func rememberReleaseHTTPError(resp *http.Response) error {
	err := releaseHTTPError{status: resp.StatusCode}
	if resp.StatusCode != http.StatusTooManyRequests && !(resp.StatusCode == http.StatusForbidden && (resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "")) {
		return err
	}
	now := time.Now()
	retryAt := now.Add(time.Minute)
	if resp.Header.Get("X-RateLimit-Remaining") == "0" {
		retryAt = now.Add(time.Hour)
		if reset, parseErr := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64); parseErr == nil && time.Unix(reset, 0).After(now) {
			retryAt = time.Unix(reset, 0).Add(time.Second)
		}
	}
	if value := resp.Header.Get("Retry-After"); value != "" {
		if seconds, parseErr := strconv.ParseInt(value, 10, 32); parseErr == nil && seconds > 0 {
			if at := now.Add(time.Duration(seconds) * time.Second); at.After(retryAt) {
				retryAt = at
			}
		} else if at, parseErr := http.ParseTime(value); parseErr == nil && at.After(retryAt) {
			retryAt = at
		}
	}
	err.retryAt = retryAt
	releaseResponseCache.Lock()
	releaseResponseCache.limitedUntil = retryAt
	releaseResponseCache.limitedStatus = resp.StatusCode
	releaseResponseCache.Unlock()
	return err
}
