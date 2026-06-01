# M3U8 下载器 Web UI

一个现代化的 M3U8 视频下载器 Web 界面，基于 Vue 3 + Go 构建的**一体化应用**。

## ✨ 核心特性

- 🎬 **支持下载 M3U8 格式的视频**
- 🔐 **支持 AES-128-CBC 加密视频解密**
- ⚡ **多线程下载（可配置线程数 1-100）**
- 📊 **实时进度显示（WebSocket 推送）**
- 🌐 **一体化部署（前端 + 后端）**
- 💾 **任务历史记录管理**
- ⚙️ **可配置的下载参数**
- 🎨 **现代化深色主题 UI**

## 🚀 快速开始

### 1. 环境检查
首先运行环境检查脚本，确保 Node.js 和 Go 已正确安装：
```powershell
./Check-Env.ps1
```

### 2. 本地开发
使用启动脚本进入开发模式，支持前后端热更新：
```powershell
./Start-App.ps1
```
选择 `1. 开发模式`。

### 3. 生产构建与部署
若要部署到服务器，请先在本地编译前端资源：
```powershell
./Build-Project.ps1
```
执行完成后，前端静态资源将自动编译至 `backend/static` 目录。

**部署流程：**
1. 仅需将 `backend` 目录上传至服务器。
2. 在服务器上进入 `backend` 目录。
3. 执行编译（Linux 示例）：
   ```bash
   CGO_ENABLED=1 go build -o m3u8-downloader-web main.go
   ```
4. 运行生成的可执行文件即可。

## 📁 项目结构

```
m3u8-downloader-web/
├── frontend/                    # Vue 3 前端源码
│   └── ...
├── backend/                     # Go 后端服务 (部署时仅需此目录)
│   ├── static/                 # 前端构建输出 (自动生成)
│   ├── handler/                # HTTP 处理器
│   ├── service/                # 业务逻辑层
│   ├── websocket/              # WebSocket 处理
│   ├── model/                  # 数据模型
│   └── main.go                 # 服务入口
│
├── Check-Env.ps1               # 环境检查脚本
├── Build-Project.ps1           # 生产构建准备脚本
└── Start-App.ps1               # 启动控制脚本
```

## 🏗️ 架构设计

### 一体化架构

```
┌─────────────────────────────────┐
│         用户浏览器               │
└──────────────┬──────────────────┘
               │
               ▼
┌─────────────────────────────────┐
│      Go HTTP Server :8080       │
│  ┌─────────────────────────┐   │
│  │    API 路由 (/api/*)     │   │
│  │    WebSocket (/ws)       │   │
│  │    SPA 路由 (/*)         │   │
│  └─────────────────────────┘   │
└──────────────┬──────────────────┘
               │
       ┌───────┴───────┐
       ▼               ▼
┌─────────────┐  ┌──────────────┐
│  API 处理器  │  │ 静态文件服务  │
│  (REST API) │  │ (Vue 构建)   │
└─────────────┘  └──────────────┘
```

##  配置说明

### 环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `PORT` | `8080` | 服务器监听端口 |
| `AUTH_USERNAME` | - | 设置以启用 Basic Auth |
| `AUTH_PASSWORD` | - | 设置以启用 Basic Auth |

## 📦 Docker 部署

项目根目录提供 `Dockerfile`，支持一键容器化部署：

```bash
docker build -t m3u8-downloader-web .
docker run -d -p 8080:8080 --name m3u8-downloader m3u8-downloader-web
```
