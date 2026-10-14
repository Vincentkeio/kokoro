package store

// 每台机器的文章（一台多篇）。

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Vincentkeio/kokoro/internal/model"
)

const articleCols = `id, node_id, title, summary, content_md, sort_order, created_at, updated_at`

func scanArticle(sc scanner) (*model.Article, error) {
	var a model.Article
	err := sc.Scan(&a.ID, &a.NodeID, &a.Title, &a.Summary, &a.ContentMD,
		&a.SortOrder, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// ListArticles 取某台机器的文章。
//
// 排序：sort_order 小的在前，同序按时间倒序（最新的靠前）。
// 站长手动排过序就按他排的，没排过就按"最近写的在最上面"。
func (s *Store) ListArticles(nodeID string) ([]*model.Article, error) {
	rows, err := s.db.Query(
		`SELECT `+articleCols+` FROM node_articles WHERE node_id = ?
		 ORDER BY sort_order ASC, created_at DESC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("查文章失败: %w", err)
	}
	defer rows.Close()
	var out []*model.Article
	for rows.Next() {
		a, err := scanArticle(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetArticle 按 id 取一篇。
func (s *Store) GetArticle(id string) (*model.Article, error) {
	row := s.db.QueryRow(`SELECT `+articleCols+` FROM node_articles WHERE id = ?`, id)
	a, err := scanArticle(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// SaveArticle 保存这台机器的文章 —— **一台只有一篇**。
//
// boss 定的产品形态：一只小鸡一篇文章，评论评的就是这只小鸡。
// 所以这里是 upsert 语义：已经有了就改那一篇，没有才建。
//
// ⚠️ 用这个方法而不是 CreateArticle，是为了让"只有一篇"成为**数据层的
// 保证**，而不是靠调用方自觉。以后有人不小心调了两次 CreateArticle，
// 就会出现两台机器各显示半篇文章的糊涂账。
func (s *Store) SaveArticle(a *model.Article) error {
	if a == nil || a.NodeID == "" {
		return errors.New("文章缺少所属节点")
	}
	existing, err := s.ListArticles(a.NodeID)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		a.ID = existing[0].ID
		if a.CreatedAt == 0 {
			a.CreatedAt = existing[0].CreatedAt
		}
		return s.UpdateArticle(a)
	}
	return s.CreateArticle(a)
}

// CreateArticle 新增一篇。
//
// ⚠️ 正常情况下请用 SaveArticle —— 这个站在"一台只有一篇"的前提下
// 是低层接口，直接调它可能造出第二篇。
func (s *Store) CreateArticle(a *model.Article) error {
	if a == nil {
		return errors.New("文章为空")
	}
	if a.ID == "" {
		a.ID = newID("art_")
	}
	now := nowMS()
	if a.CreatedAt == 0 {
		a.CreatedAt = now
	}
	a.UpdatedAt = now
	autoSummary(a)
	_, err := s.db.Exec(
		`INSERT INTO node_articles (`+articleCols+`) VALUES (`+placeholders(articleCols)+`)`,
		a.ID, a.NodeID, a.Title, a.Summary, a.ContentMD, a.SortOrder, a.CreatedAt, a.UpdatedAt)
	return err
}

// UpdateArticle 改一篇。
func (s *Store) UpdateArticle(a *model.Article) error {
	if a == nil || a.ID == "" {
		return errors.New("文章为空")
	}
	a.UpdatedAt = nowMS()
	autoSummary(a)
	_, err := s.db.Exec(`UPDATE node_articles SET
title = ?, summary = ?, content_md = ?, sort_order = ?, updated_at = ?
WHERE id = ?`,
		a.Title, a.Summary, a.ContentMD, a.SortOrder, a.UpdatedAt, a.ID)
	return err
}

// DeleteArticle 删一篇。
func (s *Store) DeleteArticle(id string) error {
	_, err := s.db.Exec(`DELETE FROM node_articles WHERE id = ?`, id)
	return err
}

// autoSummary 没写摘要时，从正文里自动截一段。
//
// 为什么要有：列表页只显示标题 + 摘要，不渲染全文（可能是几万字）。
// 要求站长每篇都手填摘要太麻烦，直接从正文头部去掉 Markdown 记号截一段。
func autoSummary(a *model.Article) {
	if strings.TrimSpace(a.Summary) != "" {
		return
	}
	body := strings.TrimSpace(a.ContentMD)
	if body == "" {
		return
	}
	// 去掉常见的 Markdown 记号，别让摘要里出现 "## " "![图](...)" 这种东西
	var lines []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "![") || strings.HasPrefix(line, "```") ||
			strings.HasPrefix(line, ">") {
			continue
		}
		lines = append(lines, line)
		if len(lines) >= 2 { // 攒两行够长了
			break
		}
	}
	txt := strings.Join(lines, " ")
	txt = strings.NewReplacer("**", "", "__", "", "`", "", "*", "").Replace(txt)
	const maxRunes = 120
	r := []rune(txt)
	if len(r) > maxRunes {
		txt = string(r[:maxRunes]) + "…"
	}
	a.Summary = txt
}

// SortArticles 让文章的排序稳定（给测试和后台用）。
func SortArticles(list []*model.Article) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].SortOrder != list[j].SortOrder {
			return list[i].SortOrder < list[j].SortOrder
		}
		return list[i].CreatedAt > list[j].CreatedAt
	})
}
