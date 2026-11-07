param(
    [string]$Version = "1.1.0",
    [string]$Out = "readtrace.exe"
)

$ErrorActionPreference = "Stop"
$go = Get-Command go -ErrorAction SilentlyContinue
if (-not $go) {
    $candidate = Join-Path $env:LOCALAPPDATA "Programs\go\bin\go.exe"
    if (Test-Path $candidate) { $go = $candidate } else { throw "未找到 go，请先安装 Go 1.26+" }
}

& $go build -trimpath -ldflags "-s -w -X main.version=$Version" -o $Out ./cmd/readtrace
if ($LASTEXITCODE -ne 0) { throw "构建失败" }
Write-Host "已生成 $Out（版本 $Version）" -ForegroundColor Green
Write-Host "运行： .\$Out" -ForegroundColor Cyan
