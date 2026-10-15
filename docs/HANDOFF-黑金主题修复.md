# Kokoro 黑金主题与性能修复交接

更新时间：2026-10-16（Asia/Shanghai）

## 目标与当前状态

DMIT 上 Kokoro 的自定义黑金主题曾出现标题栏异常放大、总览区错位、主题切换和首页加载卡顿，以及无法从文件选择器导入 JSON 的问题。本轮已修复并部署兼容与性能改动；DMIT 服务运行正常。主题包已单独生成，尚未在 DMIT 管理后台导入新版主题包。

## 已完成

- 将面板公共结构样式从只匹配 `kokoro.*` 内置主题，改为适用于所有经过校验的主题 ID。修复自定义主题漏掉公共样式造成的巨大标志和总览排版错乱；保留各内置主题独有样式。
- 主题导入表单现在接受 `.json`、`.kokoro-theme` 和 `.zip`；上传 JSON 与粘贴清单走同一校验及持久化路径，并处理文件大小限制和无效 JSON。
- 重写节点 ISP 延迟查询，让查询从节点时间索引读取有限的近期轮次。DMIT 实际数据集上的同结果对比中，旧查询约 2.8 秒，新查询约 9 毫秒；返回结果逐行一致。
- 修改 SSE 使用的最新指标快照查询，避免每秒扫描全部历史指标。
- 增加主题包壁纸优先级：主题包有自带壁纸时使用包内壁纸；没有自带壁纸时仍使用站点壁纸作为后备，不改写站点设置。
- 增加 JSON 上传、主题持久化、壁纸优先级、最新快照等回归覆盖；主题格式文档补充 JSON 导入和资源规范。
- 更新黑金主题源文件和规范兼容性说明。

## 部署与验证

- 服务：`kokoro-hub`，DMIT 地址：[https://vps.mjfuns.lat](https://vps.mjfuns.lat)。服务配置和数据目录均未改动。
- 部署时间：2026-10-16 00:45（服务器时间）。部署前旧首页在首字节诊断中超过 8 秒仍未响应；部署后远端本机首字节约 **0.40 秒**，公网首页约 **0.51 秒**。
- 公网验证：`/`、`/theme.json`、`/static/style.css` 均返回 HTTP 200，主题接口仍报告 `jiwo.blackgold`；进程 `kokoro-hub` 为 active。
- 线上主题包和数据库备份完整性检查通过，SQLite `PRAGMA integrity_check` 返回 `ok`。
- 已用真实公网页面做 1440×1000 渲染检查：标题栏高度正常、总览项目对齐、卡片金色边框可见，页面无横向溢出。另有本地模拟 390px 移动视口检查，页面无横向溢出。
- 本地验证：发布脚本中的主题导入和壁纸相关 Hub 测试通过；`internal/store` 与 `internal/theme` 测试通过；Linux amd64 发布程序构建成功。

## 主题包交付

- 文件：[dist/themes/jiwo-blackgold-1.0.1.kokoro-theme](../dist/themes/jiwo-blackgold-1.0.1.kokoro-theme)，约 41 KB。
- SHA-256：`3ec8cb30d9b36072aa974135b000570b08f1c06c92a2d3c3fd3e4ea5a5d035e2`
- 包含标准 `theme.json`、说明、许可和 `assets/wallpaper.webp`；不包含 CSS、脚本或远程资源。主题 ID `jiwo.blackgold`，版本 `1.0.1`。
- 这次线上更新修复了面板结构兼容和服务查询性能。若要启用本版新增的壁纸与更清楚的香槟金边框，还需在后台重新导入上面的 1.0.1 主题包；导入前可先导出/保存当前主题 JSON。服务端不会自动替用户切换站点主题包。

## 线上备份与回滚

部署脚本在覆盖程序前备份了当前程序和 SQLite 数据库：

`/www/wwwroot/kokoro/backups/20261016-004536-theme-fix/`

备份清单在 `manifest.txt`，旧程序为 `kokoro`，数据库快照为 `kokoro.db`。部署只替换程序二进制并重启服务，没有数据库迁移；若需要回滚，先将备份中的 `kokoro` 原子替回 `/www/wwwroot/kokoro/kokoro`，然后执行 `systemctl restart kokoro-hub` 并确认服务恢复。不要删除该备份目录，直到确认不再需要回滚。

## 主要改动位置

- `internal/hub/static/style.css`：自定义主题公共布局与卡片样式匹配。
- `internal/hub/theme.go`、`internal/hub/templates/themes_panel.html`：壁纸来源优先级、JSON 文件导入。
- `internal/store/cardstats.go`、`internal/store/store.go`：ISP 延迟与最新指标查询性能。
- `themes/blackgold/`：1.0.1 主题源文件和壁纸。
- `docs/THEME-FORMAT.md`：主题导入和资源规范。

## 注意事项

- `.github/` 和根目录 `LICENSE` 是工作区原有未跟踪文件，未纳入本次改动或清理。
- 本交接记录中的响应时间是本次部署验证样本；DMIT 节点负载和网络变化会影响后续测量。
