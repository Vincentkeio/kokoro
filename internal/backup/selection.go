package backup

// 导出备份时可选的内容分组。
//
// 分组的边界不是随意的：每一组要么自身引用闭合，要么由 SelectGroups 自动补上
// 被引用的 nodes 组——否则导入时的 foreign_key_check 会判定备份损坏。
type Group struct {
	Key    string
	Label  string
	Tables []string
}

// FilesKey 是"图片等上传文件"这一组的表单字段后缀。
const FilesKey = "files"

// Groups 是导出界面上的复选项，顺序即界面顺序。
var Groups = []Group{
	{
		Key:   "nodes",
		Label: "节点信息",
		// 节点身份（token_hash）在这里，探针能不能重新认上新面板就靠它。
		Tables: []string{"nodes", "node_groups", "node_profile"},
	},
	{
		Key:    "config",
		Label:  "配置",
		Tables: []string{"settings", "alert_rules", "install_tokens"},
	},
	{
		Key:   "themes",
		Label: "自定义主题",
		// 只有用户导入/创建的主题存档；内置主题在二进制里，不需要导出。
		Tables: []string{"themes"},
	},
	{
		Key:   "history",
		Label: "历史数据",
		Tables: []string{
			"metrics_raw", "metrics_1m", "metrics_5m", "metrics_1h", "metrics_1d",
			"uptime_buckets", "net_quality", "events", "node_tasks",
			"alert_events", "audit_log", "expiry_reminder_deliveries",
		},
	},
	{
		Key:   "content",
		Label: "内容",
		Tables: []string{
			"node_articles", "comments", "votes", "buy_intents", "site_messages", "trade_offers",
		},
	},
}

// selectionTables 汇总所有可选表，用于校验调用方传来的表名。
var selectionTables = func() map[string]bool {
	out := make(map[string]bool)
	for _, group := range Groups {
		for _, table := range group.Tables {
			out[table] = true
		}
	}
	return out
}()

// Selection 是一次导出实际包含的内容。
type Selection struct {
	// Tables 是要复制的表；All 为真时可以走整库快照。
	Tables []string
	// SettingsKeys 非空时只复制这些 settings 键（不要整份配置、但要迁移身份）。
	SettingsKeys []string
	// Groups 是实际包含的分组，可能比用户勾选的多一个自动补上的 nodes。
	Groups []string
	All    bool
}

// SelectGroups 把勾选结果展开成表清单。
//
// 历史数据和内容都引用节点：选中任一者就自动带上节点信息，否则备份会因外键
// 约束无法通过导入校验。只勾"节点信息"时，额外带上迁移身份所需的那个 settings
// 键——否则旧面板的"验证并迁移探针"会被新面板拒绝（迁移身份不匹配）。
func SelectGroups(selected map[string]bool) Selection {
	needNodes := selected["nodes"] || selected["history"] || selected["content"]
	all := needNodes && selected["config"] && selected["themes"] &&
		selected["history"] && selected["content"]

	var out Selection
	seen := make(map[string]bool)
	addGroup := func(key string) {
		for _, group := range Groups {
			if group.Key != key {
				continue
			}
			for _, table := range group.Tables {
				if !seen[table] {
					seen[table] = true
					out.Tables = append(out.Tables, table)
				}
			}
			out.Groups = append(out.Groups, group.Key)
		}
	}
	if needNodes {
		addGroup("nodes")
	}
	for _, key := range []string{"config", "themes", "history", "content"} {
		if selected[key] {
			addGroup(key)
		}
	}
	if needNodes && !selected["config"] {
		out.SettingsKeys = []string{"panel_migration_key"}
	}
	out.All = all
	return out
}

// ValidTable 报告表名是否属于可选分组（防止把调用方传入的字符串直接拼进 SQL）。
func ValidTable(name string) bool { return selectionTables[name] }
