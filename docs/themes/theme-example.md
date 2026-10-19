# Kokoro 主题范例

把这份范例发给 AI，再描述你想要的视觉效果（例如“暖白纸感、朱红强调色、衬线标题”）。请 AI 只修改需要的设计变量和布局选项，最后输出完整、可导入的 `theme.json`。

本文件列出 Kokoro 当前支持的全部 185 个设计变量（CSS tokens）、布局字段、枚举取值和限制。设计变量用于控制颜色、字体、间距等视觉样式；布局字段用于选择已经内置的页面结构。JSON 文件本身不能包含注释，下面的说明供你和 AI 参考。

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

有文章的按钮使用主题主色强调，无文章时使用淡色虚线。测试执行期间保留最近已完成的结果，并显示更新状态。结果窗由浏览器顶层显示，颜色、字体、边框和圆角继续采用主题变量。地球上的地区连线标注也应保持清晰；朦胧主题为标注卡片使用与面板协调的半透明磨砂层。设计主题时请保留这些状态的可读性和区别。

## 全部设计变量（CSS tokens）

只覆盖需要的变量，其余沿用默认值。当前为 tokens v3，兼容旧版本；下文视觉扩展变量必须显式使用 `tokensSchemaVersion: 3`。颜色、长度、数字、时间、字体栈按各自类型校验；单值最多 512 字节。允许多层渐变、受校验的 `var()`、本地图片和栅格遮罩，拒绝外部资源、CSS 注入、脚本和 SVG。

本地资源写 `url(/_theme-assets/ambient.webp)`，对应包内 `assets/ambient.webp`，由引擎补全当前主题 ID。不要引用其他主题 ID 的文件。若需要本地文件，必须交付完整 `.kokoro-theme`，不能只交付 JSON。

| 变量名 |
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
| `--kokoro-chart-filter` |
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
| `--kokoro-globe-background` |
| `--kokoro-globe-background-size` |
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
| `--kokoro-globe-lit-day` |
| `--kokoro-globe-lit-night` |
| `--kokoro-globe-matrix-color` |
| `--kokoro-globe-matrix-density` |
| `--kokoro-globe-matrix-font-size` |
| `--kokoro-globe-matrix-highlight` |
| `--kokoro-globe-matrix-opacity` |
| `--kokoro-globe-matrix-secondary` |
| `--kokoro-globe-matrix-speed` |
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
| `--kokoro-usage-bar-fill` |
| `--kokoro-usage-bar-glow` |
| `--kokoro-usage-bar-height` |
| `--kokoro-usage-bar-radius` |
| `--kokoro-usage-bar-track` |
| `--kokoro-z-dropdown` |
| `--kokoro-z-modal` |
| `--kokoro-z-toast` |

## 全部布局字段与可选值

以下字段位于 `layout` 内，均可省略。布尔字段只能填 `true` 或 `false`；尺寸字段使用 CSS 长度；文本字段受长度限制。未列出的字段会被拒绝。

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
| `misc.usageBarStyle` | `solid` / `gradient` / `segmented` / `striped` / `dotted` / `soft` / `laser` / `ruled` / `none` | 资源占用条形态；`laser` 为外发光激光，`ruled` 为墨线刻度，二者需 layout v2 |
| `misc.uptimeBarStyle` | `solid` / `pill` / `hatch` / `ruled` / `tiles` / `dots` | 在线率时间轴形态，每天一格 |
| `misc.tagStyle` | `pill` / `square` / `text` | 标签样式 |
| `misc.badgeStyle` | `soft` / `outline` / `solid` | 徽标样式 |
| `misc.showUptime` | boolean | 是否显示运行时间 |
| `misc.numberFormat` | `si` / `plain` / `percent` | 数字格式 |
| `misc.dateFormat` | `relative` / `absolute` | 日期格式 |
| `globe.showRoutes` | boolean | 显示或隐藏地球航线 |
| `globe.showFlow` | boolean | 显示或隐藏航线流光 |
| `globe.autoRotate` | boolean | 开关地球自动旋转，仍尊重系统减少动态效果偏好 |
| `globe.backdrop` | `none` / `matrix` | 地球背景；`matrix` 为冰蓝字符列向上循环，需 layout v2 |

