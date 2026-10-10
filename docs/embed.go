// Package docs 把仓库里的文档打进二进制，供后台的「下载 AI 提示词」
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

//go:embed themes/AI-PROMPT.md
var aiPrompt []byte

// AIPrompt 返回「给 AI 写主题用的提示词」原文。
//
// 它自包含：硬约束、字段表、101 个合法变量清单全在里面，用户整段丢给 AI
// 就能产出能过校验的主题，不需要另外附带 THEME-FORMAT.md。
func AIPrompt() []byte { return aiPrompt }
