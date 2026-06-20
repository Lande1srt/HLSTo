# Wails Build Script for Windows
# 此脚本用于构建 HLSTo Windows 桌面应用

Write-Host "========================================" -ForegroundColor Cyan
Write-Host "HLSTo Windows Build Script" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan

# 检查 Wails CLI
Write-Host "`nChecking Wails CLI..." -ForegroundColor Yellow
$wailsVersion = wails version 2>$null
if ($LASTEXITCODE -ne 0) {
    Write-Host "Wails not installed, installing..." -ForegroundColor Red
    $env:GOPROXY = "https://goproxy.cn,direct"
    git config --global url."https://ghproxy.com/".insteadOf https://github.com
    go install github.com/wailsapp/wails/v2/cmd/wails@latest
}
Write-Host "Wails version: $wailsVersion" -ForegroundColor Green

# 检查前端依赖
Write-Host "`nChecking frontend dependencies..." -ForegroundColor Yellow
if (-not (Test-Path "../frontend/node_modules")) {
    Write-Host "Installing frontend dependencies..." -ForegroundColor Yellow
    cd ../frontend
    npm install
    cd ../winui
}

# 构建前端应用
Write-Host "`nBuilding frontend..." -ForegroundColor Yellow
cd ../frontend
npm run build
if ($LASTEXITCODE -ne 0) {
    Write-Host "Frontend build failed!" -ForegroundColor Red
    exit 1
}
cd ../winui

# 复制静态文件
Write-Host "`nCopying static files..." -ForegroundColor Yellow
if (Test-Path "static") { Remove-Item -Recurse -Force static }
Copy-Item -Path "../backend/static" -Destination "static" -Recurse -Container

# 复制应用图标
Write-Host "`nCopying application icon..." -ForegroundColor Yellow
if (-not (Test-Path "build/windows")) { New-Item -ItemType Directory -Path "build/windows" -Force | Out-Null }
Copy-Item -Path "hlsto.ico" -Destination "build/windows/icon.ico" -Force
Write-Host "Icon applied: hlsto.ico -> build/windows/icon.ico" -ForegroundColor Green

# 构建应用，应用平台为 Windows/amd64
Write-Host "`nBuilding Windows application..." -ForegroundColor Yellow
wails build -platform windows/amd64
$buildExitCode = $LASTEXITCODE

if ($buildExitCode -eq 0) {
    Write-Host "`n========================================" -ForegroundColor Green
    Write-Host "Build successful!" -ForegroundColor Green
    Write-Host "Output directory: build/bin/" -ForegroundColor Green
    Write-Host "========================================" -ForegroundColor Green
} else {
    Write-Host "`nBuild failed! Exit code: $buildExitCode" -ForegroundColor Red
    exit 1
}