| `globe.matrixGlyphs` | `numeric` / `binary` / `hex` / `katakana` / `mixed` | 字符雨字符集，默认 mixed，需 layout v2 |
| `visuals` | 下文列出的组件规则 | 受检 CSS 属性，需 layout v2 |
| `motions` | 自定义动效字典 | 受检关键帧，需 layout v2 |
| `layers` | 固定组件的装饰层 | 每组件最多 2 层，需 layout v2 |
| `responsive` | `mobile` / `tablet` 规则字典 | 固定断点下的视觉覆盖，需 layout v2 |

### 动效随主题获取

使用 `layoutSchemaVersion: 2`，并声明 `layout.misc.usageBarStyle: "laser"`、`layout.globe.backdrop: "matrix"`，即可携带激光条与字符雨配置。这些能力由接收面板的通用渲染器实现，不依赖主题 ID；接收面板需先更新到支持 layout v2 的版本。主题包不执行第三方 CSS 或脚本。已获取过旧主题时，可勾选「覆盖本地同名主题」重新获取完整包。

## 顶层字段

| 字段 | 说明 |
| --- | --- |
| `schemaVersion` | 当前为 `1` |
| `id` | 必填；3–64 位小写字母、数字、点、下划线或连字符；不要使用保留的 `kokoro.` 前缀 |
| `name` / `version` | 必填；主题显示名与版本 |
| `description` / `author` / `homepage` / `license` / `tags` | 主题说明和署名信息 |
| `sourceUrl` | 来源地址；获取其他站点主题时自动记录，通常不要手写 |
| `tokensSchemaVersion` | 当前支持 `3`，兼容 `1`、`2`；新视觉变量需使用 `3` |
| `tokens` | 必填；浅色或唯一模式下需要覆盖的设计变量 |
| `tokensDark` | 可选；深色模式覆盖，未写的变量沿用 `tokens` |
| `mode.default` | `light` / `dark` / `auto` |
| `mode.allowUserSwitch` / `mode.supportsDark` | boolean |
| `layoutSchemaVersion` | 当前支持 `2`，兼容 `1`；激光条、刻度条和字符雨需使用 `2` |
| `layout` | 可选；还可使用下文 `visuals` 组件规则，需 layout v2 |
| `package` | `.kokoro-theme` 打包元数据，由打包流程生成，通常不要手写 |

## 生成和导入

把本范例发给 AI，并描述目标风格、明暗模式、强调色、字体气质和偏好的布局。要求 AI 保留合法字段，只输出严格 JSON（不带 Markdown 代码围栏和注释），并让它检查所有颜色、长度、枚举值和主题 ID。保存为 `theme.json`，再到 Kokoro 后台的主题页导入。

主题资源不能引用远程 URL；禁止 `@import`、任意 CSS、HTML 和 JavaScript。主题只能使用上面列出的设计变量和布局字段。

## 图表、BAR 与毛玻璃范例

地球背景请使用 `--kokoro-globe-background` 或 `layout.visuals.globe-background`。Hero 背景与地球背景分别配置；接收面板不支持所需能力时，应更新面板后获取。

图表类型会应用到全网速率、资源历史、节点卡片与三网波动图；`smooth` 仅平滑 `line` / `area`，`step` 保持阶梯。资源占用条与在线率时间轴是两个独立选项，形态会随主题导出与复制。

| 内置主题 | 历史图 | 资源条 | 在线率时间轴 |
| --- | --- | --- | --- |
| 简洁 | 平滑面积图 | 实心 | 实心格 |
| 纸页 | 柱状图 | 墨线刻度 | 印刷线纹 |
| 极夜 | 阶梯图 | 金属分段 | 银蓝镶边格 |
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

## 创作与获取使用同一个能力边界

当前版本支持 **tokens v3 + layout v2**。主题作者可以组合设计变量、下列组件样式、自定义关键帧、独立装饰层、响应式样式、预设动效、字符雨和静态或动画 WebP / GIF。所有配置都在清单内，所有本地资源都在包内；任意名称的第三方主题都能使用，不依赖特定主题 ID。

本地安装、导出、导入和一键获取使用相同的版本、属性、路径、资源和包大小校验。缺少图片、未知字段、不支持的能力版本、清单与包 ID 不一致，都应报错；不会把只有配色的残缺清单当作完整主题安装。GitHub 目录优先读取 `theme.kokoro-theme`。裸 JSON 只适合不引用本地文件的主题。

