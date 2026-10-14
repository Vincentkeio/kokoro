# Kokoro

Kokoro 是一个用于管理多台 VPS 的主控面板与轻量探针。Hub 部署在主机上，负责节点管理、任务下发和结果展示；Agent 安装在子机上，定期上报状态并执行 Hub 下发的固定任务。

> **版本状态：Alpha 基准版。** 当前代码作为 Alpha 测试基线，适合在可恢复的测试环境中试用并反馈问题。功能、配置和接口仍可能调整；重要数据请先备份。

## 功能

- 汇总 VPS 在线状态、硬件信息、网络流量和延迟，并支持节点分组与筛选。
- 通过任务队列下发硬件跑分、IP 质量与解锁、网络测速及三网回程检测。
- 查看任务进度、历史结果和原始输出。
- 按目标容量增加节点 SWAP；不支持减少 SWAP。
- Agent 只接受预先定义的结构化指令，不执行 Hub 下发的任意 Shell 命令或脚本。

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

首次安装可用 `--admin-user 用户名` 设置管理员用户名。交互安装时设置管理员密码；无交互安装会生成随机密码并在结束时显示一次，请立即保存。

安装器会安装 Hub、Caddy 和 `kokoro-hub` systemd 服务。Hub 只监听本机 `127.0.0.1:8799`，公网流量由反向代理接入：

- 没有 Nginx 占用 80/443 时，Caddy 负责 HTTPS 和面板反代，并长期提供面板服务。
- 检测到 Nginx（包括常见的宝塔配置目录）时，保留 Nginx 现有网站，只为 Kokoro 添加独立站点；Caddy 在本地备用端口申请和续期证书，续期后自动重载 Nginx。
- 若 80/443 被其他程序占用、无法识别安全的 Nginx 配置目录、域名已配置，或发现需要人工合并的自定义 Caddy 配置，安装器会停止以避免覆盖现有站点。它不会停止或接管 Nginx，也不会自动修改其他站点配置。

安装目录为 `/opt/kokoro`，数据目录为 `/var/lib/kokoro`。安装完成后运行 `kokoro` 打开管理菜单；可用 `systemctl status kokoro-hub` 查看服务状态，用 `journalctl -u kokoro-hub -f` 查看实时日志。Nginx 模式下也可检查 `nginx` 和 `caddy` 服务。

安装器从 GitHub Release 下载 Hub、Linux amd64/arm64 Agent、Agent 安装脚本和固定测试脚本，并校验 SHA-256。目标主机不需要 Go 或编译工具；建议预留约 100 MB 临时空间和运行空间。Release 资产版本由 [`scripts/install-hub.sh`](scripts/install-hub.sh) 中的 `RELEASE_TAG` 指定，安装脚本与 Release 资产应配套发布。

### Agent 子机

在 Hub 后台为子机生成一键安装命令，在目标 VPS 上以 root 执行。安装器从 Hub 下载经过校验的 Agent，写入配置并注册服务；Agent 开始上报后即可在面板中看到节点。

也可以使用安装令牌直接安装：

```sh
curl -fsSL https://<Hub域名>/i/<安装令牌> | sh
```

请从 Hub 后台复制实际生成的命令，不要把安装令牌提交到代码仓库或公开分享。管理菜单中的“更新探针程序”更新 Agent 及其本地管理安装脚本；Hub 主程序需通过部署端更新。Agent 安装入口见 [`scripts/install.sh`](scripts/install.sh)，部署单元示例见 [`deploy/`](deploy/)。

## 安全与资源占用

- Hub 与 Agent 都使用 Go 编写；Hub 页面资源随程序一起打包，无需额外安装前端运行环境。
- Agent 按内置白名单校验 Hub 下发的结构化指令类型及参数；测试仅允许 `bench`、`ipquality`、`netquality`，SWAP、配置和探测操作也只接受各自规定的参数。Agent 拒绝任意 Shell 文本、可执行程序、命令参数、脚本地址、工作目录和超时设置；测试由 Agent 固定流程调用版本及 SHA-256 均受校验的脚本。下载的程序和安装资源会校验 SHA-256。
- 管理员密码使用带随机盐的 PBKDF2-HMAC-SHA256 保存。Agent 默认通过 HTTPS 通信；Hub 服务以专用系统用户运行，并由 systemd 限制可写目录。
- Agent 需要 root 权限采集主机信息及执行 SWAP 等管理操作；其 systemd 服务启用文件系统隔离，并将常驻服务的可写范围限制在配置目录。

发布二进制的磁盘占用约为：Hub **16.5 MiB**；每台 Agent **16.5 MiB（amd64）**或 **15.5 MiB（arm64）**。Hub 为子机分发程序还需保存两种架构的 Agent 文件，Hub 与两份 Agent 二进制合计约 **48.5 MiB**；不含数据库、Caddy、系统依赖及运行数据。大小会随版本变化。

## 开发

项目使用 Go，入口在 `cmd/kokoro`；安装与 [`go.mod`](go.mod) 匹配的 Go 版本。常用命令：

```sh
go build ./cmd/kokoro
go test ./...
```

运行 `kokoro` 查看命令帮助。主要子命令包括 `serve`（启动 Hub）、`agent`（运行 Agent）和 `passwd`（管理唯一管理员账户）。

维护者可使用 [`scripts/build-release.ps1`](scripts/build-release.ps1) 构建 Linux amd64/arm64 发布资产，并将生成的 `dist/release/` 文件上传至对应 GitHub Release。服务器部署和更新流程应在本地维护，避免将主机地址、SSH 配置或线上节点信息提交到仓库。

## 文档

- [`docs/SECURITY-AUDIT.md`](docs/SECURITY-AUDIT.md)：安全加固和信任边界。
- [`docs/API-CONTRACT.md`](docs/API-CONTRACT.md)：Hub 与 Agent 接口约定。
- [`docs/AGENT-COMMANDS.md`](docs/AGENT-COMMANDS.md)：Agent 指令和任务处理。
- [`docs/FEATURE-MATRIX.md`](docs/FEATURE-MATRIX.md)：功能清单。
- [`docs/THEME-FORMAT.md`](docs/THEME-FORMAT.md)：主题格式。
