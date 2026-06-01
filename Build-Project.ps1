# 强制设置控制台输出为 UTF-8 以解决中文乱码问题
$OutputEncoding = [System.Text.Encoding]::UTF8
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

Write-Host "========================================" -ForegroundColor Cyan
Write-Host "M3U8 Downloader Web UI 生产构建 " -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host ""

# 使用 $PSScriptRoot 获取脚本所在目录，确保在任何路径下执行都能找到正确文件
$rootPath = $PSScriptRoot
if (-not $rootPath) { $rootPath = Get-Location }

# 1. Build Frontend
Write-Host "[1/2] 构建前端资源... " -ForegroundColor Yellow
Set-Location "$rootPath\frontend"
if (-not (Test-Path "node_modules")) {
    Write-Host "正在安装前端依赖... "
    npm install
}
npm run build
if ($LASTEXITCODE -ne 0) {
    Write-Host "[错误] 前端构建失败 " -ForegroundColor Red
    Set-Location $rootPath
    exit 1
}
Write-Host "[OK] 前端资源已编译至 backend/static " -ForegroundColor Green

# 2. Build Backend (Download dependencies)
Write-Host "`n[2/2] 准备后端目录... " -ForegroundColor Yellow
Set-Location "$rootPath\backend"
Write-Host "正在下载后端依赖... "
go mod download
if ($LASTEXITCODE -ne 0) {
    Write-Host "[错误] 后端依赖下载失败 " -ForegroundColor Red
    Set-Location $rootPath
    exit 1
}

Write-Host "`n========================================" -ForegroundColor Cyan
Write-Host "本地构建准备完成！ " -ForegroundColor Cyan
Write-Host "========================================" -ForegroundColor Cyan
Write-Host "提示: 您现在只需将 'backend' 目录上传到服务器。 "
Write-Host "      然后在服务器上进入 'backend' 目录，运行编译命令即可： "
Write-Host "      CGO_ENABLED=1 go build -o m3u8-downloader-web main.go "
Write-Host ""
Set-Location $rootPath
