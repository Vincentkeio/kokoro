package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

func TestValidSwapSizeMB(t *testing.T) {
	for _, tc := range []struct {
		size int
		want bool
	}{{0, false}, {127, false}, {128, true}, {maxSwapSizeMB, true}, {maxSwapSizeMB + 1, false}, {-1, false}} {
		if got := validSwapSizeMB(tc.size); got != tc.want {
			t.Errorf("validSwapSizeMB(%d) = %v, want %v", tc.size, got, tc.want)
		}
	}
}

func TestManagedSwapFileSizeForTotal(t *testing.T) {
	const header = "Filename\tType\tSize\tUsed\tPriority\n"
	for _, tc := range []struct {
		name    string
		target  int
		swaps   string
		want    int
		wantErr bool
	}{
		{name: "no existing swap", target: 3072, swaps: header, want: 3072},
		{name: "preserve other swap", target: 3072, swaps: header + "/swap.img file 1048576 0 -2\n", want: 2048},
		{name: "ignore managed swap in total", target: 3072, swaps: header + "/swap.img file 1048576 0 -2\n" + managedSwapFile + " file 2097152 0 -3\n", want: 2048},
		{name: "external swap already meets target", target: 1024, swaps: header + "/swap.img file 1048576 0 -2\n", wantErr: true},
		{name: "external swap exceeds target", target: 1024, swaps: header + "/swap.img file 2097152 0 -2\n", wantErr: true},
		{name: "target below current total including managed swap", target: 3072, swaps: header + "/swap.img file 1048576 0 -2\n" + managedSwapFile + " file 3145728 0 -3\n", wantErr: true},
		{name: "target equal to current total", target: 4096, swaps: header + "/swap.img file 1048576 0 -2\n" + managedSwapFile + " file 3145728 0 -3\n", want: 3072},
		{name: "preserve legacy managed swap as external source", target: 4096, swaps: header + "/var/lib/kokoro/swapfile file 3145728 0 -3\n", want: 1024},
		{name: "remaining amount below minimum", target: 1100, swaps: header + "/swap.img file 1048576 0 -2\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := managedSwapFileSizeForTotal(tc.target, strings.NewReader(tc.swaps))
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("managed SWAP = %d MiB, want %d MiB", got, tc.want)
			}
		})
	}
}

func TestSwapCommandRejectsInvalidPayloadWithoutExecution(t *testing.T) {
	a, err := New(&model.AgentConfig{Hub: "https://example.invalid", Token: "nt_test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []model.Command{
		{ID: "bad-size", Type: "swap", Payload: map[string]any{"size_mb": 1}},
		{ID: "bad-field", Type: "swap", Payload: map[string]any{"size_mb": 1024, "command": "anything"}},
	} {
		res := a.runSwapContext(context.Background(), cmd)
		if res.OK || res.Stderr == "" {
			t.Errorf("invalid swap command was accepted: %+v", cmd)
		}
	}
}
