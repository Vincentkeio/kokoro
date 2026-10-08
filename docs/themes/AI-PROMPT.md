# AI 主题生成提示词

> 用法：把下面 `======` 之间的全部内容整段复制给你的 AI，然后在**末尾**补一句你的需求（见 §「怎么用」）。
>
> 这份提示词里已经写死了所有硬约束（合法变量名、值类型、枚举值、安全禁区）。
> AI 拿着它产出的主题，能直接通过 Hub 校验，不需要你再手工纠错。

======

你是一名 Kokoro 探针面板的主题（皮肤）工程师。我要你写一份 Kokoro 主题文件 `theme.json`。

## 一、这是什么

Kokoro 是一个自托管的服务器探针面板（类似 nezha），有节点列表、节点详情页（像博客一样展示小鸡）、主机仪表盘、管理员后台。

主题是**一个纯 JSON 文件**，它只能做两件事：

1. **`tokens`**：设置 101 个 CSS 变量的取值（颜色、字体、圆角、间距、阴影、图表色等）。
2. **`layout`**：从封闭枚举里选一些布局偏好（列表形态、顶栏形态、图表类型、详情页头部形态等）。

## 二、硬性限制（违反任何一条都会被 Hub 直接拒绝，主题装不上）

### 1. 不能写这些

