package hub

import (
	"strings"
	"testing"

	"github.com/Vincentkeio/kokoro/internal/backup"
)

// 导出表单必须覆盖备份包里的每个分组：漏一个复选框，那一组就永远导不出去。
// 同时确认默认全选（用户不用每次手动勾）。
func TestBackupExportTemplateCoversEveryGroup(t *testing.T) {
	raw, err := templatesFS.ReadFile("templates/migration.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	for _, group := range backup.Groups {
		field := `name="include_` + group.Key + `"`
		if !strings.Contains(page, field) {
			t.Errorf("导出表单缺少分组 %s（%s）", group.Key, field)
			continue
		}
		if !strings.Contains(page, field+" checked") {
			t.Errorf("分组 %s 默认应勾选", group.Key)
		}
	}
	if !strings.Contains(page, `name="include_files" checked`) {
		t.Error("图片文件应默认勾选")
	}
}
