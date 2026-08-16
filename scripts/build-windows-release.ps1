param(
  [Parameter(Mandatory = $true)][ValidateSet('amd64', 'arm64')][string]$Architecture,
  [Parameter(Mandatory = $true)][ValidatePattern('^\d+\.\d+\.\d+$')][string]$Version
)
$ErrorActionPreference = 'Stop'
$env:GOPROXY = 'https://goproxy.cn'
$env:GOSUMDB = 'sum.golang.google.cn'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

pnpm.cmd --filter '@deskpatrol/client' build
if ($LASTEXITCODE -ne 0) { throw "客户端前端构建失败，exit=$LASTEXITCODE" }

$assets = Join-Path $repoRoot 'cmd\client\assets'
Get-ChildItem -LiteralPath $assets -Force | Where-Object { $_.Name -ne 'placeholder.txt' } | Remove-Item -Recurse -Force
Copy-Item -Path (Join-Path $repoRoot 'frontend\apps\client\dist\*') -Destination $assets -Recurse -Force

$stage = Join-Path $repoRoot "dist\windows\$Architecture\stage"
New-Item -ItemType Directory -Path $stage -Force | Out-Null
$env:GOOS = 'windows'
$env:GOARCH = $Architecture
$env:CGO_ENABLED = '0'
go build -trimpath -ldflags "-s -w -H windowsgui -X deskpatrol/internal/buildinfo.Version=$Version" -o (Join-Path $stage 'DeskPatrol.exe') ./cmd/client
if ($LASTEXITCODE -ne 0) { throw "Wails 客户端构建失败，exit=$LASTEXITCODE" }
go build -trimpath -ldflags "-s -w -H windowsgui -X deskpatrol/internal/buildinfo.Version=$Version" -o (Join-Path $stage 'DeskPatrolHelper.exe') ./cmd/client-helper
if ($LASTEXITCODE -ne 0) { throw "提权 helper 构建失败，exit=$LASTEXITCODE" }

$iscc = Join-Path ${env:ProgramFiles(x86)} 'Inno Setup 6\ISCC.exe'
if (-not (Test-Path -LiteralPath $iscc)) { throw "未找到 Inno Setup 6：$iscc" }
$output = Join-Path $repoRoot "dist\windows\$Architecture"
& $iscc "/DMyVersion=$Version" "/DMyArch=$Architecture" "/DSourceDir=$stage" "/DOutputDir=$output" (Join-Path $repoRoot 'deploy\windows\DeskPatrol.iss')
if ($LASTEXITCODE -ne 0) { throw "Inno Setup 构建失败，exit=$LASTEXITCODE" }
