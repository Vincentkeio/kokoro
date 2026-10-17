param(
    [string]$Version = '',
    [string]$GoPath = 'go',
    [string]$OutputDirectory = 'dist\release'
)

$ErrorActionPreference = 'Stop'
$installerPath = Join-Path $PSScriptRoot 'install-hub.sh'
$installer = Get-Content -LiteralPath $installerPath -Raw
$tagMatch = [regex]::Match($installer, 'RELEASE_TAG="([^"]+)"')
if ([string]::IsNullOrWhiteSpace($Version) -and $tagMatch.Success) { $Version = $tagMatch.Groups[1].Value }
if ($Version -notmatch '^(v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?|alpha[0-9]+(\.[0-9]+)+)$') {
    throw "Invalid release version: $Version"
}
if (-not $tagMatch.Success) {
    throw "Could not find RELEASE_TAG in scripts/install-hub.sh"
}

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$outputPath = Join-Path $repoRoot $OutputDirectory
New-Item -ItemType Directory -Force -Path $outputPath | Out-Null
$assetNames = @(
    'kokoro-linux-amd64', 'kokoro-agent-linux-amd64',
    'kokoro-linux-arm64', 'kokoro-agent-linux-arm64',
    'kokoro-bench.sh', 'install.sh'
)
foreach ($assetName in $assetNames) {
    Remove-Item -LiteralPath (Join-Path $outputPath $assetName) -Force -ErrorAction SilentlyContinue
    Remove-Item -LiteralPath (Join-Path $outputPath "$assetName.sha256") -Force -ErrorAction SilentlyContinue
}
$env:GOOS = 'linux'
$env:CGO_ENABLED = '0'
Push-Location $repoRoot
try {

function Write-Checksum([string]$Path) {
    $hash = (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
    [IO.File]::WriteAllText("$Path.sha256", "$hash  $(Split-Path -Leaf $Path)`n", [Text.Encoding]::ASCII)
}

foreach ($arch in @('amd64', 'arm64')) {
    $hubAsset = "kokoro-linux-$arch"
    $agentAsset = "kokoro-agent-linux-$arch"
    $env:GOARCH = $arch
    & $GoPath build -trimpath -ldflags "-s -w -X main.Version=$Version" -o (Join-Path $outputPath $hubAsset) ./cmd/kokoro
    if ($LASTEXITCODE -ne 0) { throw "Hub build failed for linux/$arch" }
    Copy-Item -LiteralPath (Join-Path $outputPath $hubAsset) -Destination (Join-Path $outputPath $agentAsset) -Force
    Write-Checksum (Join-Path $outputPath $hubAsset)
    Write-Checksum (Join-Path $outputPath $agentAsset)
}

$agentSource = Get-Content -LiteralPath (Join-Path $repoRoot 'internal\agent\test_command.go') -Raw
$hashMatch = [regex]::Match($agentSource, 'const testToolSHA256 = "([0-9a-f]{64})"')
if (-not $hashMatch.Success) { throw 'Could not find the pinned test script SHA-256' }
$benchPath = Join-Path $outputPath 'kokoro-bench.sh'
Copy-Item -LiteralPath (Join-Path $repoRoot 'scripts\kokoro-bench.sh') -Destination $benchPath -Force
$benchHash = (Get-FileHash -LiteralPath $benchPath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($benchHash -ne $hashMatch.Groups[1].Value) { throw 'Downloaded test script does not match the Agent pinned SHA-256' }
Write-Checksum $benchPath

$installPath = Join-Path $outputPath 'install.sh'
Copy-Item -LiteralPath (Join-Path $repoRoot 'scripts\install.sh') -Destination $installPath -Force
$installSource = Get-Content -LiteralPath $installPath -Raw
$latestTagDefault = 'GH_TAG="${GH_TAG:-latest}"'
if (-not $installSource.Contains($latestTagDefault)) {
    throw 'Could not find the default GH_TAG in install.sh'
}
$pinnedTagDefault = 'GH_TAG="${GH_TAG:-' + $Version + '}"'
$installSource = $installSource.Replace($latestTagDefault, $pinnedTagDefault)
[IO.File]::WriteAllText($installPath, $installSource, [Text.UTF8Encoding]::new($false))
if (-not (Select-String -LiteralPath $installPath -SimpleMatch $pinnedTagDefault -Quiet)) {
    throw "Release install.sh is not pinned to $Version"
}
Write-Checksum $installPath

Write-Output "Release assets built in $outputPath"
Get-ChildItem -LiteralPath $outputPath -File | Select-Object Name, Length
} finally {
    Pop-Location
}