旧面板需先更新到支持相应能力的版本。既有同名主题默认保留；主动更新时勾选「覆盖本地同名主题」，失败保留旧主题及资源。主题图片随完整包复制，站长另外设置的站点壁纸和账号配置属于站点数据，不是主题包内容。

### 新视觉变量（tokens v3）

| 变量 | 用途与范围 |
| --- | --- |
| `--kokoro-globe-background` | 地球区域的单层或多层背景图片 / 渐变；本地文件必须入包 |
| `--kokoro-globe-background-size` | 如 `cover`、`contain`、`180% 180%` |
| `--kokoro-globe-matrix-color` / `highlight` / `secondary` | 字符雨主色、亮部和次色；后两项也有完整 `--kokoro-globe-matrix-` 前缀 |
| `--kokoro-globe-matrix-opacity` | 整体透明度，限制在 0–1 |
| `--kokoro-globe-matrix-speed` | 移动倍率，限制在 0.25–3，默认 1 |
| `--kokoro-globe-matrix-density` | 列数，限制在 8–64，默认 24 |
| `--kokoro-globe-matrix-font-size` | 字符尺寸，限制在 8–20px，默认 12px |
| `--kokoro-usage-bar-height` / `radius` | 资源条高度与圆角；后项完整名称为 `--kokoro-usage-bar-radius` |
| `--kokoro-usage-bar-fill` / `track` | 填充层与轨道的背景，可用渐变；两项都带完整前缀 |
| `--kokoro-usage-bar-glow` | 填充层阴影，如 `0 0 8px currentColor` |
| `--kokoro-chart-filter` | 折线滤镜，如 `drop-shadow(0 0 2px #b8a5ff)` |

BAR 的高度、圆角、背景与光晕只在主题显式设置时覆盖所选形态。`laser` 自带外发光；`ruled` 是墨线刻度。字符雨只生成一次有限数量的文本列，由 CSS 平移；尊重减少动态效果偏好和地球可见性暂停，不用每帧脚本。

### 组件视觉配置 `layout.visuals`

键名选择固定组件，不接受自定义选择器。规则应用于公开页面，后台操作界面不受这层扩展规则影响。深浅模式差异可通过 `var(--kokoro-...)` 引用 `tokensDark`。

| 组件键 | 范围 |
| --- | --- |
| `header` / `hero` / `overview` | 顶栏、Hero、总览统计 |
| `card` / `card-hover` / `card-offline` / `card-focus` | 节点卡片、悬停、离线和内部键盘焦点状态 |
| `card-result` | 卡片测试结果与网络结果内层 |
| `rate-panel` / `rate-chart` | 总览速率面板与流量趋势内层 |
| `globe-panel` / `globe-background` | 地球外层面板与内部背景 |
| `clock` | 总览时钟 |
| `usage-track` / `usage-fill` | CPU、内存、磁盘资源条轨道与填充 |
| `chart-line` / `chart-area` | 历史、卡片图表折线与面积层 |
| `uptime-cell` / `button` / `tag` | 在线率格子、按钮、标签 |
| `button-hover` / `button-focus` / `tag-hover` | 按钮悬停、键盘焦点与标签悬停状态 |

可用属性：

`background`, `background-image`, `background-color`, `background-size`, `background-position`, `background-repeat`, `background-blend-mode`, `color`, `border`, `border-color`, `border-style`, `border-width`, `border-radius`, `box-shadow`, `text-shadow`, `filter`, `backdrop-filter`, `opacity`, `mix-blend-mode`, `mask-image`, `mask-size`, `mask-position`, `mask-repeat`, `clip-path`, `transform`, `transform-origin`, `outline-color`, `stroke`, `stroke-width`, `stroke-dasharray`, `fill`, `font-family`, `font-size`, `letter-spacing`, `transition-duration`, `outline`, `outline-width`, `outline-style`, `outline-offset`, `text-decoration`, `text-decoration-color`, `text-decoration-thickness`, `text-underline-offset`, `font-weight`, `line-height`, `text-transform`, `background-clip`, `background-origin`, `border-image`, `border-image-source`, `border-image-slice`, `border-image-width`, `stroke-dashoffset`。

