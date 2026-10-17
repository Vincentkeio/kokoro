package agent

import (
	"context"
	"net"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// Independent TCP checks avoid DNS and never block metric collection.
func probeIPFamily(ctx context.Context, network string, targets []string, dial func(context.Context, string, string) (net.Conn, error)) bool {
	for _, target := range targets {
		conn, err := dial(ctx, network, target)
		if err == nil {
			conn.Close()
			return true
		}
		if ctx.Err() != nil {
			return false
		}
	}
	return false
}

func (a *Agent) connectivityLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		result := &model.IPConnectivity{}
		done := make(chan struct{}, 2)
		for _, family := range []string{"tcp4", "tcp6"} {
			go func(network string) {
				probeCtx, cancel := context.WithTimeout(ctx, 7*time.Second)
				defer cancel()
				targets := []string{"1.1.1.1:443", "8.8.8.8:443"}
				if network == "tcp6" {
					targets = []string{"[2606:4700:4700::1111]:443", "[2001:4860:4860::8888]:443"}
				}
				dialer := &net.Dialer{Timeout: 3 * time.Second}
				ok := probeIPFamily(probeCtx, network, targets, dialer.DialContext)
				if network == "tcp4" {
					result.IPv4 = ok
				} else {
					result.IPv6 = ok
				}
				done <- struct{}{}
			}(family)
		}
		<-done
		<-done
		if ctx.Err() != nil {
			return
		}
		result.CheckedAt = time.Now().UnixMilli()
		a.mu.Lock()
		a.connectivity = result
		a.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
