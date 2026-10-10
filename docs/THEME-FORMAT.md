# Kokoro 主题格式规范 v1

> 这份文档的用途：**让你（或你手里的 AI）只读这一篇，就能写出一份一定能通过校验的主题文件。**
>
> 配套文件：
> - `docs/themes/theme.schema.json` —— JSON Schema（编辑器自动提示、CI 校验都用它）
> - `docs/themes/minimal.json` —— 能直接用的最小示例
> - `docs/themes/AI-PROMPT.md` —— 可直接复制粘贴给 AI 的提示词
> - 线上：任意 Kokoro 站点的 `/theme.json` 就是一份真实可用的清单

---

## 目录

1. [三十秒上手](#1-三十秒上手)
2. [主题文件是什么](#2-主题文件是什么)
3. [完整字段表](#3-完整字段表)
4. [第一层：tokens（颜色、字体、圆角…）](#4-第一层tokens颜色字体圆角)
5. [第二层：layout（布局与形态）](#5-第二层layout布局与形态)
6. [标准主题包 `.kokoro-theme`](#6-标准主题包-kokoro-theme)
7. [安全边界（为什么某些写法会被拒绝）](#7-安全边界为什么某些写法会被被拒绝)
8. [一键获取主题](#8-一键获取主题)
9. [常见错误与修正](#9-常见错误与修正)
10. [从零写一套主题](#10-从零写一套主题)

---

## 1. 三十秒上手

**最省事的办法**：打开任意一台 Kokoro 探针，访问它的 `/theme.json`，把内容整个复制下来改。

内置主题在二进制里，但可以这样导出成一份可分享的清单：

```
GET /theme-export/kokoro.daylight     →  返回 JSON，Content-Disposition 触发下载
GET /theme.json                       →  返回当前生效主题的清单（公开，无鉴权）
```

**改动最小的原则**：只写你想改的键，其余全走默认。

```json
{
  "schemaVersion": 1,
  "id": "acme.ocean",
  "name": "海盐",
  "version": "1.0.0",
  "author": "acme",
  "tokensSchemaVersion": 2,
  "tokens": {
    "--kokoro-color-primary": "#0a7ea4",
    "--kokoro-color-accent": "#f4a261"
  },
  "layoutSchemaVersion": 1,
  "layout": {
    "home": { "list": { "mode": "card" } }
  }
}
```

这一份就能用。只有 3 个键有实际效果，其余是必填的身份字段。

---

## 2. 主题文件是什么

主题是**一个 JSON 文件**，叫 `theme.json`。它有两层：

| 层 | 字段 | 管什么 | 能做的事 |
| --- | --- | --- | --- |
| 第一层 | `tokens` / `tokensDark` | 设计变量 | 101 个 CSS 变量的取值 |
| 第二层 | `layout` | 布局偏好 | 列表形态、顶栏形态、图表类型、详情页头部… |

**主题不能做的事**（重要）：

- ❌ 不能写任意 CSS 选择器（没有 `styles` 字段）
- ❌ 不能写 JavaScript
- ❌ 不能引用远程资源（`url(https://…`、`@import`）
- ❌ 不能覆盖 `--kokoro-*` 以外的任何变量
- ❌ 不能用 `kokoro.` 开头的 ID（官方保留）

这不是功能缺失，是**刻意的设计**。因为主题可以一键从别人的站导入，如果主题能执行任意代码，那「一键获取」就等于「一键执行陌生人的代码」。所以主题的表达能力被限制在「一组受校验的变量 + 一组枚举选择」。

如果你需要真正的自定义 CSS/HTML/Javascript，请 fork 项目改模板，不要指望主题文件。

---

## 3. 完整字段表

### 3.1 顶层字段

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `schemaVersion` | number | 否¹ | 清单结构版本，当前为 `1`。填 0 或省略视为 1；**填大于 1 会被拒绝** |
| `id` | string | ✅ | 主题唯一标识。3–64 字符，只能含 `a-z`、`0-9`、`.`、`_`、`-`。**不能以 `kokoro.` 开头** |
| `name` | string | ✅ | 显示名称（后台列表、皮肤下拉） |
| `version` | string | ✅ | 版本号，建议用语义化版本 `1.0.0` |
| `description` | string | 否 | 一句话说明，显示在主题选择器里 |
| `author` | string | 否⁴ | 作者名，留空会填「未知作者」 |
| `homepage` | string | 否 | 主题主页，必须是 `http://` 或 `https://` 开头 |
| `sourceUrl` | string | 否 | 来源地址，必须是 `http://` 或 `https://` 开头。一键获取 / 贴 GitHub 链接时由 Hub 自动回填，**手写主题不用填**。只作追溯与署名，Hub 不会拿它检查更新 |
| `license` | string | 否 | 许可证，留空视为 `MIT` |
| `tags` | string[] | 否 | 标签，用于分类检索 |
| `tokensSchemaVersion` | number | 否¹ | tokens 命名表版本，当前为 `2` |
| `tokens` | object | ✅ **至少一个** | 浅色（或唯一）模式的变量 |
| `tokensDark` | object | 否 | 深色模式的覆盖值 |
| `mode` | object | 否 | 深浅色策略 |
| `layoutSchemaVersion` | number | 否¹ | layout 结构版本，当前为 `1` |
| `layout` | object | 否² | 布局偏好 |
| `package` | object | 否³ | 包元数据（见 §6） |

¹ 省略或填 `0` 会自动取当前版本；**填高于当前版本会被拒绝**并提示升级 Hub。
² 省略时全部走默认值，功能完全正常。
³ 手写主题不需要填；由打包工具自动生成。
⁴ `author` 为空时会自动填「未知作者」，不会报错。

### 3.2 `mode` 对象

| 字段 | 类型 | 默认 | 说明 |
| --- | --- | --- | --- |
| `default` | `light` \| `dark` \| `auto` | `auto` | 访客没选过时的初始模式 |
| `allowUserSwitch` | boolean | `true` | 是否显示深浅色切换按钮。设 `false` 则强制跟随 `default` |
| `supportsDark` | boolean | `true` | 设 `false` 表示这套主题只做了浅色，深色切换按钮会消失 |

> **稀疏深色覆盖**：`tokensDark` 里没写的键，自动沿用 `tokens` 的值。
> 所以做深色主题时通常只需要写十几个变量，不用把 101 个重写一遍。

### 3.3 严格性说明（为什么你的编辑器会标红）

解析器用了 `DisallowUnknownFields`：**任何未在本文档出现的字段都会导致整个主题被拒绝**。

比如你把 `secondaryLine` 写成 `secondLine`、把 `--kokoro-font-line-height` 写成 `--kokoro-line-height`，都会直接失败。这是故意的——拼错的键如果被静默忽略，作者会以为生效了，然后花几天找「为什么没变化」。

---

## 4. 第一层：tokens（颜色、字体、圆角）

### 4.1 基本用法

`tokens` 是一个扁平的对象，键是变量名，值是字符串。

```json
"tokens": {
  "--kokoro-color-primary": "#0a7ea4",
  "--kokoro-radius-md": "6px",
  "--kokoro-font-sans": "Georgia, 'Songti SC', serif"
}
```

**只写你要改的。** 未写的键自动使用下表的默认值，主题之间互不影响。

### 4.2 引用其他变量

`tokens` 内部可以互相引用，这是做「一套主色派生出一整套色板」的关键：

```json
"tokens": {
  "--kokoro-color-primary": "#0a7ea4",
  "--kokoro-ring-color": "color-mix(in srgb, var(--kokoro-color-primary) 30%, transparent)",
  "--kokoro-hero-gradient": "linear-gradient(135deg, var(--kokoro-color-primary), var(--kokoro-color-accent))"
}
```

### 4.3 全部 101 个合法变量

以下表格由源码直接导出，与实现严格一致。**表里没有的变量名一律会被拒绝。**

#### 颜色与表面

| 变量 | 类型 | 默认值 |
| --- | --- | --- |
| `--kokoro-color-accent` | 颜色 | `#7c5cff` |
| `--kokoro-color-accent-fg` | 颜色 | `#ffffff` |
| `--kokoro-color-bg` | 颜色 | `#ffffff` |
| `--kokoro-color-bg-subtle` | 颜色 | `#f6f8fa` |
| `--kokoro-color-border` | 颜色 | `#d0d7de` |
| `--kokoro-color-border-strong` | 颜色 | `#8c959f` |
| `--kokoro-color-danger` | 颜色 | `#cf222e` |
| `--kokoro-color-info` | 颜色 | `#0969da` |
| `--kokoro-color-link` | 颜色 | `#0969da` |
| `--kokoro-color-primary` | 颜色 | `#2f6feb` |
| `--kokoro-color-primary-active` | 颜色 | `#2455b8` |
| `--kokoro-color-primary-fg` | 颜色 | `#ffffff` |
| `--kokoro-color-primary-hover` | 颜色 | `#2a61d0` |
| `--kokoro-color-success` | 颜色 | `#1a7f37` |
| `--kokoro-color-surface` | 颜色 | `#ffffff` |
| `--kokoro-color-surface-raised` | 颜色 | `#ffffff` |
| `--kokoro-color-surface-sunken` | 颜色 | `#f6f8fa` |
| `--kokoro-color-text` | 颜色 | `#1f2328` |
| `--kokoro-color-text-faint` | 颜色 | `#818b98` |
| `--kokoro-color-text-inverse` | 颜色 | `#ffffff` |
| `--kokoro-color-text-muted` | 颜色 | `#59636e` |
| `--kokoro-color-warning` | 颜色 | `#bf6a02` |


#### 圆角

| 变量 | 类型 | 默认值 |
| --- | --- | --- |
| `--kokoro-radius-full` | 取值 | `999px` |
| `--kokoro-radius-lg` | 取值 | `12px` |
| `--kokoro-radius-md` | 取值 | `8px` |
| `--kokoro-radius-sm` | 取值 | `4px` |


#### 间距

| 变量 | 类型 | 默认值 |
| --- | --- | --- |
| `--kokoro-space-1` | 取值 | `4px` |
| `--kokoro-space-2` | 取值 | `8px` |
| `--kokoro-space-3` | 取值 | `12px` |
| `--kokoro-space-4` | 取值 | `16px` |
| `--kokoro-space-5` | 取值 | `24px` |
| `--kokoro-space-6` | 取值 | `32px` |
| `--kokoro-space-7` | 取值 | `48px` |
| `--kokoro-space-8` | 取值 | `64px` |


#### 字体

| 变量 | 类型 | 默认值 |
| --- | --- | --- |
| `--kokoro-font-letter-spacing` | 数值 | `0` |
| `--kokoro-font-line-height` | 数值 | `1.55` |
| `--kokoro-font-mono` | 字体栈 | `ui-monospace, 'Cascadia Code', 'JetBrains Mono', Consolas, monospace` |
| `--kokoro-font-sans` | 字体栈 | `system-ui, -apple-system, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif` |
| `--kokoro-font-size-2xl` | 取值 | `26px` |
| `--kokoro-font-size-3xl` | 取值 | `34px` |
| `--kokoro-font-size-lg` | 取值 | `16px` |
| `--kokoro-font-size-md` | 取值 | `14px` |
| `--kokoro-font-size-sm` | 取值 | `13px` |
| `--kokoro-font-size-xl` | 取值 | `20px` |
| `--kokoro-font-size-xs` | 取值 | `12px` |
| `--kokoro-font-weight-bold` | 数值 | `700` |
| `--kokoro-font-weight-medium` | 数值 | `500` |
| `--kokoro-font-weight-normal` | 数值 | `400` |


#### 阴影

| 变量 | 类型 | 默认值 |
| --- | --- | --- |
| `--kokoro-shadow-lg` | 阴影 | `0 16px 40px rgba(31, 35, 40, .16)` |
| `--kokoro-shadow-md` | 阴影 | `0 4px 12px rgba(31, 35, 40, .10)` |
| `--kokoro-shadow-sm` | 阴影 | `0 1px 2px rgba(31, 35, 40, .08)` |


#### 动效

| 变量 | 类型 | 默认值 |
| --- | --- | --- |
| `--kokoro-transition-base` | 时间 | `200ms` |
| `--kokoro-transition-fast` | 时间 | `120ms` |
| `--kokoro-transition-slow` | 时间 | `320ms` |


#### 图表

| 变量 | 类型 | 默认值 |
| --- | --- | --- |
| `--kokoro-chart-area-opacity` | 数值 | `0.18` |
| `--kokoro-chart-axis` | 颜色 | `#8c959f` |
| `--kokoro-chart-bar-radius` | 数值 | `3` |
| `--kokoro-chart-grid` | 颜色 | `#eaeef2` |
| `--kokoro-chart-line-width` | 数值 | `1.75` |
| `--kokoro-chart-point-radius` | 数值 | `2.5` |
| `--kokoro-chart-series-1` | 取值 | `#2f6feb` |
| `--kokoro-chart-series-2` | 取值 | `#7c5cff` |
| `--kokoro-chart-series-3` | 取值 | `#1a7f37` |
| `--kokoro-chart-series-4` | 取值 | `#bf6a02` |
| `--kokoro-chart-series-5` | 取值 | `#cf222e` |
| `--kokoro-chart-series-6` | 取值 | `#0969da` |
| `--kokoro-chart-series-7` | 取值 | `#8250df` |
| `--kokoro-chart-series-8` | 取值 | `#6e7781` |
| `--kokoro-chart-tooltip-bg` | 颜色 | `#1f2328` |
| `--kokoro-chart-tooltip-fg` | 颜色 | `#ffffff` |


#### 状态色

| 变量 | 类型 | 默认值 |
| --- | --- | --- |
| `--kokoro-status-idle` | 颜色 | `#bf6a02` |
| `--kokoro-status-maintenance` | 颜色 | `#0969da` |
| `--kokoro-status-offline` | 颜色 | `#8c959f` |
| `--kokoro-status-online` | 颜色 | `#1a7f37` |


#### 结构与杂项

| 变量 | 类型 | 默认值 |
| --- | --- | --- |
| `--kokoro-backdrop-blur` | 取值 | `0px` |
| `--kokoro-bg-attachment` | 取值 | `fixed` |
| `--kokoro-bg-image` | 背景图 | `none` |
| `--kokoro-bg-repeat` | 取值 | `repeat` |
| `--kokoro-bg-size` | 取值 | `auto` |
| `--kokoro-border-width` | 取值 | `1px` |
| `--kokoro-card-bg` | 取值 | `var(--kokoro-color-surface)` |
| `--kokoro-card-border` | 取值 | `var(--kokoro-color-border)` |
| `--kokoro-card-border-image` | 取值 | `none` |
| `--kokoro-card-padding` | 取值 | `16px` |
| `--kokoro-code-bg` | 取值 | `var(--kokoro-color-surface-sunken)` |
| `--kokoro-content-max-width` | 取值 | `1280px` |
| `--kokoro-easing` | 缓动 | `cubic-bezier(.2,0,0,1)` |
| `--kokoro-footer-bg` | 取值 | `var(--kokoro-color-bg-subtle)` |
| `--kokoro-glow` | 阴影 | `none` |
| `--kokoro-header-bg` | 取值 | `var(--kokoro-color-surface)` |
| `--kokoro-header-fg` | 取值 | `var(--kokoro-color-text)` |
| `--kokoro-header-height` | 取值 | `56px` |
| `--kokoro-hero-gradient` | 渐变 | `linear-gradient(135deg, var(--kokoro-color-primary), var(--kokoro-color-accent))` |
| `--kokoro-hero-overlay` | 颜色 | `rgba(0,0,0,.35)` |
| `--kokoro-opacity-disabled` | 数值 | `0.5` |
| `--kokoro-ring-color` | 颜色 | `#2f6feb59` |
| `--kokoro-ring-width` | 取值 | `3px` |
| `--kokoro-sidebar-width` | 取值 | `240px` |
| `--kokoro-z-dropdown` | 数值 | `1000` |
| `--kokoro-z-modal` | 数值 | `1100` |
| `--kokoro-z-toast` | 数值 | `1200` |

### 4.4 值的形态限制

每个变量有一个固定的「类型」，值的写法必须对得上：

| 类型 | 允许的写法 | 例子 |
| --- | --- | --- |
| 颜色 | `#rgb` `#rgba` `#rrggbb` `#rrggbbaa`、`rgb()`、`rgba()`、`hsl()`、`hsla()`、`color-mix()`、`var(--kokoro-*)`、`transparent`、`currentColor`、`inherit`、`white`、`black` 等少量具名色 | `#0a7ea4`、`rgb(10,126,164,.8)` |
| 长度 | `0`、`auto`、`none`、带单位的数字、`calc()`、`min()`、`max()`、`clamp()`、`var()` | `12px`、`2rem`、`40%`、`clamp(280px, 30vw, 420px)` |
| 数值 | 纯数字，可带负号与小数 | `1.75`、`0.18`、`700` |
| 时间 | `数字 + ms` 或 `数字 + s` | `120ms`、`0.3s` |
| 字体栈 | 逗号分隔的字体名，可用引号 | `'Inter', system-ui, sans-serif` |
| 缓动 | 含括号的缓动函数 | `cubic-bezier(.2,0,0,1)`、`ease-in-out` |
| 阴影 | `none` 或含数字的 `box-shadow` 值 | `0 8px 24px rgba(0,0,0,.28)` |
| 渐变 | 含 `gradient(` | `linear-gradient(180deg, #123, #456)` |
| 背景图 | `none` / `url(...)` / `data:image/...` | 见 §7 |

**所有类型都绝对禁止出现的字符**：`;` `{` `}` `<` `>` `\` `@` 以及 `/*` `*/`。

原因见 §7——这些字符能终止声明、插入新规则或开新标签。

---

## 5. 第二层：layout（布局与形态）

layout 决定「页面长什么样」，而不只是「什么颜色」。它是主题可玩性的主要来源。

**所有取值都是封闭枚举**，写错会立刻报错（不会静默回落）。

### 5.1 `shell` —— 整站骨架

| 字段 | 取值 | 默认 | 说明 |
| --- | --- | --- | --- |
| `contentMaxWidth` | 长度 | `1280px` | 内容区最大宽度 |
| `contentAlign` | `center` \| `left` | `center` | 内容区居中或左对齐 |
| `header.variant` | `solid` \| `transparent` \| `blur` \| `bordered` | `solid` | 顶栏形态。`blur` 用毛玻璃，`bordered` 是底部一条主色线 |
| `header.sticky` | boolean | `true` | 顶栏是否吸顶 |
| `header.showSearch` | boolean | `true` | 是否显示搜索框 |
| `header.showModeSwitch` | boolean | `true` | 是否显示深浅色切换 |
| `footer.variant` | `simple` \| `columns` \| `minimal` | `simple` | 页脚形态 |

### 5.2 `home` —— 首页

| 字段 | 取值 | 默认 | 说明 |
| --- | --- | --- | --- |
| `hero.enabled` | boolean | `false` | 是否显示首页 Hero 大标题区 |
| `hero.variant` | `plain` \| `gradient` \| `image` \| `split` | `plain` | Hero 形态。`gradient` 用 `--kokoro-hero-gradient` |
| `hero.align` | `left` \| `center` | `left` | 文字对齐 |
| `hero.subtitle` | string | 空 | 副标题，≤200 字符 |
| `hero.showStats` | boolean | `true` | 是否显示节点数/带宽等统计 |
| `list.mode` | `card` \| `table` \| `compact` \| `map` | `card` | **节点列表形态**，差异最大的一项 |
| `list.allowUserSwitch` | boolean | `true` | 访客能否自己切换列表形态 |
| `list.groupBy` | `none` \| `status` \| `region` \| `tag` \| `provider` | `none` | 列表分组方式 |
| `list.card.minWidth` | 长度 | `300px` | 卡片最小宽度（影响每行几个） |
| `list.card.gap` | 长度 | `24px` | 卡片间距 |
| `list.card.border` | boolean | `true` | 卡片是否描边 |
| `list.card.shadow` | `none` \| `sm` \| `md` \| `lg` \| `glow` | `sm` | 卡片阴影 |
| `list.card.hoverLift` | boolean | `true` | 鼠标悬停时卡片上浮 |
| `list.table.density` | `compact` \| `comfortable` | `comfortable` | 表格行高 |
| `list.table.striped` | boolean | `false` | 斑马纹 |
| `list.table.rowHover` | boolean | `true` | 行悬停高亮 |
| `list.table.showUnit` | boolean | `true` | 是否显示单位（GB/MB…） |
| `list.compact.rowHeight` | 长度 | `44px` | 紧凑模式行高 |
| `list.compact.dividers` | boolean | `true` | 行间分隔线 |
| `list.compact.showFlag` | boolean | `true` | 是否显示国旗 |
| `list.compact.inlineBars` | boolean | `true` | 是否把资源条内联到行里 |

### 5.3 `detail` —— 节点详情页

| 字段 | 取值 | 默认 | 说明 |
| --- | --- | --- | --- |
| `header.variant` | `hero` \| `compact` \| `none` | `compact` | 详情页头部。`hero` 是大图头，`none` 是完全无头图 |
| `header.height` | 长度 | `320px` | 头部高度 |
| `header.showTags` | boolean | `true` | 是否显示标签 |
| `header.secondaryLine` | `location` \| `specs` \| `uptime` \| `none` | `location` | 头部第二行显示什么 |
| `sectionGap` | 长度 | `32px` | 区块间距 |
| `introFontSize` | 长度 | — | 站长简介字号 |
| `commentsCollapsed` | boolean | `false` | 评论区默认折叠 |

### 5.4 `charts` —— 图表

| 字段 | 取值 | 默认 | 说明 |
| --- | --- | --- | --- |
| `type` | `line` \| `area` \| `bar` | `area` | 图表类型 |
| `grid` | boolean | `true` | 是否画网格线 |
| `legend` | boolean | `true` | 是否显示图例 |
| `axisLabel` | boolean | `true` | 是否显示坐标轴标签 |
| `smooth` | boolean | `true` | 折线是否平滑 |
| `tooltip` | `axis` \| `item` \| `none` | `axis` | 提示框行为 |
| `height` | 长度 | `200px` | 首页图表高度 |
| `heightDetail` | 长度 | `300px` | 详情页图表高度 |

### 5.5 `misc` —— 零碎风格

| 字段 | 取值 | 默认 | 说明 |
| --- | --- | --- | --- |
| `statusDotStyle` | `solid` \| `ring` \| `glow` \| `none` | `solid` | 在线状态点样式 |
| `usageBarStyle` | `solid` \| `gradient` \| `segmented` \| `none` | `solid` | 资源占用条样式 |
| `tagStyle` | `pill` \| `square` \| `text` | `pill` | 标签样式 |
| `badgeStyle` | `soft` \| `outline` \| `solid` | `soft` | 徽章样式 |
| `showUptime` | boolean | `true` | 是否显示在线时长 |
| `numberFormat` | `si` \| `plain` \| `percent` | `si` | 数字格式（1.2K / 1200 / 40%） |
| `dateFormat` | `relative` \| `absolute` | `relative` | 时间格式 |

### 5.6 内置默认主题与取值参考

当前**只维护一套内置主题**：`kokoro.daylight`（晨白）。

| 主题 | 列表 | 图表 | 顶栏 | 详情页头 | 模式 |
| --- | --- | --- | --- | --- | --- |
| `kokoro.daylight` 晨白 | card | area | solid | compact | 浅/深 |

这是刻意的：界面方向还没定稿，多写几套只是让「等定了再写新的」
变成「等定了再删三套」。**主题系统本身的能力不打折**——下面每个枚举
取值都能用，文档示例 `docs/themes/example-terminal.json`（紧凑 + 条形图
+ 描边顶栏）和 `example-paper.json`（透明顶栏 + hero 详情页头）就是
拿来覆盖其余取值的，可以直接拿去改。

导出它当作起点：`GET /theme-export/kokoro.daylight`

以后定了稿要加主题，往 `internal/theme/builtin/` 下加目录、
并在 `internal/theme/theme_test.go` 的 `want` 列表里登记即可。

---

## 6. 标准主题包 `.kokoro-theme`

### 6.1 什么时候需要包

只用颜色和布局 → 裸 `theme.json` 就够。
需要**自定义字体、背景图、预览图** → 需要包（JSON 装不下二进制）。

### 6.2 包结构

`.kokoro-theme` 就是个 zip，**只允许**这几个条目：

```
theme.json          必需   清单本体
preview.png         可选   预览图
LICENSE             可选   许可证全文
README.md           可选   说明文档
assets/*            可选   附加资源（字体、图片…）
```

**任何其它条目都会被拒绝**，包括但不限于：
- `../../etc/passwd`（zip-slip 路径穿越）
- `js/evil.js`、`css/overlay.css`（白名单外的目录）
- 符号链接
- 反斜杠路径、盘符路径（`C:\...`）、UNC 路径

### 6.3 硬性限制

| 项目 | 上限 |
| --- | --- |
| 包体积 | 8 MB |
| 解压后单文件 | 2 MB |
| 解压后总大小 | 12 MB |
| 条目数 | 64 |
| 压缩比 | 100:1 |

### 6.4 完整性校验

`theme.json` 里可以声明 `package` 段：

```json
"package": {
  "format": "kokoro-theme",
  "files": ["assets/bg.png", "assets/font.woff2"],
  "sha256": "3f2a…（64 位十六进制）"
}
```

- `sha256` 的算法：把 `assets/` 下所有文件**按路径名排序**，对每个文件依次写入 `路径\0字节数\0` 和文件内容，再整体做一次 SHA-256（不包含 `theme.json` 自身）。
- 声明与实际不一致 → **拒绝导入**。
- 打包工具（`theme.BuildPackage`）会自动算好并回填，你手写时一般不用管。

> **关于签名**：`package.signed` 字段目前只记录、不校验。
> 因为还没有可信的公钥分发渠道，装一个「假装校验签名」的字段比没有更危险——它会给人虚假的安全感。看到 `signed: true` 时，请以「未经验证」对待。

### 6.5 打包与安装

**打包**（用 Go 写主题工具时）：

```go
raw, err := theme.BuildPackage(manifest, assets, preview, license, readme)
```

**安装**：后台 `/admin/themes` → 上传 `.kokoro-theme` 文件，或直接粘贴 `theme.json`。

**导出**：任意站点的 `/theme-bundle/<id>` 返回该主题的完整包（公开、无鉴权）。

---

## 7. 安全边界（为什么某些写法会被拒绝）

这一节解释每条限制背后的攻击面。理解了就不会觉得限制莫名其妙。

### 7.1 拒绝注入字符

`;` `{` `}` `<` `>` `\` `@` `/*` `*/` 在 token 值里一律拒绝。

```json
"--kokoro-color-primary": "#fff; background: url(https://evil.tld/x)"
```

如果放行，这条声明会终止并注入一条新规则。有了主题一键获取，任何人都能把这样一份主题塞进别人的站。

### 7.2 拒绝远程资源

`url()` 只允许两种来源：
- `data:image/...`（内联图片）
- `/_theme-assets/`（Hub 本地路径）

```json
"--kokoro-bg-image": "url(https://fonts.googleapis.com/…)"    ❌
"--kokoro-bg-image": "url(/_theme-assets/bg.png)"            ✅
```

理由：主题不该让访客的浏览器去别处拉东西（隐私泄露 + 对方站点挂了整站跟着花屏）。同理 `@import` 完全禁止。

写 `/_theme-assets/` 时**只写包内相对路径即可**，Hub 渲染时会自动补上主题 ID：

```json
"--kokoro-bg-image": "url(/_theme-assets/bg.png)"           ✅ 会自动变成 /_theme-assets/<你的主题ID>/bg.png
"--kokoro-bg-image": "url(/_theme-assets/<主题ID>/bg.png)"  ✅ 写全了也不会被重复拼接
```

这些资源只在你把主题**打成 `.kokoro-theme` 包**时才存在（资源字节在包里）。
只交一份 `theme.json` 的话，`/_theme-assets/` 下面什么都取不到，背景图会 404。

Hub 只肯下发这些类型，其余（**尤其 `.svg`**）一律 404：

| 类别 | 扩展名 |
| --- | --- |
| 图片 | `.png` `.jpg` `.jpeg` `.webp` `.gif` `.avif` `.bmp` `.ico` |
| 字体 | `.woff2` `.woff` `.ttf` `.otf` |

`.svg` 被单独拎出来禁止，是因为它是唯一能在「图片」位置执行脚本的格式
（`<svg onload=…>`、内嵌 `<script>`），而主题包来自第三方，不能赌它善意。
要矢量图就转成 `.png`/`.webp`。

### 7.3 拒绝未知变量名

只能写 `tokens` 表里的 101 个名字。不能通过 `--body-bg: red` 之类去污染站点其他样式。

### 7.4 拒绝官方 ID 前缀

`kokoro.` 前缀是官方保留的。内置主题随二进制分发、行为可预期；让社区主题也用这个前缀，访客看到它就会默认信任——身份体系就废了。

```json
"id": "kokoro.mytema"    ❌ 被拒绝
"id": "mytheme.aurora"   ✅
```

### 7.5 一键获取时：包被拒 ≠ 退回清单

如果对方的**包存在但没通过校验**，Hub 会**直接中止导入**，不会退而去装同一站点的清单。

理由：否则攻击者只要把包做坏、把清单做干净，就能绕过包校验——等于给了一个免费重试机会。

### 7.6 摘要与来源

后台会显示每套自定义主题的 `sha256` 与来源地址（`source_url`）。这是唯一的追溯线索：出问题时能确认「装的是哪一份」。

---

## 8. 一键获取主题

### 8.1 使用

后台 `/admin/themes` → 「一键获取别人的皮肤」→ 填地址。支持三种地址：

- **GitHub 链接**（推荐）：`https://github.com/user/repo`、
  子目录 `https://github.com/user/repo/tree/main/themes/dark`、
  或单文件 `https://github.com/user/repo/blob/main/theme.json`
- 对方站点地址：`https://vps.example.com`
- 清单完整地址：`https://vps.example.com/theme.json`

抓下来就**存在你自己的面板里**，此后与来源断开关联：对方改了他的主题，
你这边不会跟着变（来源地址会记在清单的 `sourceUrl` 里，只作追溯）。

**填站点地址时**，Hub 的取件顺序：

1. `GET <站点>/theme.json` —— 拿清单，同时知道对方主题的 ID
2. `GET <站点>/theme-bundle/<id>` —— 尝试拿完整包
   - 成功 → 用包导入（含资源，带校验和）
   - 对方没有这个端点（404） → 退回只用清单
   - **拿到了但校验没过 → 直接中止**，报明确错误

**填 GitHub 链接时**，Hub 把页面地址翻译成 raw 内容地址
（`https://raw.githubusercontent.com/...`），然后：

1. 在目标目录找 `theme.json` —— 找到就用清单导入
2. 没有就找 `theme.kokoro-theme` —— 找到就装包（含资源）
3. 都没有 → 报「该目录下既没有 theme.json，也没有主题包」

### 8.2 GitHub 仓库怎么放主题

最省事的两种布局，任选其一：

```
user/repo
├── theme.json          ← 仓库根直接放清单
└── assets/             ← 可选：背景图、字体（随包一起走需要打成包）
```

或者直接把打包好的主题放在仓库里：

```
user/repo
└── theme.kokoro-theme  ← 用「导出」按钮拿到的那个包，直接提交进去
```

仓库根是默认分支即可（Hub 用 `HEAD` 别名指向默认分支）；
主题放在子目录时，贴 `.../tree/<分支>/<子目录>` 形式的链接。

### 8.3 对方站点需要暴露什么

任何 Kokoro 站点**默认就暴露**这三个端点，无需配置：

| 端点 | 内容 | 鉴权 |
| --- | --- | --- |
| `/theme.json` | 当前生效主题的清单 | 无（公开） |
| `/theme-bundle/<id>` | 指定主题的完整包 | 无（公开） |
| `/theme-export/<id>` | 下载清单文件 | 无（公开） |

这是刻意的：主题的意义就是被人抄。如果你想给自己的站加「禁止被获取」，需要在 Nginx 层拦这些路径。

### 8.4 抓取限制

- 只允许 `http://` / `https://`
- 单次超时 12 秒
- 最多 3 次重定向，**且每一跳的目标都重新校验**
- 体积上限 8MB
- **不会携带本地管理员 cookie 去请求对方**——你的登录态不会泄漏给别人

### 8.5 内网地址一律不抓（SSRF 防护）

「一键获取」本质是让 Hub 按一个地址发请求，不设防就等于开放内网探测。
所以目标 IP 会被检查：

| 地址段 | 结果 | 为什么 |
| --- | --- | --- |
| 公网可路由地址 | 放行 | 正常目标 |
| `127.0.0.0/8`、`::1` | **默认拒绝**，仅白名单主机名放行 | 回环不是「你的站」，而是**这台机器上的所有服务**。放行等于让一个 SSRF 读遍本机的数据库、Docker 映射端口和本地管理面板 |
| `10/8`、`172.16/12`、`192.168/16` | 拒绝 | RFC1918 私有段 |
| `169.254/16` | 拒绝 | 含云厂商元数据 `169.254.169.254` |
| `100.64/10` | 拒绝 | CGNAT 共享段，指向一片用户的内网设备 |
| `fc00::/7`、`fe80::/10` | 拒绝 | IPv6 ULA / 链路本地 |
| 组播、`0.0.0.0`、其它保留段 | 拒绝 | 无正当用途 |

防护分两层，缺一不可：

1. **URL 校验** —— 拒绝非 http(s) 与字面量内网 IP；
2. **拨号校验** —— 真正建连时再解析一次域名、检查 IP，
   并把**已验证的那个 IP** 直接交给连接器。

第 2 层是防 DNS rebinding 的：只在第 1 层检查的话，
攻击者可以让域名在校验时解析到公网、连接时改到 `127.0.0.1`。
重定向也逐跳走同样两道校验，所以「先正常响应、再 302 到内网」这条
路径同样被堵死。

#### 回环白名单（抓自己站）

反代架构下（Nginx 在前、Go 监听 `127.0.0.1`），「抓自己站的主题」是常见需求。
为了不放开整个回环段，改成**按主机名精确放行**。两种来源：

1. **启动参数 `--domain`** —— 本站自己的域名自动进白名单。
   这是最常见的场景，站长不需要额外配置。
2. **后台设置项 `theme_fetch_hosts`** —— 逗号分隔的额外域名，
   用于「本机还有另一个域名指向自己」之类的情形。

匹配规则是**精确匹配**，不做后缀或子域名匹配：

| 白名单 | 目标 | 结果 |
| --- | --- | --- |
| `mjfuns.lat` | `mjfuns.lat` | 放行 |
| `mjfuns.lat` | `MJfuns.Lat` | 放行（大小写不敏感） |
| `mjfuns.lat` | `evil.mjfuns.lat` | **拒绝**（不是同一个名字） |
| `mjfuns.lat` | `mjfuns.lat.evil.com` | **拒绝** |

白名单同样会被校验：粘进来一整条 URL（`https://a.com/x`）、带端口
（`127.0.0.1:8799`）、多余逗号都会被规范化；但像 `http`、`abc` 这种
既不含点、也不是 IP/localhost 的碎片会被丢弃——错收一条等于放开回环，
宁可漏收也不能错收。

---

## 9. 常见错误与修正

### 9.1 变量名拼错

```json
"--kokoro-line-height": "1.6"          ❌
"--kokoro-font-line-height": "1.6"     ✅
```

报错：`未知变量 "--kokoro-line-height"`

**修正**：对照 §4.3 的表，一个字符都别差。

### 9.2 值里带了分号

```json
"--kokoro-shadow-md": "0 4px 12px rgba(0,0,0,.1); color: red"   ❌
"--kokoro-shadow-md": "0 4px 12px rgba(0,0,0,.1)"                ✅
```

报错：`值里不允许出现 ";"`

**修正**：一个变量只能有一个值。需要组合效果就拆成多个变量。

### 9.3 枚举值写错

```json
"layout": { "home": { "list": { "mode": "grid" } } }    ❌
"layout": { "home": { "list": { "mode": "card" } } }    ✅
```

报错：`layout: home.list.mode 的取值 "grid" 非法`

**修正**：对照 §5 的枚举表。

### 9.4 字段名写错

```json
"layout": { "detail": { "header": { "secondLine": "specs" } } }   ❌
"layout": { "detail": { "header": { "secondaryLine": "specs" } } } ✅
```

报错：`主题 JSON 解析失败: json: unknown field "secondLine"`

**修正**：§3 的表。

### 9.5 数字型变量带了单位

```json
"--kokoro-chart-line-width": "1.75px"   ❌ 这是纯数值，不是长度
"--kokoro-chart-line-width": "1.75"     ✅
```

报错：`不是合法数值`

**修正**：看清 §4.4 的类型表，`chart-*` 里的几何量都是无单位数值。

### 9.6 深色主题没生效

大概率是 `tokensDark` 里写错了键（被当作未知变量拒绝），或者 `mode.supportsDark` 是 `false`。

**检查**：
1. `tokensDark` 里的每个键都在 §4.3 表里？
2. `"mode": { "supportsDark": true }`？
3. `tokensDark` 非空时才会渲染深色块——只写 `mode.default: "dark"` 但不写 `tokensDark`，会得到一个深色下难读的站点。

### 9.7 `id` 含大写或中文

```json
"id": "MyTheme"      ❌
"id": "我的主题"      ❌
"id": "mytheme-aurora" ✅
```

报错：`主题 id 只能含小写字母、数字、. _ -`

**修正**：`id` 是机器标识，用小写短横线。中文放 `name`。

### 9.8 从零写时忘了必填项

最常见的三个：`id`、`name`、`version`。

报错：`主题缺少 id` / `主题缺少 name` / `主题缺少 version`

**修正**：见 §3.1，`tokens` 也必须至少有一个键。

---

## 10. 从零写一套主题

### 10.1 推荐流程

1. 挑一套最接近的内置主题：`GET /theme-export/kokoro.daylight`
2. 改 `id`、`name`、`author`（**必须去掉 `kokoro.` 前缀**）
3. 逐组替换 `tokens` 里的颜色
4. 想换形态就改 `layout`（对照 §5.6，可参考 `docs/themes/example-*.json`）
5. 导入后台 → `/admin/themes` → 粘贴 → 「校验并导入」
6. 不满意就改 JSON 再导一次（同 ID 会覆盖）

### 10.2 配色建议

一套能用的主题至少要定这几个，它们互相牵制：

```
--kokoro-color-bg          页面底色
--kokoro-color-surface     卡片面
--kokoro-color-text        正文
--kokoro-color-text-muted  次要文字
--kokoro-color-border      描边
--kokoro-color-primary     主色（按钮、链接、强调）
--kokoro-color-primary-fg  主色上的文字色
--kokoro-color-success/warning/danger   状态色
```

配比参考：`bg` 和 `surface` 拉开一点差异（有层次），`text` 和 `text-muted` 对比度 ≥ 4.5:1，`primary` 和 `primary-fg` 对比度 ≥ 4.5:1。

### 10.3 用 AI 生成

把 `docs/themes/AI-PROMPT.md` 的内容整段发给 AI，把你的需求说清楚（"深色、极简、等宽字体、列表用紧凑行"），它就能产出一份能直接导入的主题。

那份提示词里已经写清了所有限制，比让 AI 自由发挥靠谱得多。

---

## 附录 A：校验顺序

导入时按这个顺序检查，前一步失败就不会走到后面：

1. 包结构（如果传的是包）：魔数 → 体积 → 条目数 → 路径安全 → 压缩比 → 逐个解压
2. `theme.json` 存在且能解析
3. `package` 声明与实际内容一致（校验和）
4. `schemaVersion` / `tokensSchemaVersion` / `layoutSchemaVersion` 不高于当前
5. 身份字段：`id`（字符集 + 非官方前缀）、`name`、`version`、`homepage`
6. `tokens` 非空，且每个键名合法、每个值形态正确
7. `tokensDark` 同上（若有）
8. `layout` 每个枚举值合法、每个长度值合法

## 附录 B：相关端点速查

| 端点 | 方法 | 鉴权 | 说明 |
| --- | --- | --- | --- |
| `/theme.json` | GET | 无 | 当前生效主题清单 |
| `/theme-export/<id>` | GET | 无 | 下载清单文件 |
| `/theme-bundle/<id>` | GET | 无 | 下载完整包 |
| `/_theme-assets/<id>/<name>` | GET | 无 | 主题包里的资源（背景图 / 字体）。只下发白名单类型，**SVG 一律 404** |
| `/pick/<id>` | GET | 无 | **访客**自选主题（只写自己的 cookie） |
| `/pick/default` | GET | 无 | 访客恢复站点默认 |
| `/theme/<id>` | GET | **管理员** | 切换站点主题（影响所有人） |
| `/admin/themes` | GET | **管理员** | 主题管理页 |
| `/admin/themes/import` | POST | **管理员** | 导入（粘贴 JSON 或上传包） |
| `/admin/themes/grab` | POST | **管理员** | 一键获取（站点地址 / 清单地址 / GitHub 链接） |
| `/admin/themes/delete` | POST | **管理员** | 删除自定义主题 |

### 两种"切换主题"的区别

| | `/theme/<id>` | `/pick/<id>` |
| --- | --- | --- |
| 改谁 | 站点默认主题 | 只有当前访客 |
| 影响别人 | 是，所有人立刻变 | 否 |
| 需要登录 | 管理员 | 无 |
| 落地方式 | `settings.site_theme` + 内存缓存 | `k_theme` cookie |
| 出现在 UI | 后台「启用这套」 | 页头右上角的皮肤下拉（`/pick/<id>`） |

站台上装了几套主题，访客就能在首页切几套（只换自己浏览器里的外观）。
自己看够了点「恢复默认」就回到站长设定的样子。

## 附录 C：本文档与实现的关系

本规范与 `internal/theme` 包一一对应，并由测试守护：

- `TestDocMinimalExampleIsValid` —— `docs/themes/minimal.json` 必须始终合法
- `TestBuiltinManifestsCanBePackaged` —— 内置主题必须始终能打成包并解回来
- `TestPackageRejects*` —— 每一条安全限制都有一个真实的攻击样本测试

**如果 Hub 升级导致不兼容**（比如 tokens 命名表加了新键），以 Hub 的报错信息为准，并更新本文档。