可用多层线性 / 径向 / 锥形 / 重复渐变，阴影、金属纹理、局部磨砂、渐变或本地栅格遮罩、几何 `clip-path`、折线虚线与光晕。CSS 值仍经过注入和资源校验；不接受任意 CSS 文件、`@import`、自定义选择器、原始 keyframes 文本、伪元素内容、HTML、脚本、SVG、远程 URL、`!important` 或页面定位属性。

| 动效字段 | 可选值 |
| --- | --- |
| `motion` | `none`、`breathe`（透明度呼吸）、`float`（轻微浮动）、`pan`（背景缓移）、`custom:名称`（下文定义的关键帧） |
| `motion-duration` | `2s`–`60s`，默认 `6s`；可写等价 `ms` |
| `motion-distance` | float 位移，默认 `4px`，渲染时限制在 0–12px |

pan 配合较大的 `background-size` 使用。优先把动效用于小面积装饰，不对密集文本持续做重滤镜。减少动态效果时关闭预设动效，页面隐藏时暂停；不要把核心信息或按钮可用性依赖于动画。

### 受校验的自定义关键帧 `layout.motions`

可以自行设计扫光、边框流动、明暗闪烁、漂浮、位移和折线描边效果。使用 `motion: "custom:名称"` 引用，不接受原始 CSS 或 JavaScript。引擎为每个主题生成隔离的动画名称，复制到其他主题 ID 后仍可使用。

每组可设置 `duration`（2s–60s，默认 6s）、`easing`（linear / ease / ease-in / ease-out / ease-in-out）、`direction`（normal / reverse / alternate / alternate-reverse，默认 alternate）、`iterations`（0 为循环，1–20 为次数）。每主题最多 8 组动效，每组 2–12 帧；每帧的 `at` 为 0–100 整数，严格递增，必须包含 0 和 100。`style` 为 1–8 个属性，允许：

`transform`, `opacity`, `background-position`, `background-size`, `background-color`, `color`, `border-color`, `box-shadow`, `text-shadow`, `stroke-dashoffset`。

关键帧仍使用同一套 CSS 值和资源校验。优先使用 transform、opacity；持续变化的阴影和大面积背景更耗绘制，应保持克制。页面隐藏时暂停，装饰层滚出视口时也暂停；减少动态效果时关闭动画并保留基础样式。`motion-duration` 可在引用处覆盖周期，仍受相同范围限制。

### 独立装饰层 `layout.layers`

支持 header、hero、card、rate-panel、globe-panel、globe-background、usage-track、usage-fill 八类组件。每组件可声明 1–2 层，最多 16 层声明。每层有必填 `style` 和可选 `mobile` / `tablet` 样式覆盖，使用与组件相同的属性白名单及动效引用。`hideOnMobile`、`hideOnReducedMotion` 可分别隐藏手机或减少动态效果下的装饰。

层的容器和绘制元素由引擎创建，固定裁切在组件范围内并位于内容后面，不接收指针、焦点、选择或业务交互。作者不能改变其定位、层级或插入 HTML、文本和脚本。每页最多创建 256 个装饰容器；不支持追加不受控的 DOM。可组合纹理、几何遮罩、渐变光束与自定义关键帧，实现扫光、栅格、金属反光等效果。

### 响应式与交互状态

`layout.responsive.mobile` 在排版宽度 ≤640px 生效，`tablet` 在 641–1024px 生效；里面仍是组件名到样式对象的字典，与 `visuals` 使用同一白名单。触屏设备沿用窗口宽度；桌面窄窗口保留桌面排版宽度，通过页面滚动查看，不会因窗口缩小而套用手机版样式。可为手机减少阴影、关闭悬停变形或关闭动画；装饰层可直接 `hideOnMobile: true`。

button-hover、button-focus、card-focus 和 tag-hover 分别作用于按钮悬停、按钮键盘焦点、卡片内部焦点与标签悬停。可用 outline、outline-offset、渐变边框、文字装饰、字距和字重塑造交互。保留可见的键盘焦点、文字对比度和按钮辨识度。

配置最终会生成 CSS；JSON 承载可移植的样式、关键帧和装饰层声明。全部字段、媒体和能力版本都随完整包传递，不能依赖某台面板私有的 CSS、脚本或 DOM 修改。

