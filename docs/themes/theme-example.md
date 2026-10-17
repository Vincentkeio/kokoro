# Kokoro 主题范例

把这份范例发给 AI，再描述你想要的视觉效果（例如“暖白纸感、朱红强调色、衬线标题、列表用表格”）。请 AI 只修改需要的设计变量和布局选项，最后输出完整、可导入的 `theme.json`。

本文件列出 Kokoro 当前支持的全部 168 个设计变量（CSS tokens）、布局字段、枚举取值和限制。设计变量用于控制颜色、字体、间距等视觉样式；布局字段用于选择已经内置的页面结构。JSON 文件本身不能包含注释，下面的说明供你和 AI 参考。

## 最小可用范例

```json
{
  "schemaVersion": 1,
  "id": "yourname.paper",
  "name": "暖纸",
  "version": "1.0.0",
  "description": "暖白纸感与朱红强调色",
  "author": "yourname",
  "tokensSchemaVersion": 2,
  "tokens": {
    "--kokoro-color-bg": "#f7f2e8",
    "--kokoro-color-surface": "#fffdf7",
    "--kokoro-color-text": "#29251f",
    "--kokoro-color-primary": "#a43d32",
    "--kokoro-font-sans": "Georgia, 'Songti SC', serif"
  },
  "layoutSchemaVersion": 1,
  "layout": {
    "home": { "list": { "mode": "card" } },
    "charts": { "type": "bar", "grid": true },
    "globe": { "showRoutes": true, "showFlow": true, "autoRotate": false }
  }
}
```

省略的 tokens 和 layout 字段会使用程序内置默认值。主题只能使用下列白名单，不能添加任意 CSS、HTML 或 JavaScript。

当前首页统一使用卡片，范例中的 `home.list.mode` 应保留 `card`。历史的其他列表模式取值保留格式兼容，但不会改变当前首页的列表形式。

## 卡片与测试结果的展示约定

卡片的硬件指标按双项排列，长文字会随可用宽度缩小；上下行速率与延迟同排，一小时图使用较紧凑的高度。三网线路在卡片中显示去重后的骨干摘要，悬浮结果窗保留北京、上海、深圳的完整明细和未识别说明。

有文章的按钮使用主题主色强调，无文章时使用淡色虚线。测试执行期间保留最近已完成的结果，并显示更新状态。结果窗由浏览器顶层显示，颜色、字体、边框和圆角继续采用主题变量。设计主题时请保留这些状态的可读性和区别。

## 全部设计变量（CSS tokens）

只写需要覆盖的变量。颜色支持十六进制、`rgb()`、`hsl()`、`color-mix()` 和引用其他 `--kokoro-*` 变量；长度使用 CSS 长度值；字体应使用字体栈。`--kokoro-bg-image` 只允许 `none` 或站内 `/_theme-assets/` 路径，不接受远程网址；data URI 若含被禁止的字符也会被拒绝。

安全规则同样适用于 `tokensDark` 和布局长度字段：不能包含控制字符、`;`、花括号、HTML 标签、反斜杠或 CSS 注释。合法的 `calc()`、`clamp()`、`var()`、渐变和字体栈继续支持。每个 `url()` 都会独立校验，必须指向规范的 `/_theme-assets/` 本地资源路径；即使外部网址中带有该字样，也会被拒绝。不能混入外部 URL、目录回退或 `image-set()`、`image()`、`src()` 等其他资源函数。带引号的本地 `url()` 可以使用，SVG data URI 会被拒绝。

背景图示例：`"--kokoro-bg-image": "url(/_theme-assets/wallpaper.webp)"`。在 `.kokoro-theme` 包中把图片放到 `assets/wallpaper.webp`；Hub 会自动补上当前主题 ID。包内每个路径必须唯一，不能重复放置 `theme.json` 或同名资源。

