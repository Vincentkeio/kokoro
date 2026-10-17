package agent

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestIPConnectivityUsesRequestedFamilyAndFallback(t *testing.T) {
	var called []string
	ok := probeIPFamily(context.Background(), "tcp6", []string{"[::1]:1", "[::1]:2"}, func(ctx context.Context, network, target string) (net.Conn, error) {
		if network != "tcp6" {
			t.Fatal(network)
		}
		called = append(called, target)
		if len(called) == 1 {
			return nil, errors.New("blocked")
		}
		a, b := net.Pipe()
		b.Close()
		return a, nil
	})
	if !ok || len(called) != 2 {
		t.Fatal(ok, called)
	}
}

func TestIPConnectivityCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	ok := probeIPFamily(ctx, "tcp4", []string{"first", "second"}, func(context.Context, string, string) (net.Conn, error) { calls++; return nil, ctx.Err() })
	if ok || calls != 1 {
		t.Fatal(ok, calls)
	}
}
