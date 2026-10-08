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
	data := adminPostData{
		adminData: h.adminBase(r),
		Node:      node,
		Profile:   prof,
		Saved:     r.URL.Query().Get("saved") == "1",
	}
	h.render(w, "admin_post.html", &data, r)
}

// savePost 处理文章正文的保存（由 handleAdminNodes 的 save_profile 分支调用）。
//
// ⚠️ 必须先读旧 profile 再合并：SaveProfile 是**整体 upsert**，
// 只塞表单里的几个字段会把封面、浏览量、UV 一起清零。
func (h *Hub) savePost(node *model.Node, form url.Values) {
	old, _ := h.store.GetProfile(node.ID)
	if old == nil {
		old = &model.NodeProfile{NodeID: node.ID}
	}
	old.Summary = clampRunes(strings.TrimSpace(form.Get("summary")), 200)
	old.ContentMD = clampRunes(form.Get("content_md"), maxPostRunes)
	old.Price = clampRunes(strings.TrimSpace(form.Get("price")), 64)
	old.ExpireAt = clampRunes(strings.TrimSpace(form.Get("expire_at")), 32)
	old.UpdatedAt = time.Now().UnixMilli()
	if err := h.store.SaveProfile(old); err != nil {
		log.Printf("[hub] 保存文章失败 node=%s: %v", node.ID, err)
	}
}