| 可用变量 |
| --- |
| `--kokoro-backdrop-blur` |
| `--kokoro-bg-attachment` |
| `--kokoro-bg-image` |
| `--kokoro-bg-repeat` |
| `--kokoro-bg-size` |
| `--kokoro-border-width` |
| `--kokoro-card-bg` |
| `--kokoro-card-border` |
| `--kokoro-card-border-image` |
| `--kokoro-card-padding` |
| `--kokoro-chart-area-opacity` |
| `--kokoro-chart-axis` |
| `--kokoro-chart-bar-opacity` |
| `--kokoro-chart-bar-radius` |
| `--kokoro-chart-grid` |
| `--kokoro-chart-line-width` |
| `--kokoro-chart-point-radius` |
| `--kokoro-chart-series-1` |
| `--kokoro-chart-series-2` |
| `--kokoro-chart-series-3` |
| `--kokoro-chart-series-4` |
| `--kokoro-chart-series-5` |
| `--kokoro-chart-series-6` |
| `--kokoro-chart-series-7` |
| `--kokoro-chart-series-8` |
| `--kokoro-chart-tooltip-bg` |
| `--kokoro-chart-tooltip-fg` |
| `--kokoro-clock-bg` |
| `--kokoro-clock-color` |
| `--kokoro-clock-divider` |
| `--kokoro-clock-gap` |
| `--kokoro-clock-muted` |
| `--kokoro-clock-padding` |
| `--kokoro-clock-size` |
| `--kokoro-code-bg` |
| `--kokoro-color-accent` |
| `--kokoro-color-accent-fg` |
| `--kokoro-color-bg` |
| `--kokoro-color-bg-subtle` |
| `--kokoro-color-border` |
| `--kokoro-color-border-strong` |
| `--kokoro-color-danger` |
| `--kokoro-color-info` |
| `--kokoro-color-link` |
| `--kokoro-color-primary` |
| `--kokoro-color-primary-active` |
| `--kokoro-color-primary-fg` |
| `--kokoro-color-primary-hover` |
| `--kokoro-color-success` |
| `--kokoro-color-surface` |
| `--kokoro-color-surface-raised` |
| `--kokoro-color-surface-sunken` |
| `--kokoro-color-text` |
| `--kokoro-color-text-faint` |
| `--kokoro-color-text-inverse` |
| `--kokoro-color-text-muted` |
| `--kokoro-color-warning` |
| `--kokoro-content-max-width` |
| `--kokoro-easing` |
| `--kokoro-font-letter-spacing` |
| `--kokoro-font-line-height` |
| `--kokoro-font-mono` |
| `--kokoro-font-sans` |
| `--kokoro-font-size-2xl` |
| `--kokoro-font-size-3xl` |
| `--kokoro-font-size-lg` |
| `--kokoro-font-size-md` |
| `--kokoro-font-size-sm` |
| `--kokoro-font-size-xl` |
| `--kokoro-font-size-xs` |
| `--kokoro-font-weight-bold` |
| `--kokoro-font-weight-medium` |
| `--kokoro-font-weight-normal` |
| `--kokoro-footer-bg` |
| `--kokoro-globe-day` |
| `--kokoro-globe-flow` |
| `--kokoro-globe-flow-core` |
| `--kokoro-globe-flow-core-width` |
| `--kokoro-globe-flow-glint-size` |
| `--kokoro-globe-flow-glow` |
| `--kokoro-globe-flow-glow-width` |
| `--kokoro-globe-flow-head-size` |
| `--kokoro-globe-flow-length` |
| `--kokoro-globe-flow-opacity` |
| `--kokoro-globe-flow-period` |
| `--kokoro-globe-flow-pulse-window` |
| `--kokoro-globe-hub-ring-size` |
| `--kokoro-globe-hub-ring-width` |
| `--kokoro-globe-hub-size` |
| `--kokoro-globe-night` |
| `--kokoro-globe-node-halo-size` |
| `--kokoro-globe-node-muted-ring-size` |
| `--kokoro-globe-node-muted-size` |
| `--kokoro-globe-node-offline` |
| `--kokoro-globe-node-online` |
| `--kokoro-globe-node-ring-size` |
| `--kokoro-globe-node-size` |
| `--kokoro-globe-ocean-day` |
| `--kokoro-globe-ocean-night` |
| `--kokoro-globe-rim` |
| `--kokoro-globe-rotation-speed` |
| `--kokoro-globe-route` |
| `--kokoro-globe-route-muted-opacity` |
| `--kokoro-globe-route-opacity` |
| `--kokoro-globe-route-underlay-opacity` |
| `--kokoro-globe-route-underlay-width` |
| `--kokoro-globe-route-width` |
| `--kokoro-glow` |
| `--kokoro-header-bg` |
| `--kokoro-header-fg` |
| `--kokoro-header-height` |
| `--kokoro-hero-gradient` |
| `--kokoro-hero-overlay` |
| `--kokoro-node-card-bg` |
| `--kokoro-node-card-border` |
| `--kokoro-node-card-label` |
| `--kokoro-node-card-label-size` |
| `--kokoro-node-card-padding` |
| `--kokoro-node-card-radius` |
| `--kokoro-node-card-section-gap` |
| `--kokoro-node-card-title` |
| `--kokoro-node-card-title-size` |
| `--kokoro-node-card-value` |
| `--kokoro-node-card-value-size` |
| `--kokoro-opacity-disabled` |
| `--kokoro-radius-full` |
| `--kokoro-radius-lg` |
| `--kokoro-radius-md` |
| `--kokoro-radius-sm` |
| `--kokoro-rate-bg` |
| `--kokoro-rate-border` |
| `--kokoro-rate-chart-height` |
| `--kokoro-rate-down` |
| `--kokoro-rate-gap` |
| `--kokoro-rate-label` |
| `--kokoro-rate-label-size` |
| `--kokoro-rate-padding` |
| `--kokoro-rate-radius` |
| `--kokoro-rate-up` |
| `--kokoro-rate-value` |
| `--kokoro-rate-value-size` |
| `--kokoro-ring-color` |
| `--kokoro-ring-width` |
| `--kokoro-shadow-lg` |
| `--kokoro-shadow-md` |
| `--kokoro-shadow-sm` |
| `--kokoro-sidebar-width` |
| `--kokoro-space-1` |
| `--kokoro-space-2` |
| `--kokoro-space-3` |
| `--kokoro-space-4` |
| `--kokoro-space-5` |
| `--kokoro-space-6` |
| `--kokoro-space-7` |
| `--kokoro-space-8` |
| `--kokoro-status-idle` |
| `--kokoro-status-maintenance` |
| `--kokoro-status-offline` |
| `--kokoro-status-online` |
| `--kokoro-transition-base` |
| `--kokoro-transition-fast` |
| `--kokoro-transition-slow` |
| `--kokoro-uptime-bar-gap` |
| `--kokoro-uptime-bar-height` |
| `--kokoro-uptime-bar-radius` |
| `--kokoro-z-dropdown` |
| `--kokoro-z-modal` |
| `--kokoro-z-toast` |
## 全部布局字段与可选值

