package theme

// 内置主题的注册表。
//
// 5 套官方皮肤随二进制 go:embed，走与第三方完全相同的解析/校验路径，
// 所以「复制内置主题改一改再导入」天然可用，也不需要为内置主题写特例分支。

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
)

//go:embed builtin/*/theme.json
var builtinFS embed.FS

// DefaultID 是新安装时的默认主题。
const DefaultID = "kokoro.daylight"

// Registry 是主题注册表。并发安全，读多写少，用 RWMutex 就够。
type Registry struct {
	mu    sync.RWMutex
	items map[string]*Manifest
	order []string
}

// NewRegistry 解析全部内置主题并构造注册表。
//
// 任何一个内置主题解析失败都直接返回 error——内置主题是编译期常量，
// 解析不过说明代码有问题，不该在运行时静默降级。
func NewRegistry() (*Registry, error) {
	entries, err := fs.Glob(builtinFS, "builtin/*/theme.json")
	if err != nil {
		return nil, fmt.Errorf("theme: 枚举内置主题失败: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("theme: 没有找到任何内置主题")
	}
	r := &Registry{items: make(map[string]*Manifest, len(entries))}
	for _, e := range entries {
		data, err := builtinFS.ReadFile(e)
		if err != nil {
			return nil, fmt.Errorf("theme: 读取 %s 失败: %w", e, err)
		}
		m, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("theme: 内置主题 %s 无效: %w", path.Base(path.Dir(e)), err)
		}
		if !m.BuiltinID() {
			return nil, fmt.Errorf("theme: 内置主题 id %q 必须以 %q 开头", m.ID, ThemeIDPrefix)
		}
		m.Builtin = true
		m.Source = "builtin"
		r.items[m.ID] = m
		r.order = append(r.order, m.ID)
	}
	sort.Strings(r.order)
	if _, ok := r.items[DefaultID]; !ok {
		return nil, fmt.Errorf("theme: 默认主题 %q 不存在", DefaultID)
	}
	return r, nil
}

// Get 按 ID 取主题。
func (r *Registry) Get(id string) *Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.items[id]
}

// List 列出全部主题，默认主题排在最前。
func (r *Registry) List() []*Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Manifest, 0, len(r.items))
	for _, id := range r.order {
		if m := r.items[id]; m != nil {
			out = append(out, m)
		}
	}
	return out
}

// ListIDs 返回全部主题 ID，顺序稳定。
func (r *Registry) ListIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Resolve 返回 id 对应的主题；找不到或 id 为空时回落到默认主题。
//
// 这个"永不返回 nil"的约定很重要：渲染路径不必到处判空，
// 而且用户手动改库/删主题都不会让站点挂掉。
func (r *Registry) Resolve(id string) *Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if m := r.items[id]; m != nil {
		return m
	}
	return r.items[DefaultID]
}

// Register 登记一份自定义主题（同 ID 覆盖）。用于导入第三方主题。
func (r *Registry) Register(m *Manifest) error {
	if m == nil {
		return fmt.Errorf("theme: 主题为空")
	}
	if err := m.Validate(); err != nil {
		return err
	}
	// 官方保留前缀只归内置主题。放开它等于允许第三方冒充官方，
	// 访客看到 "kokoro." 前缀会默认信任签名，身份就废了。
	if strings.HasPrefix(m.ID, ThemeIDPrefix) && !m.Builtin {
		return fmt.Errorf("theme: id 前缀 %q 是官方保留的，社区主题请换一个", ThemeIDPrefix)
	}
	m.normalize()
	m.Builtin = false
	if m.Source == "" || m.Source == "builtin" {
		m.Source = "custom"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[m.ID]; !exists {
		r.order = append(r.order, m.ID)
		sort.Strings(r.order)
	}
	r.items[m.ID] = m
	return nil
}

// Unregister 移除一份自定义主题。内置主题不允许移除。
func (r *Registry) Unregister(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := r.items[id]
	if m == nil {
		return fmt.Errorf("theme: 主题 %q 不存在", id)
	}
	if m.Builtin {
		return fmt.Errorf("theme: 内置主题 %q 不能删除", id)
	}
	delete(r.items, id)
	for i, x := range r.order {
		if x == id {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
	return nil
}

// Import 解析一份主题 JSON 并登记进来。data 可以是 theme.json 原文。
func (r *Registry) Import(data []byte) (*Manifest, error) {
	m, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := r.Register(m); err != nil {
		return nil, err
	}
	return m, nil
}

// ValidID 校验主题 ID 的字符集（提前拒绝，避免注册表里出现奇怪 key）。
func ValidID(id string) bool {
	if len(id) < 3 || len(id) > 64 {
		return false
	}
	if strings.ContainsAny(id, " \t\n") {
		return false
	}
	for _, r := range id {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}
