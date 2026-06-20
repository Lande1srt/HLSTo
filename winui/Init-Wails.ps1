# Wails 初始化脚本 - 首次使用前运行
# 此脚本用于初始化 Wails 项目并下载所有依赖

$ErrorActionPreference = "Stop"

Write-Host "========================================" -ForegroundColor Cyan
Write-Host "HLSTo Wails 初始化脚本" -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan

# 检查 Go 环境
Write-Host "`n检查 Go 环境..." -ForegroundColor Yellow
$goVersion = go version 2>$null
if ($LASTEXITCODE -ne 0) {
    Write-Host "Go 未安装或未配置，请先安装 Go 1.21+" -ForegroundColor Red
    exit 1
}
Write-Host "Go 版本: $goVersion" -ForegroundColor Green

# 检查 Node.js 环境
Write-Host "`n检查 Node.js 环境..." -ForegroundColor Yellow
$nodeVersion = node --version 2>$null
if ($LASTEXITCODE -ne 0) {
    Write-Host "Node.js 未安装或未配置，请先安装 Node.js 18+" -ForegroundColor Red
    exit 1
}
Write-Host "Node.js 版本: $nodeVersion" -ForegroundColor Green

# 安装 Wails CLI
Write-Host "`n安装 Wails CLI..." -ForegroundColor Yellow
go install github.com/wailsapp/wails/v2/cmd/wails@latest
if ($LASTEXITCODE -ne 0) {
    Write-Host "Wails CLI 安装失败" -ForegroundColor Red
    exit 1
}
Write-Host "Wails CLI 安装成功" -ForegroundColor Green

# 下载 Go 依赖
Write-Host "`n下载 Go 依赖..." -ForegroundColor Yellow
go mod download

# 安装前端依赖
Write-Host "`n安装前端依赖..." -ForegroundColor Yellow
cd frontend
npm install
if ($LASTEXITCODE -ne 0) {
    Write-Host "前端依赖安装失败" -ForegroundColor Red
    cd ..
    exit 1
}
cd ..

Write-Host "`n========================================" -ForegroundColor Green
Write-Host "初始化完成！" -ForegroundColor Green
Write-Host "========================================" -ForegroundColor Green
Write-Host "`n运行以下命令启动应用:" -ForegroundColor Yellow
Write-Host "  开发模式: .\Dev-WinUI.ps1" -ForegroundColor Cyan
Write-Host "  构建生产版本: .\Build-WinUI.ps1" -ForegroundColor Cyan
