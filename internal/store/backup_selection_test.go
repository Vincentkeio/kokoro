package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// 只导出节点时：节点表在，配置里只带迁移身份，其它分组不出现。
func TestSnapshotSelectionCopiesOnlySelectedTables(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "kokoro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	token := strings.Repeat("a", 64)
	if err := st.CreateNode(&model.Node{Name: "n1", TokenHash: token}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("site_name", "Secret Site"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting("panel_migration_key", "mig-key"); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(t.TempDir(), "snap.db")
	if err := st.SnapshotSelection(context.Background(), target, []string{"nodes"}, []string{"panel_migration_key"}); err != nil {
		t.Fatal(err)
	}
	snap, err := Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()

	if node, err := snap.GetNodeByTokenHash(token); err != nil || node == nil {
		t.Fatalf("节点身份没有复制过去: %v", err)
	}
	if v, _ := snap.GetSetting("panel_migration_key"); v != "mig-key" {
		t.Errorf("迁移身份应随节点一起带走，得到 %q", v)
	}
	if v, _ := snap.GetSetting("site_name"); v != "" {
		t.Errorf("没选配置时不该带上站点设置，得到 %q", v)
	}
	if err := snap.ValidateBackup(context.Background()); err != nil {
		t.Errorf("裁剪后的快照应通过完整性校验: %v", err)
	}
}

// 目标文件已存在时要拒绝，避免把旧内容混进新快照。
func TestSnapshotSelectionRejectsExistingTarget(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(filepath.Join(dir, "kokoro.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	target := filepath.Join(t.TempDir(), "snap.db")
	if err := st.SnapshotSelection(context.Background(), target, []string{"nodes"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SnapshotSelection(context.Background(), target, []string{"nodes"}, nil); err == nil {
		t.Error("目标已存在时应报错")
	}
}