以下字段均可省略。布尔字段只能填 `true` 或 `false`；尺寸字段使用 CSS 长度；文本字段受长度限制。未列出的字段会被拒绝。

| 字段 | 可选值 / 类型 | 用途 |
| --- | --- | --- |
| `shell.contentMaxWidth` | CSS 长度 | 页面最大宽度；内置主题使用 `2080px`，为 3×N 的双栏卡片留出空间 |
| `shell.contentAlign` | `center` / `left` | 页面内容对齐 |
| `shell.header.variant` | `solid` / `transparent` / `blur` / `bordered` | 顶栏样式 |
| `shell.header.sticky` | boolean | 顶栏是否吸顶 |
| `shell.header.showSearch` | boolean | 是否显示搜索 |
| `shell.header.showModeSwitch` | boolean | 是否显示深浅色切换 |
| `shell.footer.variant` | `simple` / `columns` / `minimal` | 页脚样式 |
| `home.hero.enabled` | boolean | 是否显示首页 Hero 区 |
| `home.hero.variant` | `plain` / `gradient` / `image` / `split` | Hero 形态 |
| `home.hero.align` | `left` / `center` | Hero 内容对齐 |
| `home.hero.subtitle` | 最长 200 字符 | Hero 副标题 |
| `home.hero.showStats` | boolean | 是否显示 Hero 统计 |
| `home.list.mode` | `card` / `table` / `compact` / `map` | 节点列表形态 |
| `home.list.allowUserSwitch` | boolean | 是否允许访客切换列表形态 |
| `home.list.groupBy` | `none` / `status` / `region` / `tag` / `provider` / `group` | 节点分组 |
| `home.list.card.minWidth` | CSS 长度 | 卡片最小宽度 |
| `home.list.card.gap` | CSS 长度 | 卡片间距 |
| `home.list.card.border` | boolean | 是否显示卡片边框 |
| `home.list.card.shadow` | `none` / `sm` / `md` / `lg` / `glow` | 卡片阴影 |
| `home.list.card.hoverLift` | boolean | 悬停时是否上浮 |
| `home.list.card.columns` | 整数 `3`–`5`；`0` 或省略为自适应 | 卡片列数 |
| `home.list.table.density` | `compact` / `comfortable` | 表格密度 |
| `home.list.table.striped` | boolean | 是否显示斑马纹 |
| `home.list.table.rowHover` | boolean | 行悬停效果 |
| `home.list.table.showUnit` | boolean | 表格是否显示单位 |
| `home.list.compact.rowHeight` | CSS 长度 | 紧凑列表行高 |
| `home.list.compact.dividers` | boolean | 是否显示行分隔线 |
| `home.list.compact.showFlag` | boolean | 是否显示地区旗帜 |
| `home.list.compact.inlineBars` | boolean | 是否显示行内用量条 |
| `detail.header.variant` | `hero` / `compact` / `none` | 详情页头部形态 |
| `detail.header.height` | CSS 长度 | 详情页头部高度 |
| `detail.header.showTags` | boolean | 是否显示标签 |
| `detail.header.secondaryLine` | `location` / `specs` / `uptime` / `none` | 第二信息行内容 |
| `detail.sectionGap` | CSS 长度 | 详情页区块间距 |
| `detail.introFontSize` | CSS 长度 | 介绍文字字号 |
| `detail.commentsCollapsed` | boolean | 评论区是否默认折叠 |
| `charts.type` | `line` / `area` / `bar` / `dot` / `step` | 首页、详情页的历史图表形态；卡片 `dot` 使用圆形数据点与浅色趋势连线，点半径保持实际像素尺寸，避免拉伸成椭圆 |
| `charts.grid` | boolean | 是否显示网格线 |
| `charts.legend` | boolean | 是否显示图例 |
| `charts.axisLabel` | boolean | 是否显示坐标轴标签 |
| `charts.smooth` | boolean | 是否平滑曲线 |
| `charts.tooltip` | `axis` / `item` / `none` | 图表提示框模式 |
| `charts.height` | CSS 长度 | 通用图表高度 |
| `charts.heightDetail` | CSS 长度 | 详情页图表高度 |
| `misc.statusDotStyle` | `solid` / `ring` / `glow` / `none` | 状态点样式 |
| `misc.usageBarStyle` | `solid` / `gradient` / `segmented` / `striped` / `dotted` / `none` | 资源占用条形态 |
| `misc.uptimeBarStyle` | `solid` / `pill` / `hatch` / `ruled` / `tiles` / `dots` | 在线率时间轴形态，每天一格 |
| `misc.tagStyle` | `pill` / `square` / `text` | 标签样式 |
| `misc.badgeStyle` | `soft` / `outline` / `solid` | 徽标样式 |
| `misc.showUptime` | boolean | 是否显示运行时间 |
| `misc.numberFormat` | `si` / `plain` / `percent` | 数字格式 |
| `misc.dateFormat` | `relative` / `absolute` | 日期格式 |
| `globe.showRoutes` | boolean | 显示或隐藏地球航线 |
| `globe.showFlow` | boolean | 显示或隐藏航线流光 |
| `globe.autoRotate` | boolean | 开关地球自动旋转，仍尊重系统减少动态效果偏好 |