- 不能有 `styles` / `css` / `scripts` / `js` / `html` / `overrides` 等字段——**一个都没有**。
- 不能写任意 CSS 选择器。主题只能设置变量，不能选择元素。
- 不能写 JavaScript。
- 不能引用远程资源：任何 `url(https://…)`、`url(http://…)`、`@import` 都会被拒绝。
- 不能用 `--kokoro-` 之外的变量名。
- 不能出现这些字符（在任何值里）：`;` `{` `}` `<` `>` `\` `@`，也不能有 `/*` 或 `*/`。

### 2. `id` 的要求

- 3–64 字符，只能含小写字母 `a-z`、数字、`.`、`_`、`-`。
- **绝对不能以 `kokoro.` 开头**（官方保留前缀）。
- 中文、英文大写、空格都不能有。中文放 `name` 字段。

### 3. 必填字段

`id`、`name`、`version`、`tokens`（至少一个键）。`layout` 可以省略（走默认）。

### 4. 多余字段会被拒绝

解析器禁止未知字段。字段名拼错会导致整个主题装不上。只能用下面「字段表」里列出的名字。

## 三、字段表

### 顶层

| 字段 | 类型 | 必填 | 说明 |
| --- | --- | --- | --- |
| `schemaVersion` | number | | 填 `1` |
| `id` | string | ✅ | 见上面「id 的要求」 |
| `name` | string | ✅ | 中文显示名 |
| `version` | string | ✅ | 如 `1.0.0` |
| `description` | string | | 一句话描述 |
| `author` | string | | 作者名 |
| `homepage` | string | | 必须 http(s) 开头 |
| `license` | string | | 如 `MIT` |
| `tags` | string[] | | 标签 |
| `tokensSchemaVersion` | number | | 填 `2` |
| `tokens` | object | ✅ | 变量表，见下 |
| `tokensDark` | object | | 深色模式的**覆盖**值，没写的键沿用 `tokens` |
| `mode` | object | | 深浅色策略 |
| `layoutSchemaVersion` | number | | 填 `1` |
| `layout` | object | | 布局偏好 |

### `mode`

| 字段 | 取值 | 默认 |
| --- | --- | --- |
| `default` | `light` / `dark` / `auto` | `auto` |
| `allowUserSwitch` | `true` / `false` | `true` |
| `supportsDark` | `true` / `false` | `true` |

### `layout`（所有取值都是封闭枚举，写错立刻报错）

**`layout.shell`**

| 字段 | 取值 | 默认 |
| --- | --- | --- |
| `contentMaxWidth` | 长度，如 `1280px` | `1280px` |
| `contentAlign` | `center` / `left` | `center` |
| `header.variant` | `solid` / `transparent` / `blur` / `bordered` | `solid` |
| `header.sticky` | `true` / `false` | `true` |
| `header.showSearch` | `true` / `false` | `true` |
| `header.showModeSwitch` | `true` / `false` | `true` |
| `footer.variant` | `simple` / `columns` / `minimal` | `simple` |

**`layout.home`**

| 字段 | 取值 | 默认 |
| --- | --- | --- |
| `hero.enabled` | `true` / `false` | `false` |
| `hero.variant` | `plain` / `gradient` / `image` / `split` | `plain` |
| `hero.align` | `left` / `center` | `left` |
| `hero.subtitle` | string（≤200 字符） | 空 |
| `hero.showStats` | `true` / `false` | `true` |
| `list.mode` | `card` / `table` / `compact` / `map` | `card` |
| `list.allowUserSwitch` | `true` / `false` | `true` |
| `list.groupBy` | `none` / `status` / `region` / `tag` / `provider` | `none` |
| `list.card.minWidth` | 长度 | `300px` |
| `list.card.gap` | 长度 | `24px` |
| `list.card.border` | `true` / `false` | `true` |
| `list.card.shadow` | `none` / `sm` / `md` / `lg` / `glow` | `sm` |
| `list.card.hoverLift` | `true` / `false` | `true` |
| `list.table.density` | `compact` / `comfortable` | `comfortable` |
| `list.table.striped` | `true` / `false` | `false` |
| `list.table.rowHover` | `true` / `false` | `true` |
| `list.table.showUnit` | `true` / `false` | `true` |
| `list.compact.rowHeight` | 长度 | `44px` |
| `list.compact.dividers` | `true` / `false` | `true` |
| `list.compact.showFlag` | `true` / `false` | `true` |
| `list.compact.inlineBars` | `true` / `false` | `true` |

**`layout.detail`**

| 字段 | 取值 | 默认 |
| --- | --- | --- |
| `header.variant` | `hero` / `compact` / `none` | `compact` |
| `header.height` | 长度 | `320px` |
| `header.showTags` | `true` / `false` | `true` |
| `header.secondaryLine` | `location` / `specs` / `uptime` / `none` | `location` |
| `sectionGap` | 长度 | `32px` |
| `introFontSize` | 长度 | — |
| `commentsCollapsed` | `true` / `false` | `false` |

**`layout.charts`**

| 字段 | 取值 | 默认 |
| --- | --- | --- |
| `type` | `line` / `area` / `bar` | `area` |
| `grid` | `true` / `false` | `true` |
| `legend` | `true` / `false` | `true` |
| `axisLabel` | `true` / `false` | `true` |
| `smooth` | `true` / `false` | `true` |
| `tooltip` | `axis` / `item` / `none` | `axis` |
| `height` | 长度 | `200px` |
| `heightDetail` | 长度 | `300px` |

**`layout.misc`**

| 字段 | 取值 | 默认 |
| --- | --- | --- |
| `statusDotStyle` | `solid` / `ring` / `glow` / `none` | `solid` |
| `usageBarStyle` | `solid` / `gradient` / `segmented` / `none` | `solid` |
| `tagStyle` | `pill` / `square` / `text` | `pill` |
| `badgeStyle` | `soft` / `outline` / `solid` | `soft` |
| `showUptime` | `true` / `false` | `true` |
| `numberFormat` | `si` / `plain` / `percent` | `si` |
| `dateFormat` | `relative` / `absolute` | `relative` |

## 四、tokens 的值类型（每种变量能接受什么写法）

| 类型 | 允许的写法 | 例子 |
| --- | --- | --- |
| 颜色 | `#rgb` `#rgba` `#rrggbb` `#rrggbbaa`、`rgb()`、`rgba()`、`hsl()`、`hsla()`、`color-mix()`、`var(--kokoro-*)`、`transparent`、`currentColor`、`inherit`、`white`、`black`、`gray`、`silver`、`navy`、`teal`、`olive`、`purple`、`maroon` | `#0a7ea4`、`rgba(10,126,164,.8)` |
| 长度 | `0`、`auto`、`none`、带单位的数字、`calc()`、`min()`、`max()`、`clamp()`、`var()` | `12px`、`2rem`、`40%`、`clamp(280px,30vw,420px)` |
| 数值 | 纯数字（可带负号小数），**不带单位** | `1.75`、`0.18`、`700` |
| 时间 | 数字 + `ms` 或 `s` | `120ms`、`0.3s` |
| 字体栈 | 逗号分隔的字体名，可用引号；**不能有 `url()` 或 `@`** | `'Inter', system-ui, sans-serif` |
| 缓动 | 含括号的缓动函数 | `cubic-bezier(.2,0,0,1)`、`ease-in-out` |
| 阴影 | `none` 或含数字的 shadow 值 | `0 8px 24px rgba(0,0,0,.28)` |
| 渐变 | 含 `gradient(` | `linear-gradient(180deg,#123,#456)` |
| 背景图 | `none` / `url(...)` / `data:image/...`；`url()` 里**只允许** `data:image/` 或含 `/_theme-assets/` | `none` |

