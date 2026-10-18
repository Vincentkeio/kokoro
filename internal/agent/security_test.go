package agent

import (
	"bytes"
	"context"
	"fmt"
	"github.com/Vincentkeio/kokoro/internal/model"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHubRedirectDoesNotForwardRegistrationBody(t *testing.T) {
	var received atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Add(1); w.WriteHeader(200) }))
	defer attacker.Close()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/same" {
			http.Redirect(w, r, "/ok", http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == "/ok" {
			w.WriteHeader(200)
			return
		}
		http.Redirect(w, r, attacker.URL, http.StatusTemporaryRedirect)
	}))
	defer hub.Close()
	a, err := New(&model.AgentConfig{Hub: hub.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodPost, hub.URL+"/register", bytes.NewBufferString(`{"install_token":"it_secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer nt_secret")
	if response, err := a.client.Do(req); err == nil {
		response.Body.Close()
		t.Fatal("cross-origin redirect was followed")
	}
	if received.Load() != 0 {
		t.Fatal("registration body reached another origin")
	}
	response, err := a.client.Post(hub.URL+"/same", "application/json", bytes.NewBufferString("{}"))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("same-origin redirect rejected: %d", response.StatusCode)
	}
}

func TestAgentRejectsAmbiguousHubURLs(t *testing.T) {
	for _, hub := range []string{"ftp://example.com", "https://user:pass@example.com", "https://example.com?x=1", "https://example.com#x"} {
		if _, err := New(&model.AgentConfig{Hub: hub}, nil); err == nil {
			t.Errorf("accepted unsafe Hub URL %q", hub)
		}
	}
}

func TestHubRedirectAllowsEquivalentDefaultPortAndRejectsDowngrade(t *testing.T) {
	a, err := New(&model.AgentConfig{Hub: "https://example.com:443"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		url     string
		allowed bool
	}{
		{"https://EXAMPLE.com/ok", true},
		{"https://example.com:443/ok", true},
		{"http://example.com/ok", false},
		{"https://example.com:8443/ok", false},
		{"https://sub.example.com/ok", false},
	} {
		req, err := http.NewRequest(http.MethodGet, tc.url, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := a.client.CheckRedirect(req, nil) == nil; got != tc.allowed {
			t.Errorf("redirect %s allowed=%v", tc.url, got)
		}
	}
}

func TestSwapReservesDiskSpace(t *testing.T) {
	needed := uint64(3072) * 1024 * 1024
	if validateSwapDiskSpace(3072, needed+swapDiskReserveBytes) != nil {
		t.Fatal("valid capacity rejected")
	}
	for _, free := range []uint64{0, swapDiskReserveBytes - 1, needed + swapDiskReserveBytes - 1} {
		if validateSwapDiskSpace(3072, free) == nil {
			t.Errorf("disk-filling allocation accepted: %d", free)
		}
	}
}

func TestProbeCatalogRejectsSharedAndPrivateNetworks(t *testing.T) {
	for _, host := range []string{"100.100.100.200", "100.64.0.1", "10.0.0.1", "127.0.0.1", "169.254.169.254", "0.1.2.3", "198.18.0.1", "::1"} {
		if isPublicProbeIPv4(net.ParseIP(host)) {
			t.Errorf("non-public probe target accepted: %s", host)
		}
	}
	for _, host := range []string{"1.1.1.1", "8.8.8.8"} {
		if !isPublicProbeIPv4(net.ParseIP(host)) {
			t.Errorf("public probe target rejected: %s", host)
		}
	}
}

func TestUnknownCancellationsAreBoundedAndActiveTaskStillCancels(t *testing.T) {
	a, err := New(&model.AgentConfig{Hub: "https://example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		a.cancelTask(fmt.Sprintf("unknown-%d", i))
	}
	if len(a.cancelledTasks) > cmdQueueSize {
		t.Fatal("unbounded cancellation state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.activeTaskCancels["active"] = cancel
	a.cancelTask("active")
	if ctx.Err() != context.Canceled {
		t.Fatal("active task cancellation blocked by unknown IDs")
	}
}

func TestSwapFstabPreservesLegacyAndExternalSources(t *testing.T) {
	legacy := "/var/lib/kokoro/swapfile none swap sw 0 0 " + swapFstabMark
	external := "/swap.img none swap sw 0 0"
	current := "/swapfile none swap sw 0 0 " + swapFstabMark
	content := managedSwapFstabContent("/swapfile", legacy+"\n"+external+"\n"+current+"\n")
	if !strings.Contains(content, legacy) || !strings.Contains(content, external) {
		t.Fatalf("fstab 里别人的交换源被改动了：%q", content)
	}
	// 逐行比："/var/lib/kokoro/swapfile none swap" 里也含 "/swapfile none swap" 这段子串。
	currentLines := 0
	for _, line := range strings.Split(content, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "/swapfile" {
			currentLines++
		}
	}
	if currentLines != 1 {
		t.Fatalf("当前交换文件在 fstab 里出现 %d 次，应为 1：%q", currentLines, content)
	}
}
