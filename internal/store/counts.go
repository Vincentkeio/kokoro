package store

// 卡片上的计数：文章数、已审核评论数。
//
// ⚠️ 都是**批量**接口：首页一次渲染几十张卡，
// 每张各查一次就是几十次往返。一次 GROUP BY 全拿回来。

import "fmt"

// CountByNode 统计某张表里每台机器的条数。
//
// whereExtra 是附加的过滤条件（比如评论只数已审核的）。
func (s *Store) CountByNode(table, whereExtra string) (map[string]int, error) {
	q := `SELECT node_id, COUNT(*) FROM ` + table
	if whereExtra != "" {
		q += ` WHERE ` + whereExtra
	}
	q += ` GROUP BY node_id`
	rows, err := s.db.Query(q)
	if err != nil {
		return nil, fmt.Errorf("统计 %s 失败: %w", table, err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
