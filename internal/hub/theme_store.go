package hub

// 自定义主题的持久化。
//
// 背景：注册表（internal/theme.Registry）是纯内存的，而数据库里早就有 themes 表
// 与 SaveTheme——但一直到这次才真正接上。接上之前，从后台导入或一键获取来的
// 主题在进程重启后会全部消失，站点悄悄退回默认皮肤。这类「看起来能用、
// 重启就丢」的 bug 最难被发现，所以单独一个文件把这条链路钉住：
//
//	导入 / 抓取 / 切换 → 落库 → 启动时读回 → 注册表复原
//
// 为什么不用 themes 表的 enabled 列做「当前主题」：那一列的语义是
// 「这套主题存在且被选中」，与内置主题无关（内置主题在二进制里，永远 enabled=0）。
// 真要混用，就得让每套内置主题各插一行空 manifest，太脏。
// 所以站点级选择继续放在 settings.site_theme，themes 表只管自定义主题的存档。

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/Vincentkeio/kokoro/internal/model"
	"github.com/Vincentkeio/kokoro/internal/theme"
)

// persistTheme 把一份已通过校验的自定义主题写进数据库。
//
// sourceURL 记录它是从哪拿来的：一键获取时填站点地址，手动导入留空。
// 这个字段以后要做「检查主题更新」时会用到，也是出了版权问题时唯一的追溯线索。
func (h *Hub) persistTheme(m *theme.Manifest, bundle []byte, sourceURL string) error {
	if h.store == nil || m == nil {
		return fmt.Errorf("无法保存主题：存储未就绪")
	}
	raw, err := json.MarshalIndent(exportManifest(m), "", "  ")
	if err != nil {
		return fmt.Errorf("序列化主题失败: %w", err)
	}
	var meta []byte
	if m.Package != nil {
		if b, err := json.Marshal(m.Package); err == nil {
			meta = b
		}
	}
	sum := sha256.Sum256(raw)
	t := &model.Theme{
		ID:        m.ID,
		Name:      m.Name,
		Author:    m.Author,
		Version:   m.Version,
		Homepage:  m.Homepage,
		SourceURL: sourceURL,
		Checksum:  hex.EncodeToString(sum[:]),
		Manifest:  raw,
		Bundle:    bundle,
		Meta:      meta,
	}
	return h.store.SaveTheme(t)
}

// removeTheme 删除一份自定义主题的存档。
func (h *Hub) removeTheme(id string) error {
	if h.store == nil {
		return nil
	}
	rows, err := h.store.ListThemes()
	if err != nil {
		return err
	}
	for _, t := range rows {
		if t.ID == id {
			if err := h.store.DeleteTheme(id); err != nil {
				return err
			}
			return nil
		}
	}
	return nil
}

// loadStoredThemes 在启动时把数据库里的自定义主题读回注册表。
//
// 任何一份解析失败都只记日志并跳过，不让一份坏存档把整个站点拖下水——
// 内置主题还在，用户顶多少一套皮肤，不至于白屏。
func (h *Hub) loadStoredThemes() {
	if h.store == nil || h.themes == nil {
		return
	}
	rows, err := h.store.ListThemes()
	if err != nil {
		log.Printf("[theme] 读取已存主题失败: %v", err)
		return
	}
	n := 0
	for i := range rows {
		t := rows[i]
		// 黑金和钛光现随 Hub 内置分发。旧版本导入的副本保留在数据库中，
		// 但不再作为第二个可选主题登记。
		legacyBuiltins := map[string]string{
			"jiwo.blackgold": "kokoro.blackgold",
			"jiwo.future":   "kokoro.titanium",
		}
		if replacement := legacyBuiltins[t.ID]; replacement != "" && h.themes.Get(replacement) != nil {
			log.Printf("[theme] 忽略旧版官方主题存档 %s，使用内置主题 %s", t.ID, replacement)
			continue
		}
		if len(t.Manifest) == 0 {
			continue
		}
		m, err := theme.Parse(t.Manifest)
		if err != nil {
			log.Printf("[theme] 跳过存档 %s：%v", t.ID, err)
			continue
		}
		if t.ID != "" && t.ID != m.ID {
			// ID 对不上说明存档被手改过，以清单为准，但要留痕。
			log.Printf("[theme] 存档 ID(%s) 与清单 ID(%s) 不一致，按清单登记", t.ID, m.ID)
		}
		m.Source = "custom"
		// 包元数据必须从 themes.meta 快照还原。exportManifest 刻意不序列化
		// Package（它是"这份主题带没带包"的凭证，不是渲染输入），而
		// /_theme-assets/ 路由和主题包自带的背景图都要靠它。
		// 漏了这一步，重启后 m.Package 恒为 nil，主题包自己的图与资源
		// （包括它导出的）会一起消失，且不报任何错。
		if len(t.Meta) > 0 && m.Package == nil {
			var pkg theme.Package
			if err := json.Unmarshal(t.Meta, &pkg); err != nil {
				log.Printf("[theme] 存档 %s 的包元数据解析失败：%v", m.ID, err)
			} else {
				m.Package = &pkg
			}
		}
		var assets []theme.Asset
		if len(t.Bundle) > 0 {
			bundle, err := theme.ExtractPackage(t.Bundle)
			if err != nil {
				log.Printf("[theme] 跳过资源包损坏的主题 %s：%v", m.ID, err)
				continue
			}
			packaged, err := theme.Parse(bundle.ManifestRaw)
			if err != nil || packaged.ID != m.ID {
				log.Printf("[theme] 跳过资源包与清单不一致的主题 %s", m.ID)
				continue
			}
			assets = bundle.Assets
		}
		if err := theme.ValidateAssetReferences(m, assets); err != nil {
			log.Printf("[theme] 跳过资源缺失的主题 %s：%v", m.ID, err)
			continue
		}
		if err := h.themes.Register(m); err != nil {
			log.Printf("[theme] 登记存档 %s 失败：%v", m.ID, err)
			continue
		}
		// 存档里带了原始包就顺便把资源解出来缓存，否则重启后
		// /_theme-assets/ 会一直 404，背景图全丢。
		h.themeAssets.set(m.ID, assets)
		n++
	}
	if n > 0 {
		log.Printf("[theme] 已从数据库恢复 %d 套自定义主题", n)
	}
}

// storedBundle 取回某套自定义主题的原始包字节，用于「导出成包」。
func (h *Hub) storedBundle(id string) ([]byte, bool) {
	if h.store == nil {
		return nil, false
	}
	rows, err := h.store.ListThemes()
	if err != nil {
		return nil, false
	}
	for _, t := range rows {
		if t.ID == id && len(t.Bundle) > 0 {
			return t.Bundle, true
		}
	}
	return nil, false
}

// storedSourceURL 取回主题的来源地址，后台展示用。
func (h *Hub) storedSourceURL(id string) string {
	if h.store == nil {
		return ""
	}
	rows, err := h.store.ListThemes()
	if err != nil {
		return ""
	}
	for _, t := range rows {
		if t.ID == id {
			return strings.TrimSpace(t.SourceURL)
		}
	}
	return ""
}
