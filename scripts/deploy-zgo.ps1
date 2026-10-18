param(
    [string]$ReleaseDirectory = 'dist\release',
    # 只在面板尚未安装时才用得到：安装分支用它作为面板域名与管理员的默认用户名。
    [string]$Domain = 'kokoro.mjfuns.lat',
    [string]$AdminUser = 'admin'
)

$ErrorActionPreference = 'Stop'
if ($Domain -notmatch '^[A-Za-z0-9.-]+$' -or $AdminUser -notmatch '^[A-Za-z0-9_.@-]+$') {
    throw '域名或管理员用户名含有不允许的字符。'
}
$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$releasePath = Join-Path $repoRoot $ReleaseDirectory
$assetNames = @(
    'kokoro-linux-amd64', 'kokoro-linux-amd64.sha256',
    'install.sh', 'install.sh.sha256', 'kokoro-bench.sh', 'kokoro-bench.sh.sha256'
)
foreach ($name in $assetNames) {
    if (-not (Test-Path -LiteralPath (Join-Path $releasePath $name) -PathType Leaf)) {
        throw "缺少发布文件：$name；请先构建 preview 发布包。"
    }
}

$notePath = 'D:\我的电脑\AGENTS.md'
$note = Get-Content -LiteralPath $notePath -Raw -Encoding UTF8
$sectionStart = $note.IndexOf('## zgo cloud VPS SSH 配置')
if ($sectionStart -lt 0) { throw '未找到 zgo 配置段' }
$sectionEnd = $note.IndexOf("`n## ", $sectionStart + 1)
if ($sectionEnd -lt 0) { $sectionEnd = $note.Length }
$section = $note.Substring($sectionStart, $sectionEnd - $sectionStart)
$zgoHost = [regex]::Match($section, '(?m)^- 地址：`([^`]+)`').Groups[1].Value.Split(' ')[0]
$zgoPort = [regex]::Match($section, '(?m)^- SSH 端口：`(\d+)`').Groups[1].Value
$password = [regex]::Match($section, '(?m)^- 密码：`([^`]+)`').Groups[1].Value.Trim()
if ($zgoHost -ne '64.83.30.108' -or $zgoPort -ne '35003' -or -not $password) {
    throw 'zgo 目标地址或登录配置不匹配，停止部署。'
}

$stage = Join-Path $env:TEMP "kokoro-zgo-preview-$([guid]::NewGuid().ToString('N'))"
$archive = "$stage.zip"
New-Item -ItemType Directory -Force -Path $stage | Out-Null
try {
    foreach ($name in $assetNames) {
        Copy-Item -LiteralPath (Join-Path $releasePath $name) -Destination $stage
    }
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'deploy-zgo-remote.sh') -Destination $stage
    # 安装分支要用官方安装器；远端脚本从这里读域名和管理员用户名。
    # 走文件而不是往远端命令里拼字符串：PowerShell 传长命令会被拆词，
    # here-string 里的 $() 还会被本地抢先展开。
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot 'install-hub.sh') -Destination $stage
    [IO.File]::WriteAllText(
        (Join-Path $stage 'deploy.env'),
        "KOKORO_DOMAIN=$Domain`nKOKORO_ADMIN_USER=$AdminUser`n",
        [Text.Encoding]::ASCII
    )
    Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $archive -CompressionLevel Optimal

    $env:ZGO_HOST = $zgoHost
    $env:ZGO_PORT = $zgoPort
    $env:ZGO_PASS = $password
    $env:DISPLAY = ':0'
    & bash (Join-Path $PSScriptRoot 'zgo-scp.sh') $archive '/root/kokoro-preview-release.zip'
    if ($LASTEXITCODE -ne 0) { throw '向 zgo 上传发布包失败。' }

    $remote = @'
set -euo pipefail
test "$(hostname)" = alarmed-curve
python3 -m zipfile -e /root/kokoro-preview-release.zip /root
rm -f /root/kokoro-preview-release.zip
bash /root/deploy-zgo-remote.sh
rm -f /root/deploy-zgo-remote.sh
'@
    $remote | & bash (Join-Path $PSScriptRoot 'zgo-ssh.sh')
    if ($LASTEXITCODE -ne 0) { throw 'zgo 发布连接中断；请先检查 Hub 状态，再决定是否重试。' }
} finally {
    Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath $archive -Force -ErrorAction SilentlyContinue
    Remove-Item Env:ZGO_PASS,Env:ZGO_HOST,Env:ZGO_PORT,Env:DISPLAY -ErrorAction SilentlyContinue
}
