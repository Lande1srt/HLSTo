# 强制设置控制台输出为 UTF-8 以解决中文乱码问题
$OutputEncoding = [System.Text.Encoding]::UTF8
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

Write-Host "========================================" -ForegroundColor Cyan
Write-Host "M3U8 Downloader Web UI 一体化启动 " -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

$rootPath = $PSScriptRoot
if (-not $rootPath) { $rootPath = Get-Location }

# 1. 构建前端资源
Write-Host "[1/2] 正在编译前端资源... " -ForegroundColor Yellow
Set-Location "$rootPath\frontend"

if (-not (Test-Path "node_modules")) {
    Write-Host "检测到未安装依赖，正在安装... "
    npm install
}

npm run build
if ($LASTEXITCODE -ne 0) {
    Write-Host "[错误] 前端构建失败 " -ForegroundColor Red
    Set-Location $rootPath
    exit 1
}
Write-Host "[OK] 前端资源编译完成 " -ForegroundColor Green

# 2. 启动后端服务
Write-Host "`n[2/2] 正在启动后端服务... " -ForegroundColor Yellow
Set-Location "$rootPath\backend"

Write-Host "----------------------------------------" -ForegroundColor DarkGray
Write-Host "服务已启动！ " -ForegroundColor Cyan
Write-Host "访问地址: http://localhost:8080 " -ForegroundColor Cyan

if ($env:AUTH_USERNAME -and $env:AUTH_PASSWORD) {
    Write-Host "安全模式: 已开启 (账号: $env:AUTH_USERNAME) " -ForegroundColor Yellow
} elseif ((Test-Path "$rootPath\.env") -or (Test-Path "$rootPath\backend\.env")) {
    Write-Host "安全模式: 检测到 .env 配置文件 " -ForegroundColor Yellow
} else {
    Write-Host "安全模式: 未开启 (可在根目录创建 .env 文件或设置环境变量以启用) " -ForegroundColor Gray
}

Write-Host "按 Ctrl+C 停止服务 " -ForegroundColor Gray
Write-Host "----------------------------------------" -ForegroundColor DarkGray""

go run main.go

Set-Location $rootPath
