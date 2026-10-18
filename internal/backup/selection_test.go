package backup

import "testing"

func selectedKeys(keys ...string) map[string]bool {
	out := make(map[string]bool)
	for _, key := range keys {
		out[key] = true
	}
	return out
}

// 全选时走整库快照，不需要逐表复制。
func TestSelectGroupsAll(t *testing.T) {
	sel := SelectGroups(selectedKeys("nodes", "config", "themes", "history", "content"))
	if !sel.All {
		t.Fatal("五组全选时 All 应为真")
	}
	if len(sel.SettingsKeys) != 0 {
		t.Errorf("整份配置已包含，不该再单独指定 settings 键：%v", sel.SettingsKeys)
	}
	// 每组至少贡献一张表，且 nodes 只出现一次。
	seen := map[string]int{}
	for _, table := range sel.Tables {
		seen[table]++
	}
	for _, table := range []string{"nodes", "node_profile", "settings", "themes", "metrics_1m", "node_articles"} {
		if seen[table] != 1 {
			t.Errorf("%s 应恰好出现一次，得到 %d", table, seen[table])
		}
	}
}

// 只搬节点时：带节点三张表 + 迁移身份，不带整份配置。
func TestSelectGroupsNodesOnly(t *testing.T) {
	sel := SelectGroups(selectedKeys("nodes"))
	if sel.All {
		t.Fatal("只选节点不该走整库快照")
	}
	if len(sel.Tables) != 3 {
		t.Errorf("节点组应为 3 张表，得到 %v", sel.Tables)
	}
	if len(sel.SettingsKeys) != 1 || sel.SettingsKeys[0] != "panel_migration_key" {
		t.Errorf("只搬节点时要带上迁移身份，得到 %v", sel.SettingsKeys)
	}
	for _, table := range sel.Tables {
		if table == "settings" {
			t.Error("没选配置时不该整表复制 settings")
		}
	}
}

// 历史/内容引用节点：只勾它们时必须自动补上节点，否则外键校验过不了。
func TestSelectGroupsAutoAddsNodes(t *testing.T) {
	for _, key := range []string{"history", "content"} {
		sel := SelectGroups(selectedKeys(key))
		hasNodes := false
		for _, table := range sel.Tables {
			if table == "nodes" {
				hasNodes = true
			}
		}
		if !hasNodes {
			t.Errorf("只选 %s 时应自动带上 nodes，得到 %v", key, sel.Tables)
		}
		found := false
		for _, group := range sel.Groups {
			if group == "nodes" {
				found = true
			}
		}
		if !found {
			t.Errorf("实际分组里应包含 nodes，得到 %v", sel.Groups)
		}
	}
}

// 自定义主题可以单独导出（内置主题在二进制里，不参与备份）。
func TestSelectGroupsThemesOnly(t *testing.T) {
	sel := SelectGroups(selectedKeys("themes"))
	if len(sel.Tables) != 1 || sel.Tables[0] != "themes" {
		t.Errorf("主题组应只有 themes 表，得到 %v", sel.Tables)
	}
	if sel.All {
		t.Error("只选主题不该走整库快照")
	}
}

func TestSelectGroupsNothing(t *testing.T) {
	sel := SelectGroups(selectedKeys())
	if len(sel.Tables) != 0 || sel.All || len(sel.Groups) != 0 {
		t.Errorf("什么都没选时应为空，得到 %+v", sel)
	}
}