## 顶层字段

| 字段 | 说明 |
| --- | --- |
| `schemaVersion` | 当前为 `1` |
| `id` | 必填；3–64 位小写字母、数字、点、下划线或连字符；不要使用保留的 `kokoro.` 前缀 |
| `name` / `version` | 必填；主题显示名与版本 |
| `description` / `author` / `homepage` / `license` / `tags` | 主题说明和署名信息 |
| `sourceUrl` | 来源地址；获取其他站点主题时自动记录，通常不要手写 |
| `tokensSchemaVersion` | 当前为 `2` |
| `tokens` | 必填；浅色或唯一模式下需要覆盖的设计变量 |
| `tokensDark` | 可选；深色模式覆盖，未写的变量沿用 `tokens` |
| `mode.default` | `light` / `dark` / `auto` |
| `mode.allowUserSwitch` / `mode.supportsDark` | boolean |
| `layoutSchemaVersion` | 当前为 `1` |
| `layout` | 可选；使用上表中列出的布局字段 |
| `package` | `.kokoro-theme` 打包元数据，由打包流程生成，通常不要手写 |

## 生成和导入

把本范例发给 AI，并描述目标风格、明暗模式、强调色、字体气质和偏好的布局。要求 AI 保留合法字段，只输出严格 JSON（不带 Markdown 代码围栏和注释），并让它检查所有颜色、长度、枚举值和主题 ID。保存为 `theme.json`，再到 Kokoro 后台的主题页导入。

