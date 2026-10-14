package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// NodeGroup is a named collection of nodes in the admin console.
type NodeGroup struct {
	Name  string
	Count int
}

// ListNodeGroups returns groups and their current membership counts.
func (s *Store) ListNodeGroups() ([]NodeGroup, error) {
	rows, err := s.db.Query(`SELECT g.name, COUNT(n.id)
FROM node_groups g LEFT JOIN nodes n ON n.group_name=g.name
GROUP BY g.name ORDER BY g.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var groups []NodeGroup
	for rows.Next() {
		var group NodeGroup
		if err := rows.Scan(&group.Name, &group.Count); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// CreateNodeGroup creates a group. Names are unique after trimming.
func (s *Store) CreateNodeGroup(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("分组名称不能为空")
	}
	if name == "全部" {
		return errors.New("“全部”是固定筛选标签，不能用作分组名称")
	}
	_, err := s.db.Exec(`INSERT INTO node_groups(name,created_at) VALUES(?,?)`, name, time.Now().UnixMilli())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return fmt.Errorf("分组“%s”已经存在", name)
		}
		return fmt.Errorf("创建分组失败: %w", err)
	}
	return nil
}

// RenameNodeGroup changes a group's name and keeps every node assigned to it.
func (s *Store) RenameNodeGroup(oldName, newName string) error {
	oldName = strings.TrimSpace(oldName)
	newName = strings.TrimSpace(newName)
	if newName == "" {
		return errors.New("分组名称不能为空")
	}
	if newName == "全部" {
		return errors.New("“全部”是固定标签，不能用作分组名称")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE node_groups SET name=? WHERE name=?`, newName, oldName)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") || strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return fmt.Errorf("分组“%s”已经存在", newName)
		}
		return fmt.Errorf("修改分组失败: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("分组不存在，请刷新页面")
	}
	if _, err := tx.Exec(`UPDATE nodes SET group_name=? WHERE group_name=?`, newName, oldName); err != nil {
		return fmt.Errorf("更新分组成员失败: %w", err)
	}
	return tx.Commit()
}

// AssignNodesToGroup moves the selected nodes into one group without changing
// any other members already in that group.
func (s *Store) AssignNodesToGroup(name string, nodeIDs []string) error {
	name = strings.TrimSpace(name)
	if len(nodeIDs) == 0 {
		return errors.New("请先勾选要加入分组的节点")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM node_groups WHERE name=?`, name).Scan(&exists); err == sql.ErrNoRows {
		return errors.New("分组不存在，请刷新页面")
	} else if err != nil {
		return err
	}
	seen := make(map[string]bool, len(nodeIDs))
	assigned := 0
	for _, id := range nodeIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		res, err := tx.Exec(`UPDATE nodes SET group_name=? WHERE id=?`, name, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return errors.New("所选节点已不存在，请刷新页面")
		}
		assigned++
	}
	if assigned == 0 {
		return errors.New("请先勾选要加入分组的节点")
	}
	return tx.Commit()
}

// DeleteNodeGroup removes a group and returns its nodes to the ungrouped list.
func (s *Store) DeleteNodeGroup(name string) error {
	name = strings.TrimSpace(name)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE nodes SET group_name='' WHERE group_name=?`, name); err != nil {
		return err
	}
	res, err := tx.Exec(`DELETE FROM node_groups WHERE name=?`, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("分组不存在，请刷新页面")
	}
	return tx.Commit()
}
