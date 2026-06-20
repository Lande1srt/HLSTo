# Wails Dev Script for Windows
# 此脚本用于开发模式下启动 HLSTo Windows 桌面应用

Write-Host "========================================" -ForegroundColor Cyan
Write-Host "HLSTo Windows 开发模式" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan

# 检查 Wails CLI
Write-Host "`n检查 Wails CLI..." -ForegroundColor Yellow
$wailsVersion = wails version 2>$null
if ($LASTEXITCODE -ne 0) {
    Write-Host "Wails 未安装，正在安装..." -ForegroundColor Red
    $env:GOPROXY = "https://goproxy.cn,direct"
    git config --global url."https://ghproxy.com/".insteadOf https://github.com
    go install github.com/wailsapp/wails/v2/cmd/wails@latest
}

# 检查前端依赖
Write-Host "`n检查前端依赖..." -ForegroundColor Yellow
if (-not (Test-Path "../frontend/node_modules")) {
    Write-Host "安装前端依赖..." -ForegroundColor Yellow
    cd ../frontend
    npm install
    cd ../winui
}

# 检查静态文件
Write-Host "`n检查静态文件..." -ForegroundColor Yellow
if (-not (Test-Path "static/index.html")) {
    Write-Host "复制静态文件..." -ForegroundColor Yellow
    if (Test-Path "static") { Remove-Item -Recurse -Force static }
    Copy-Item -Path "../backend/static" -Destination "static" -Recurse -Container
}

# 检查应用图标
Write-Host "`n检查应用图标..." -ForegroundColor Yellow
if (-not (Test-Path "build/windows")) { New-Item -ItemType Directory -Path "build/windows" -Force | Out-Null }
Copy-Item -Path "hlsto.ico" -Destination "build/windows/icon.ico" -Force
Write-Host "图标已应用: hlsto.ico -> build/windows/icon.ico" -ForegroundColor Green

# 启动开发模式
Write-Host "`n启动 Wails 开发模式..." -ForegroundColor Yellow
Write-Host "应用将在窗口中启动" -ForegroundColor Yellow
wails dev