主题资源不能引用远程 URL；禁止 `@import`、任意 CSS、HTML 和 JavaScript。主题只能使用上面列出的设计变量和布局字段。

## 图表、BAR 与毛玻璃范例

图表类型会应用到全网速率、资源历史、节点卡片与三网波动图；`smooth` 仅平滑 `line` / `area`，`step` 保持阶梯。资源占用条与在线率时间轴是两个独立选项，形态会随主题导出与复制。

| 内置主题 | 历史图 | 资源条 | 在线率时间轴 |
| --- | --- | --- | --- |
| 简洁 | 平滑面积图 | 实心 | 实心格 |
| 纸页 | 柱状图 | 分段 | 印刷线纹 |
| 极夜 | 阶梯图 | 渐变 | 银蓝镶边格 |
| 朦胧 | 平滑曲线 | 点阵 | 圆点 |

```json
{
  "tokens": {
    "--kokoro-backdrop-blur": "28px",
    "--kokoro-color-surface": "rgba(255,255,255,.30)",
    "--kokoro-node-card-bg": "rgba(255,255,255,.28)",
    "--kokoro-rate-bg": "rgba(255,255,255,.28)",
    "--kokoro-clock-size": "clamp(36px, 3vw, 48px)",
    "--kokoro-clock-gap": "24px",
    "--kokoro-uptime-bar-height": "16px",
    "--kokoro-uptime-bar-gap": "4px",
    "--kokoro-uptime-bar-radius": "999px"
  },
  "layout": {
    "shell": { "contentMaxWidth": "2080px" },
    "charts": { "type": "line", "smooth": true },
    "misc": { "usageBarStyle": "dotted", "uptimeBarStyle": "dots" },
    "globe": { "showRoutes": true, "showFlow": true, "autoRotate": true }
  }
}
```

把这些字段合并进上方完整范例，不要直接导入这段局部配置。磨砂面板需要同时设置模糊长度与半透明背景；航线开关、流光和自动旋转分别控制。

双栏卡片的在线状态与在线率文字共享第一行，标签与在线率条共享第二行。“近 N 天”统计说明保留在在线率文字行，整行会自适应缩小。条形高度和形状继续由主题定义，组件会居中对齐。两栏剩余空间分配在内容区块之间，底部操作和分隔线保持对齐。
