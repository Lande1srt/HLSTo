# HLSTo Windows 桌面应用

基于 [Wails](https://wails.io/) 框架构建的 Windows 桌面应用，集成了 M3U8 视频下载器的完整功能。

## 功能特点

- 🚀 一体化启动前后端，无需手动打开浏览器
- 🎨 现代化深色主题界面
- 📥 支持 M3U8/HLS 视频下载
- 🔐 WebDAV 上传到远程网盘
- 💾 SQLite 本地数据库存储
- 🔒 任务暂停/恢复/停止/重试

## 快速开始

### 1. 初始化（首次使用）

```powershell
.\Init-Wails.ps1
```

这将安装 Wails CLI 和所有必要的依赖。

### 2. 开发模式

```powershell
.\Dev-WinUI.ps1
```

应用将在窗口中启动，支持热重载。

### 3. 构建生产版本

```powershell
.\Build-WinUI.ps1
```

构建完成后，可执行文件位于 `build/bin/` 目录。

## 系统要求

- Windows 10/11
- Go 1.21+
- Node.js 18+
- Wails CLI

## 项目结构

```
winui/
├── main.go           # Wails 入口
├── app.go           # 应用逻辑
├── wails.json       # Wails 配置
├── go.mod          # Go 模块
├── frontend/       # 前端代码（链接到 ../frontend）
├── build/          # 构建脚本
└── *.ps1           # PowerShell 脚本
```

## 注意事项

- 前端代码复用 `../frontend` 目录
- 后端代码复用 `../backend` 目录
- 配置文件使用项目根目录的 `.env` 文件