### 字体边界

字体仅使用系统字体栈，可调整字形风格、字重、字号和字距。主题包不接受或下发第三方字体文件，不生成 `@font-face`，也不从外部 CDN 加载字体。

### 可直接导入的完整光效范例

以下范例不依赖外部文件，可直接保存为 `theme.json`。独立文件在仓库 `docs/themes/example-effects.json`。

```json
{
  "schemaVersion": 1,
  "id": "example.prism",
  "name": "棱镜范例",
  "version": "1.0.0",
  "author": "example",
  "description": "声明式材质、字符雨、激光资源条与呼吸动效范例",
  "tokensSchemaVersion": 3,
  "tokens": {
    "--kokoro-color-bg": "#10101c",
    "--kokoro-color-surface": "#181725",
    "--kokoro-color-text": "#f1edf9",
    "--kokoro-color-text-muted": "#b9b0cc",
    "--kokoro-color-primary": "#b8a5ff",
    "--kokoro-color-border": "#393047",
    "--kokoro-globe-matrix-color": "#73609e",
    "--kokoro-globe-matrix-highlight": "#e1d4ff",
    "--kokoro-globe-matrix-secondary": "#d7a2aa",
    "--kokoro-globe-matrix-speed": "0.7",
    "--kokoro-globe-matrix-opacity": "0.7",
    "--kokoro-globe-matrix-density": "32",
    "--kokoro-globe-matrix-font-size": "11px",
    "--kokoro-usage-bar-height": "5px",
    "--kokoro-usage-bar-glow": "0 0 5px currentColor, 0 0 12px currentColor",
    "--kokoro-chart-filter": "drop-shadow(0 0 2px rgba(184,165,255,.3))",
    "--kokoro-color-bg-subtle": "#151321",
    "--kokoro-color-surface-raised": "#242031",
    "--kokoro-color-surface-sunken": "#0e0d17",
    "--kokoro-color-border-strong": "#6b5b8b",
    "--kokoro-color-text-faint": "#9d90ae",
    "--kokoro-color-text-inverse": "#11101c",
    "--kokoro-color-primary-hover": "#cbbdff",
    "--kokoro-color-primary-active": "#a99ae9",
    "--kokoro-color-primary-fg": "#171023",
    "--kokoro-color-accent": "#d7a2aa",
    "--kokoro-color-accent-fg": "#201421",
    "--kokoro-color-success": "#8fc8b3",
    "--kokoro-color-warning": "#e0bd85",
    "--kokoro-color-danger": "#e0a0ab",
    "--kokoro-color-info": "#a2b9e4",
    "--kokoro-color-link": "#c7b7ff",
    "--kokoro-status-online": "#8fc8b3",
    "--kokoro-status-offline": "#746c81",
    "--kokoro-chart-series-1": "#b8a5ff",
    "--kokoro-chart-series-2": "#d7a2aa",
    "--kokoro-chart-series-3": "#91c7c1",
    "--kokoro-chart-grid": "#30263f",
    "--kokoro-chart-axis": "#9988ad",
    "--kokoro-chart-tooltip-bg": "#242031",
    "--kokoro-chart-tooltip-fg": "#f1edf9"
  },
  "mode": {
    "default": "dark",
    "allowUserSwitch": false
  },
  "layoutSchemaVersion": 2,
  "layout": {
    "home": {
      "list": {
        "mode": "card"
      }
    },
    "charts": {
      "type": "line",
      "smooth": true
    },
    "misc": {
      "usageBarStyle": "laser",
      "uptimeBarStyle": "tiles"
    },
    "globe": {
      "backdrop": "matrix",
      "matrixGlyphs": "hex",
      "showRoutes": true,
      "showFlow": true,
      "autoRotate": true
    },
    "visuals": {
      "card": {
        "background": "linear-gradient(145deg, rgba(44,34,62,.95), rgba(19,19,32,.98))",
        "border-color": "rgba(184,165,255,.25)",
        "box-shadow": "inset 0 1px 0 rgba(255,255,255,.08), 0 6px 20px rgba(0,0,0,.16)"
      },
      "card-hover": {
        "transform": "translateY(-2px)",
        "box-shadow": "0 8px 28px rgba(130,105,200,.18)"
      },
      "globe-background": {
        "background": "radial-gradient(ellipse at 20% 30%, rgba(135,88,180,.2), transparent 60%), linear-gradient(120deg, #11101d, #261c36, #11101d)",
        "background-size": "180% 180%",
        "motion": "pan",
        "motion-duration": "30s"
      },
      "usage-fill": {
        "motion": "breathe",
        "motion-duration": "8s"
      },
      "chart-line": {
        "stroke-dasharray": "6 3"
      },
      "button-focus": {
        "outline": "2px solid var(--kokoro-color-primary)",
        "outline-offset": "3px"
      }
    },
    "motions": {
      "sheen": {
        "duration": "12s",
        "easing": "linear",
        "direction": "alternate",
        "frames": [
          {
            "at": 0,
            "style": {
              "background-position": "0% 50%",
              "opacity": "0.15"
            }
          },
          {
            "at": 100,
            "style": {
              "background-position": "100% 50%",
              "opacity": "0.55"
            }
          }
        ]
      },
      "grid-drift": {
        "duration": "30s",
        "easing": "ease-in-out",
        "direction": "alternate",
        "frames": [
          {
            "at": 0,
            "style": {
              "transform": "translateX(-8px)"
            }
          },
          {
            "at": 100,
            "style": {
              "transform": "translateX(8px)"
            }
          }
        ]
      }
    },
    "layers": {
      "card": [
        {
          "style": {
            "background": "linear-gradient(110deg, transparent 30%, rgba(216,205,255,.16) 48%, transparent 65%)",
            "background-size": "250% 100%",
            "motion": "custom:sheen"
          },
          "hideOnMobile": true,
          "hideOnReducedMotion": true
        }
      ],
      "globe-background": [
        {
          "style": {
            "background": "repeating-linear-gradient(90deg, rgba(184,165,255,.12) 0 1px, transparent 1px 32px), repeating-linear-gradient(0deg, rgba(184,165,255,.1) 0 1px, transparent 1px 32px)",
            "mask-image": "radial-gradient(ellipse at center, #000, transparent 70%)",
            "opacity": "0.35",
            "motion": "custom:grid-drift"
          },
          "tablet": {
            "opacity": "0.2"
          },
          "hideOnMobile": true,
          "hideOnReducedMotion": true
        }
      ]
    },
    "responsive": {
      "mobile": {
        "card": {
          "box-shadow": "none"
        },
        "card-hover": {
          "transform": "none"
        }
      },
      "tablet": {
        "chart-line": {
          "stroke-width": "1.5"
        }
      }
    }
  }
}
```

