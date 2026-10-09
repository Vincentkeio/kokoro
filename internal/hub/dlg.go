package hub

// 卡片上「文章 / 评论」弹窗的数据接口。
//
// 点卡片上的「文章 3」「评论 5」时，前端按 slug 拉这两个端点，
// 填进弹窗里。为什么不做成服务端一次渲染好整页：
//   1) 评论可能上百条，全塞进首页会让首页体积翻好几倍
//   2) 弹窗只在真的点开时才需要数据，绝大多数访客不点
// 所以按需拉，并且**分页**。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// 弹窗里每页的条数。
//
// 文章**没有分页** —— 产品形态是"一只小鸡一篇文章"（boss 定的），
// 一篇就是全部，给个翻页按钮反而是凭空多出来一个一直是"1/1"的控件。
// 评论会很多，所以分页。
const drawerCommentPage = 20

// drawerArticle 是弹窗里的一篇文章。
type drawerArticle struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
	HTML    string `json:"html"`
	When    string `json:"when"`
	Index   int    `json:"index"` // 第几篇，从 1 数，给"第 2/5 篇"用
}

// drawerComment 是弹窗里的一条评论。
type drawerComment struct {
	Author  string `json:"author"`
	Content string `json:"content"`
	When    string `json:"when"`
	HTML    string `json:"html"` // 已审核的才带；未审核的只有管理员看得到
}

// dlgResp 是弹窗接口的统一响应。
type dlgResp struct {
	OK    bool `json:"ok"`
	Total int  `json:"total"`
	Page  int  `json:"page"`
	Pages int  `json:"pages"`
	// IsAdmin 让前端决定要不要渲染「编辑文章 / 管理评论」这两个入口。
	// 后端那边照样会再判一次权限 —— 前端只是决定显不显示。
	IsAdmin bool `json:"is_admin"`
	// Articles / Comments 二选一，按请求的端点填。
	Articles []drawerArticle `json:"articles,omitempty"`
	Comments []drawerComment `json:"comments,omitempty"`
	// EditURL / ManageURL 只有管理员才带，前端直接拿来当链接。
	EditURL   string `json:"edit_url,omitempty"`
	ManageURL string `json:"manage_url,omitempty"`
}

// handleNodeArticles 处理 /n/<slug>/articles.json?page=N
func (h *Hub) handleNodeArticles(w http.ResponseWriter, r *http.Request, slug string) {
	node, err := h.store.GetNodeBySlug(slug)
	if err != nil || node == nil {
		writeErr(w, http.StatusNotFound, "节点不存在")
		return
	}
	// 私有节点对访客完全不可见，弹窗也不能漏
	admin := h.adminAuthed(r)
	if node.Visibility == model.VisibilityPrivate && !admin {
		writeErr(w, http.StatusNotFound, "节点不存在")
		return
	}

	arts, err := h.store.ListArticles(node.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取文章失败")
		return
	}

	// 一台只有一篇 —— 有就给它，没有就是空列表（前端显示"还没写文章"）
	resp := dlgResp{OK: true, Total: len(arts), Page: 1, Pages: 1, IsAdmin: admin}
	if admin {
		resp.EditURL = "/admin/post?node=" + node.ID
	}
	if len(arts) > 0 {
		a := arts[0]
		resp.Articles = append(resp.Articles, drawerArticle{
			Title:   firstNonEmpty(a.Title, "无标题"),
			Summary: a.Summary,
			HTML:    string(RenderMarkdown(a.ContentMD)),
			When:    relativeTime(a.CreatedAt),
			Index:   1,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleNodeComments 处理 /n/<slug>/comments.json?page=N
func (h *Hub) handleNodeComments(w http.ResponseWriter, r *http.Request, slug string) {
	node, err := h.store.GetNodeBySlug(slug)
	if err != nil || node == nil {
		writeErr(w, http.StatusNotFound, "节点不存在")
		return
	}
	admin := h.adminAuthed(r)
	if node.Visibility == model.VisibilityPrivate && !admin {
		writeErr(w, http.StatusNotFound, "节点不存在")
		return
	}

	// 访客只看已审核的；管理员能看到全部（含待审），
	// 否则他在弹窗里没法判断"这条该不该放出去"。
	cs, err := h.store.ListComments(node.ID, !admin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取评论失败")
		return
	}

	page, pages := pagedIndex(r, len(cs), drawerCommentPage)
	resp := dlgResp{OK: true, Total: len(cs), Page: page, Pages: pages, IsAdmin: admin}
	if admin {
		resp.ManageURL = "/admin#comments"
	}
	start := (page - 1) * drawerCommentPage
	for i := start; i < start+drawerCommentPage && i < len(cs); i++ {
		c := cs[i]
		item := drawerComment{
			Author:  firstNonEmpty(c.Author, "匿名"),
			Content: c.Content,
			When:    relativeTime(c.CreatedAt),
		}
		// 待审的用纯文本 —— 别把没放行的内容当 HTML 渲染出去
		if c.Status == model.CommentApproved {
			item.HTML = string(RenderMarkdown(c.Content))
		} else {
			item.HTML = template_HTMLEscape(c.Content)
		}
		resp.Comments = append(resp.Comments, item)
	}
	writeJSON(w, http.StatusOK, resp)
}

// pagedIndex 解析 ?page=N 并夹到合法范围。
//
// 夹而不报错：URL 是手改得出来的，?page=999 不该给个 400，
// 直接给最后一页更友好。
func pagedIndex(r *http.Request, total, perPage int) (page, pages int) {
	pages = (total + perPage - 1) / perPage
	if pages < 1 {
		pages = 1
	}
	page, _ = strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("page")))
	if page < 1 {
		page = 1
	}
	if page > pages {
		page = pages
	}
	return page, pages
}

// template_HTMLEscape 把纯文本转义成能安全塞进 innerHTML 的形式。
func template_HTMLEscape(s string) string {
	var b bytes.Buffer
	json.HTMLEscape(&b, []byte(s))
	return b.String()
}
