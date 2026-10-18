package agent

import (
	"context"
	"os"
	"path/filepath"
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
	const managed = "/swapfile"
	for _, tc := range []struct {
		name    string
		target  int
		swaps   string
		want    int
		wantErr bool
	}{
		{name: "no existing swap", target: 3072, swaps: header, want: 3072},
		{name: "preserve other swap", target: 3072, swaps: header + "/swap.img file 1048576 0 -2\n", want: 2048},
		{name: "ignore managed swap in total", target: 3072, swaps: header + "/swap.img file 1048576 0 -2\n" + managed + " file 2097152 0 -3\n", want: 2048},
		{name: "external swap already meets target", target: 1024, swaps: header + "/swap.img file 1048576 0 -2\n", wantErr: true},
		{name: "external swap exceeds target", target: 1024, swaps: header + "/swap.img file 2097152 0 -2\n", wantErr: true},
		{name: "target below current total including managed swap", target: 3072, swaps: header + "/swap.img file 1048576 0 -2\n" + managed + " file 3145728 0 -3\n", wantErr: true},
		{name: "target equal to current total", target: 4096, swaps: header + "/swap.img file 1048576 0 -2\n" + managed + " file 3145728 0 -3\n", want: 3072},
		{name: "legacy kokoro path counts as external", target: 4096, swaps: header + "/var/lib/kokoro/swapfile file 3145728 0 -3\n", want: 1024},
		{name: "second slot managed file", target: 4096, swaps: header + "/swapfile2 file 1048576 0 -3\n", want: 3072},
		{name: "remaining amount below minimum", target: 1100, swaps: header + "/swap.img file 1048576 0 -2\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := managedSwapFileSizeForTotal(managed, tc.target, strings.NewReader(tc.swaps))
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if err == nil && got != tc.want {
				t.Errorf("managed SWAP = %d MiB, want %d MiB", got, tc.want)
			}
		})
	}
}

// 交换文件放在系统根目录，不放进任何 Kokoro 目录；别人的交换文件绝不覆盖。
func TestResolveManagedSwapFile(t *testing.T) {
	dir := t.TempDir()
	const mark = "# kokoro-managed-swap"

	got, err := resolveManagedSwapFile(dir, "")
	if err != nil {
		t.Fatalf("空目录应当可以选位: %v", err)
	}
	if got != filepath.Join(dir, "swapfile") {
		t.Errorf("首选 %s，得到 %q", filepath.Join(dir, "swapfile"), got)
	}

	// 被别人占用（普通文件、fstab 里没有我们的标记）就跳到下一个空位。
	if err := os.WriteFile(filepath.Join(dir, "swapfile"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = resolveManagedSwapFile(dir, "")
	if err != nil {
		t.Fatalf("应当跳到下一个空位: %v", err)
	}
	if got != filepath.Join(dir, "swapfile2") {
		t.Errorf("期望换到 swapfile2，得到 %q", got)
	}

	// 已经有我们标记过的文件就继续用它，避免同机出现两个 Kokoro 交换文件。
	fstab := "/swapfile2 none swap sw 0 0 " + mark + "\n"
	got, err = resolveManagedSwapFile(dir, fstab)
	if err != nil {
		t.Fatalf("应当复用已标记的文件: %v", err)
	}
	if got != filepath.Join(dir, "swapfile2") {
		t.Errorf("期望复用 swapfile2，得到 %q", got)
	}

	// 目录占着名字也要跳过。
	dir2 := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir2, "swapfile"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err = resolveManagedSwapFile(dir2, "")
	if err != nil {
		t.Fatalf("目录占位时应当继续找: %v", err)
	}
	if got != filepath.Join(dir2, "swapfile2") {
		t.Errorf("期望 swapfile2，得到 %q", got)
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