**特别注意**：`--kokoro-chart-line-width`、`--kokoro-chart-point-radius`、`--kokoro-chart-bar-radius`、`--kokoro-chart-area-opacity`、`--kokoro-font-weight-*`、`--kokoro-z-*`、`--kokoro-opacity-disabled`、`--kokoro-font-letter-spacing`、行高 `--kokoro-font-line-height` 都是**纯数值，不带单位**。写成 `1.75px` 或 `1.55rem` 会被拒绝。

## 五、完整合法变量清单（只能从这 101 个里选）

### 颜色

```
--kokoro-color-primary          --kokoro-color-primary-hover    --kokoro-color-primary-active
--kokoro-color-primary-fg       --kokoro-color-accent           --kokoro-color-accent-fg
--kokoro-color-success          --kokoro-color-warning         --kokoro-color-danger
--kokoro-color-info             --kokoro-color-bg               --kokoro-color-bg-subtle
--kokoro-color-surface          --kokoro-color-surface-raised   --kokoro-color-surface-sunken
--kokoro-color-border           --kokoro-color-border-strong    --kokoro-color-text
--kokoro-color-text-muted       --kokoro-color-text-faint       --kokoro-color-text-inverse
--kokoro-color-link
```

### 圆角

```
--kokoro-radius-sm   --kokoro-radius-md   --kokoro-radius-lg   --kokoro-radius-full
```

### 间距

```
--kokoro-space-1  --kokoro-space-2  --kokoro-space-3  --kokoro-space-4
--kokoro-space-5  --kokoro-space-6  --kokoro-space-7  --kokoro-space-8
```

### 字体

```
--kokoro-font-sans            --kokoro-font-mono
--kokoro-font-size-xs         --kokoro-font-size-sm         --kokoro-font-size-md
--kokoro-font-size-lg         --kokoro-font-size-xl         --kokoro-font-size-2xl
--kokoro-font-size-3xl
--kokoro-font-weight-normal   --kokoro-font-weight-medium   --kokoro-font-weight-bold
--kokoro-font-line-height     --kokoro-font-letter-spacing
```

### 阴影 / 动效

```
--kokoro-shadow-sm   --kokoro-shadow-md   --kokoro-shadow-lg   --kokoro-glow
--kokoro-transition-fast   --kokoro-transition-base   --kokoro-transition-slow
--kokoro-easing
```

### 图表

```
--kokoro-chart-series-1 .. --kokoro-chart-series-8
--kokoro-chart-grid      --kokoro-chart-axis
--kokoro-chart-tooltip-bg  --kokoro-chart-tooltip-fg
--kokoro-chart-area-opacity  --kokoro-chart-line-width
--kokoro-chart-point-radius  --kokoro-chart-bar-radius
```

### 状态色

```
--kokoro-status-online   --kokoro-status-offline   --kokoro-status-idle   --kokoro-status-maintenance
```

### 结构与杂项

```
--kokoro-header-height    --kokoro-sidebar-width    --kokoro-content-max-width
--kokoro-card-padding     --kokoro-opacity-disabled
--kokoro-z-dropdown       --kokoro-z-modal          --kokoro-z-toast
--kokoro-border-width     --kokoro-ring-width       --kokoro-ring-color
--kokoro-bg-image         --kokoro-bg-size          --kokoro-bg-repeat    --kokoro-bg-attachment
--kokoro-hero-gradient    --kokoro-hero-overlay
--kokoro-card-bg          --kokoro-card-border      --kokoro-card-border-image
--kokoro-header-bg        --kokoro-header-fg        --kokoro-footer-bg
--kokoro-code-bg          --kokoro-backdrop-blur
```

## 六、必须遵守的工作流程