### 带媒体资源的完整包

```text
theme.kokoro-theme（ZIP）
├── theme.json
├── assets/
│   ├── ambient.webp
│   └── texture.webp
├── preview.png（可选）
├── LICENSE（可选）
└── README.md（可选）
```

静态壁纸可用高质量 WebP；需要独创动画时可携带动画 WebP / GIF 并用于固定组件背景。保证文字对比度，提供静态方案或避免强烈闪烁；不要为了小体积牺牲明显画质。压缩包最多 8 MiB、64 个条目，单文件最多 2 MiB、解压总量最多 12 MiB，最大压缩比 100:1。开发、打包与获取遵守同一限制。

可用媒体为 png / jpg / jpeg / webp / gif / avif / bmp / ico；字体、SVG、CSS、脚本及可执行文件拒绝入包。资源路径必须规范，不得包含目录回退、符号链接、外部地址。若打包工具自动生成 `package.files` 和 `package.sha256`，必须与实际一致；手工 ZIP 可以省略整个 package 段，不要让 AI 编造校验和。

### 发给 AI 的制作要求

请先确定视觉方向、信息层级、对比度、桌面和窄屏布局，再组合本文件列出的真实能力。输出完整严格 JSON；需要媒体时同时交付真实文件与完整 `.kokoro-theme`，不要用不存在的资源占位。使用 tokens v3 / layout v2 声明新增能力，保持可读、可操作和减少动态效果支持。所有效果必须通过 tokens、layout.visuals、layout.motions、layout.layers、layout.responsive 或打包静态媒体实现；如果目标超出这些范围，应明确说明所需的引擎扩展，不要声称仅凭主题已实现。完成后验证导入、导出、另一台兼容面板重新获取后的资源和效果都完整。
