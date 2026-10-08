package hub

// 后台的「文章」编辑页：每台小鸡一篇文章。
//
// 数据落在 node_profile.content_md（Markdown），详情页用 md 模板函数渲染。
// 之所以天然就是「一台一篇」：node_profile 以 node_id 为主键，
// SaveProfile 是 upsert，不会出现第二篇。

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Vincentkeio/kokoro/internal/model"
)

// adminPostData 是文章编辑页的数据。
type adminPostData struct {
	adminData
	Node    *model.Node
	Profile *model.NodeProfile
	// Article 是这台机器的第一篇文章。编辑器编辑的就是它 ——
	// 老的"一台一篇"入口，内容已迁到 node_articles。
	Article *model.Article
	Saved   bool
}

// maxPostRunes 限制正文长度。
//
// 不设上限的话，粘贴一篇几十万字的文档会：撑爆页面、拖慢每次详情页渲染
// （Markdown 转换是同步的）、把 DB 撑大。64K 字符对"一篇介绍"绰绰有余。
const maxPostRunes = 64000

func (h *Hub) handleAdminPost(w http.ResponseWriter, r *http.Request, authed bool) {
	if !authed {
		// 未登录时不暴露任何节点信息，直接回后台登录页
		h.render(w, "login.html", map[string]any{"SiteName": h.cfg.SiteName}, r)
		return
	}
	nodeID := strings.TrimSpace(r.URL.Query().Get("node"))
	node, err := h.store.GetNode(nodeID)
	if err != nil || node == nil {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	prof, _ := h.store.GetProfile(node.ID)
	if prof == nil {
		prof = &model.NodeProfile{NodeID: node.ID}
	}
	// 第一篇文章 —— 没有就现造一个空的，让编辑框有东西可填
	arts, _ := h.store.ListArticles(node.ID)
	var first *model.Article
	if len(arts) > 0 {
		first = arts[0]
	} else {
		first = &model.Article{NodeID: node.ID}
	}
	data := adminPostData{
		adminData: h.adminBase(r),
		Node:      node,
		Profile:   prof,
		Article:   first,
		Saved:     r.URL.Query().Get("saved") == "1",
	}
	h.render(w, "admin_post.html", &data, r)
}

// savePost 处理文章正文的保存（由 handleAdminNodes 的 save_profile 分支调用）。
//
// ⚠️ 必须先读旧 profile 再合并：SaveProfile 是**整体 upsert**，
// 只塞表单里的几个字段会把封面、浏览量、UV 一起清零。
func (h *Hub) savePost(node *model.Node, form url.Values) {
	summary := clampRunes(strings.TrimSpace(form.Get("summary")), 200)
	content := clampRunes(form.Get("content_md"), maxPostRunes)
	title := clampRunes(strings.TrimSpace(form.Get("title")), 120)

	// 名片部分（摘要/价格/到期）还是存 node_profile ——
	// 这些是"这台机器"的属性，不是"某篇文章"的属性。
	old, _ := h.store.GetProfile(node.ID)
	if old == nil {
		old = &model.NodeProfile{NodeID: node.ID}
	}
	old.Summary = summary
	old.ContentMD = content // 保留一份，老数据/导出还用得上
	old.Price = clampRunes(strings.TrimSpace(form.Get("price")), 64)
	old.ExpireAt = clampRunes(strings.TrimSpace(form.Get("expire_at")), 32)
	old.UpdatedAt = time.Now().UnixMilli()
	if err := h.store.SaveProfile(old); err != nil {
		log.Printf("[hub] 保存名片失败 node=%s: %v", node.ID, err)
	}

	// 正文写透到文章表 —— 详情页读的是这里。
	arts, _ := h.store.ListArticles(node.ID)
	if len(arts) == 0 {
		if strings.TrimSpace(content) == "" && title == "" {
			return // 什么都没填，别建空文章
		}
		a := &model.Article{NodeID: node.ID, Title: title, Summary: summary, ContentMD: content}
		if err := h.store.CreateArticle(a); err != nil {
			log.Printf("[hub] 新建文章失败 node=%s: %v", node.ID, err)
		}
		return
	}
	a := arts[0]
	a.Title, a.ContentMD = title, content
	if strings.TrimSpace(a.Summary) == "" {
		a.Summary = summary
	}
	if err := h.store.UpdateArticle(a); err != nil {
		log.Printf("[hub] 保存文章失败 node=%s: %v", node.ID, err)
	}
}