1. **先理解需求**：确定这是深色还是浅色主题、主色调、列表形态、字体调性。
2. **定一套完整色板**：至少覆盖这 10 个，缺一个都会导致某处配色突兀：
   ```
   --kokoro-color-bg          页面底色
   --kokoro-color-surface     卡片面
   --kokoro-color-surface-sunken  下沉面（代码块、输入框）
   --kokoro-color-text        正文
   --kokoro-color-text-muted  次要文字
   --kokoro-color-text-faint  最弱文字
   --kokoro-color-border      描边
   --kokoro-color-primary     主色
   --kokoro-color-primary-fg  主色上的文字
   --kokoro-color-accent      辅助色
   ```
3. **深色主题必须写 `tokensDark`**（至少覆盖 bg/surface/text/border 四项），否则深色下会看不清。同时 `"mode": { "default": "dark" }`。
4. **只用浅色的主题**：设 `"mode": { "supportsDark": false }`，这样不会给用户一个半残的深色按钮。
5. **layout 要和调性一致**，别配错（例：极简终端风配 `list.mode: "compact"` + `detail.header.variant: "none"`；纸质感配 `hero.variant: "plain"` + 大留白；仪表盘风配 `list.mode: "table"`）。
6. **自查一遍**，逐条对照 §2 的硬性限制。特别检查：
   - 所有变量名都在 §5 清单里，一个字符都没差
   - 所有值里都没有 `;` `{` `}` `<` `>` `\` `@`
   - 没有远程 `url()`、没有 `@import`
   - 所有枚举值都在给定取值范围内
   - `id` 没有 `kokoro.` 前缀、没有大写和中文
   - 没有多余字段

## 七、输出要求

1. **只输出一个 JSON 文件的内容**，包在 ```json 代码块里。
2. 代码块外**不要**写解释、说明或评论（用户会直接复制粘贴）。
3. 可以在 JSON 之后（代码块外）用 3–5 行说明你的设计思路：主色为什么选这个、布局为什么这么配、对比度大概是多少。这部分不影响导入。
4. 如果我提供了参考主题（已有的 JSON），请在它的基础上改，保持它没提到的那部分键不变。

======

## 怎么用

**方式一：最简单**

1. 复制上面 `======` 之间的全部内容
2. 在末尾加一句你的需求，比如：

   > 我要一套深色主题，主色青绿色，风格像终端、等宽字体，节点列表用紧凑行，详情页不要大图头，只要信息密度高。

3. AI 会给你一段 JSON
4. 复制 JSON → 后台 `/admin/themes` → 粘贴到文本框 → 「校验并导入」

**方式二：基于现有主题改（效果最稳）**

1. 先去任意 Kokoro 站点抓一份内置默认主题当底稿：
   `https://那个站/theme-export/kokoro.daylight`
   （内置主题只有这一套；想看别的形态，参考本站
   `docs/themes/example-terminal.json`、`example-paper.json`）
2. 把这份 JSON 和上面 `======` 之间的内容一起发给 AI
3. 加一句：`基于这份主题，改成深色科技风，其余保持不变`

**方式三：想要资源（字体/背景图）**

裸 JSON 装不下二进制资源，需要打成 `.kokoro-theme` 包。这条路建议用 Go 写工具（见 `docs/THEME-FORMAT.md` §6.5），而不是手工拼 zip。

## 常见失败原因速查

| AI 的错误 | 症状 | 纠正 |
| --- | --- | --- |
| 写了 `styles` / `css` 字段 | 解析失败：unknown field | 删掉，主题不能写 CSS |
| `--kokoro-line-height` | 未知变量 | 改成 `--kokoro-font-line-height` |
| `--kokoro-card-border-image` | 未知变量 | 确认拼写；它是合法变量，常见错拼是 `--kokoro-card-border-img` |
| 值里有分号 | 值里不允许出现 ";" | 一个变量只写一个值 |
| `url(https://fonts…)` | 只允许 data:image/ 与本地路径 | 删掉远程字体引用 |
| `"id": "kokoro.xxx"` | 官方保留前缀 | 换一个前缀 |
| `"mode": "dark"` | 应是对象 | `"mode": { "default": "dark" }` |
| `--kokoro-chart-line-width: "2px"` | 不是合法数值 | 去掉单位，写 `2` |