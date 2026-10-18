# Kokoro

Kokoro 是一个用于管理多台 VPS 的主控面板与轻量探针。Hub 部署在主机上，负责节点管理、任务下发和结果展示；Agent 安装在子机上，定期上报状态并执行 Hub 下发的固定任务。

> **版本状态：Alpha 2.0.0。** 当前发布标签为 [v0.1.3-preview.37](https://github.com/Vincentkeio/kokoro/releases/tag/v0.1.3-preview.37)，兼容既有数字版本更新器。适合在可恢复的测试环境中试用并反馈问题。功能、配置和接口仍可能调整；重要数据请先备份。

## 功能

- 汇总 VPS 在线状态、硬件信息、网络流量和延迟，并支持节点分组与筛选。
- 首页总览显示服务器总数、在线/离线与**点亮地区数**；点阵地球把有节点的**国家/地区整片点亮**。
- 通过任务队列下发硬件跑分、IP 质量与解锁、网络测速及三网回程检测。
- 查看任务进度、历史结果和原始输出。
- 到期提醒默认提前 7 天，支持 Telegram / Webhook 推送；站内消息保存购买留言与到期提醒备份，支持已读、分页和批量删除。
- 加密备份与导入恢复，支持同域名迁移和探针切换到新域名，详见[备份与迁移说明](docs/BACKUP-MIGRATION.md)。
- 购买留言与评论每台节点、每个 IP 在 5 分钟内最多 2 条；正文最多 100 字，联系方式最多 20 字。
- 指标分层降采样保存，磁盘占用与采集频率解耦（见下一节）。
- 按目标容量增加节点 SWAP；不支持减少 SWAP。
- Agent 只接受预先定义的结构化指令，不执行 Hub 下发的任意 Shell 命令或脚本。

## 社区与反馈

TG 群组：[Kokoro Panel](https://t.me/kokoropanel)

## 部署

### Hub 主机

安装器面向全新的 Debian 或 Ubuntu 主机，需要 root 权限、systemd，以及可用的 DNS 和公网 HTTP/HTTPS 入站连接。先将面板域名指向主机，并允许 TCP 80、443 入站；安装器不会修改系统防火墙。默认交互输入域名，也可用 `--domain` 指定：

```sh
curl -fsSL https://raw.githubusercontent.com/Vincentkeio/kokoro/main/scripts/install-hub.sh \
  | sudo sh
```

无交互终端时请显式传入域名：

```sh
curl -fsSL https://raw.githubusercontent.com/Vincentkeio/kokoro/main/scripts/install-hub.sh \
  | sudo sh -s -- --domain panel.example.com
```

首次安装可用 `--admin-user 用户名` 设置管理员用户名。交互安装时设置管理员密码；无交互安装会生成随机密码并在结束时显示一次，请立即保存。如果数据目录里已经有管理员账户（例如在保留数据的前提下重装），安装器会沿用原凭据，并明确提示"本次输入的密码没有生效"以及重置方法——不会再让安装摘要显示一个并未生效的用户名。

也可以从本地发布包安装，不访问 GitHub（制品和 `.sha256` 仍然逐个校验）：

```sh
sudo sh install-hub.sh --domain panel.example.com --assets-dir /path/to/release
```

安装器会安装 Hub、Caddy 和 `kokoro-hub` systemd 服务。Hub 只监听本机 `127.0.0.1:8799`，公网流量由反向代理接入：

- 没有 Nginx 占用 80/443 时，Caddy 负责 HTTPS 和面板反代，并长期提供面板服务。
- 检测到 Nginx（包括常见的宝塔配置目录）时，保留 Nginx 现有网站，只为 Kokoro 添加独立站点；Caddy 在本地备用端口申请和续期证书，续期后自动重载 Nginx。
- 若 80/443 被其他程序占用、无法识别安全的 Nginx 配置目录、域名已配置，或发现需要人工合并的自定义 Caddy 配置，安装器会停止以避免覆盖现有站点。它不会停止或接管 Nginx，也不会自动修改其他站点配置。
- 安装器自己写下的配置带 `Managed-By=Kokoro-Installer` 标记：卸载后重装时，这些残留（包括上一次的 `/etc/caddy/Caddyfile` 和 `/opt/kokoro/data` 链接）会被识别为 Kokoro 自己的产物并放行，不会把操作者挡在"检测到已有 Caddy 配置"之外。没有标记的配置仍然一律拒绝。
- 安装中途失败会把这次创建的服务单元、程序和 Caddy 配置收回，主机不会停在"装了一半、再也装不上"的状态。

安装目录为 `/opt/kokoro`，数据目录为 `/var/lib/kokoro`。安装完成后运行 `kokoro` 打开管理菜单；可用 `systemctl status kokoro-hub` 查看服务状态，用 `journalctl -u kokoro-hub -f` 查看实时日志。Nginx 模式下也可检查 `nginx` 和 `caddy` 服务。菜单里面板与探针的更新、卸载是分开的，且只显示本机实际装了的角色。三个更新选项都会自动检测当前角色是否有可用更新：没有更新时直接提示；有更新时询问是否立即更新，不让用户选择版本。「更新面板（Hub 主程序）」从 GitHub Release 获取制品并校验 SHA-256，替换 Hub 二进制后重启服务，健康检查失败自动还原旧程序和更新前数据库；「更新探针程序」只更新 Agent；「更新安装脚本」单独更新 Hub 提供给后续节点安装和升级的 `install.sh`，不替换 Hub 或 Agent 二进制。管理菜单中的「卸载面板」会询问是否**连数据一起删除**：选择彻底删除时会先关闭 Kokoro 托管的 SWAP 并清理 `/etc/fstab` 里的对应挂载行，再删数据目录（默认只卸载面板、保留数据）。卸载会一并收回安装器写下的反代配置——带 Kokoro 标记的 `/etc/caddy/Caddyfile`（随后停用 Caddy）、`kokoro-hub-*.conf` 形式的 Nginx 站点，以及 `kokoro-update`、`kokoro-nginx-reload` 单元；只删带标记的文件，别人的站点一律不动。面板只卸载了一半（服务没了、配置还在）时，菜单会多出「清理面板残留」一项把主机收拾干净。「卸载 Agent」只卸载子机探针，不会动面板及其数据。

安装器从 GitHub Release 下载 Hub、Linux amd64/arm64 Agent、Agent 安装脚本和固定测试脚本，并校验 SHA-256。目标主机不需要 Go 或编译工具；建议预留约 100 MB 临时空间和运行空间。可用 `scripts/build-release.ps1 -Version v版本号` 构建候选制品；确认下载包已公开后，再更新 [`scripts/install-hub.sh`](scripts/install-hub.sh) 中的 `RELEASE_TAG`，避免安装入口指向尚不存在的制品。

`scripts/deploy-zgo.ps1` 负责把本机构建好的发布包送到 zgo：面板已安装时按升级处理（备份数据库与程序，健康检查失败回滚），面板不存在时直接用同一份发布包现场安装，不需要再走一遍联网安装。`-Domain` 只在安装分支生效。

### Agent 子机

在 Hub 后台为子机生成一键安装命令，在目标 VPS 上以 root 执行。安装器从 Hub 下载经过校验的 Agent，写入配置并注册服务；Agent 开始上报后即可在面板中看到节点。

也可以使用安装令牌直接安装：

```sh
curl -fsSL https://<Hub域名>/i/<安装令牌> | sh
```

请从 Hub 后台复制实际生成的命令，不要把安装令牌提交到代码仓库或公开分享。在子机上以 root 运行 `kokoro`，选择“更新探针程序”；也可直接运行 `sh /usr/local/lib/kokoro/install.sh --upgrade`。这会更新 Agent，不会更新 Hub。Hub 主程序在面板主机上运行 `kokoro` 选择「更新面板」更新，也可继续用部署端脚本更新。发布包中的 Agent 安装脚本会将 GitHub 回退下载固定到对应发布标签，避免 Hub 分发暂时不可用时回退到旧正式版。Agent 安装入口见 [`scripts/install.sh`](scripts/install.sh)，部署单元示例见 [`deploy/`](deploy/)。

## 数据存储与保留

指标按**分层降采样（rollup ladder）**保存：写入量只跟"桶数 × 层数"有关，与采集频率解耦，所以提高上报频率不会再线性撑大数据库。级联方向 `metrics_raw → metrics_1m → metrics_5m → metrics_1h → metrics_1d`。

| 层 | 粒度 | 默认保留 | 主要用途 |
| --- | --- | --- | --- |
| `metrics_raw` | 每次上报 | 6 小时 | 详情页「近 1 小时」 |
| `metrics_1m` | 1 分钟 | 24 小时 | 「近 6 小时」 |
| `metrics_5m` | 5 分钟 | 7 天 | 「近 1 天」 |
| `metrics_1h` | 1 小时 | 45 天 | 「近 7/30 天」 |
| `metrics_1d` | 1 天 | 365 天 | 长期趋势 |
| `net_quality` | 每轮三网探测（默认 5 分钟一轮） | 7 天，且每节点最多 2000 轮 | 三网延迟波形 |

- 每层只聚合**已闭合**的窗口，各层有独立游标（`settings.agg_cursor_*`），因此重启不会重算或漏算。
- 上游是聚合表时，平均值按样本数加权，最大值取最大，避免"桶内样本数不同"把均值带偏。
- 后台节奏：**每 5 分钟**聚合一次，**每小时**按保留期清理一次（清理会做大范围删除，必要时回收空间，不挤在写路径上）。
- 详情页按跨度自动选层（≤2 小时读原始、≤12 小时读 1 分钟、≤3 天读 5 分钟、≤45 天读 1 小时，更久读 1 天）。
- 各层保留期可用 `settings` 覆盖：`retention_raw_h`、`retention_1m_h`、`retention_5m_h`、`retention_1h_d`、`retention_1d_d`、`retention_netq_d`（分别为小时/天，**负值表示不清理该层**）。⚠️ 调整保留期时要和详情页的选层阈值一起考虑，否则曲线头部会缺口。

默认保留下，四台节点的数据库实测约 **5 MiB**（迁移前那套「原始表 24 小时 + 5 分钟表 90 天 + 三网连续探测 30 天」在同样数据量下是 **200 MiB 以上**，且随天数继续增长）。

## 安全与资源占用

- Hub 与 Agent 都使用 Go 编写；Hub 页面资源随程序一起打包，无需额外安装前端运行环境。
- Agent 按内置白名单校验 Hub 下发的结构化指令类型及参数；测试仅允许 `bench`、`ipquality`、`netquality`，SWAP、配置和探测操作也只接受各自规定的参数。Agent 拒绝任意 Shell 文本、可执行程序、命令参数、脚本地址、工作目录和超时设置；测试由 Agent 固定流程调用版本及 SHA-256 均受校验的脚本。下载的程序和安装资源会校验 SHA-256。
- 管理员密码使用带随机盐的 PBKDF2-HMAC-SHA256 保存。Agent 默认通过 HTTPS 通信；Hub 服务以专用系统用户运行，并由 systemd 限制可写目录。
- Agent 需要 root 权限采集主机信息及执行 SWAP 等管理操作；其 systemd 服务启用文件系统隔离，并将常驻服务的可写范围限制在配置目录。

发布二进制的磁盘占用约为：Hub **15.6 MiB**；每台 Agent **15.6 MiB（amd64）**或 **14.7 MiB（arm64）**。Hub 为子机分发程序还需保存两种架构的 Agent 文件，Hub 与两份 Agent 二进制合计约 **45.9 MiB**；不含数据库、Caddy、系统依赖及运行数据。数据库本身的占用取决于保留期设置，默认约在**几十 MiB**量级。大小会随版本变化。

## 开发

项目使用 Go，入口在 `cmd/kokoro`；安装与 [`go.mod`](go.mod) 匹配的 Go 版本。常用命令：

```sh
go build ./cmd/kokoro
go test ./...
```

运行 `kokoro` 查看命令帮助。主要子命令包括 `serve`（启动 Hub）、`agent`（运行 Agent）和 `passwd`（管理唯一管理员账户）。

维护者可用 [`scripts/build-release.ps1`](scripts/build-release.ps1) 在本地构建 Linux amd64/arm64 发布资产（输出到 `dist/release/`），用于部署到自有服务器；推送 `v*` 标签会触发 GitHub Actions 自动构建并创建对应 Release。服务器部署和更新流程应在本地维护，避免将主机地址、SSH 配置或线上节点信息提交到仓库。

## 文档

- [`docs/SECURITY-AUDIT.md`](docs/SECURITY-AUDIT.md)：安全加固和信任边界。
- [`docs/API-CONTRACT.md`](docs/API-CONTRACT.md)：Hub 与 Agent 接口约定。
- [`docs/AGENT-COMMANDS.md`](docs/AGENT-COMMANDS.md)：Agent 指令和任务处理。
- [`docs/FEATURE-MATRIX.md`](docs/FEATURE-MATRIX.md)：功能清单。
- [`docs/THEME-FORMAT.md`](docs/THEME-FORMAT.md)：主题格式。

## 许可证

本项目采用 **GNU Affero General Public License v3.0（AGPL-3.0）**，Copyright (c) 2026 Vincentkeio。全文见 [`LICENSE`](LICENSE)。

AGPL 的义务：若你把修改后的 Kokoro 部署成网络服务供他人使用，需要向使用者提供对应的完整源码。
