// Package docs 把仓库里的文档打进二进制，供后台的「下载主题范例」
// 这类入口直接吐给用户。
//
// 为什么 docs/ 里会有一个 Go 文件：**`go:embed` 不能跨目录向上引用**
// （`//go:embed ../docs/x.md` 是非法的），所以想让程序读到这些文档，
// 唯一的办法就是让 docs/ 自己成为一个包。
//
// 只 embed 真正需要随二进制分发的 —— 全量 docs 有几百 KB，
// THEME-SPEC.md 那种历史文档没必要进制品。
package docs

import _ "embed"

//go:embed themes/theme-example.md
var themeExample []byte

// ThemeExample 返回带完整变量和布局选项说明的主题范例。
func ThemeExample() []byte { return themeExample }
