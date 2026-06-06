# HLSTo - M3U8 Downloader Web UI

一个现代化的 M3U8 视频下载器 Web 界面，基于 **Vue 3 + Go** 构建的一体化应用。
其后端基于开源项目 [m3u8-downloader](https://github.com/llychao/m3u8-downloader)进行二次开发，且作修改以部署到Linux服务端下载转发到对应WebDAV服务器（现代网盘主流支持），避免本地下载再上传的尴尬处境。
且基于原来版本做了伪后缀混淆识别，某些视频网站将m3u8内的ts文件后缀改为jpeg与其他后缀，导致常规只识别ts后缀的下载器无法正常识别下载。

> 可以下载岛国小电影<br/>可以下载岛国小电影<br/>可以下载岛国小电影<br/>重要的视频说三遍！！！

>需要你有一台对国内连接友好的服务器（不然光速下载，然后龟速中转到国内webdav服务器，如果你是海外存储那可以忽略此小节），不推荐国内需要备案的服务器，小心被请去喝茶，建议使用国外的大陆网络优化服务器，下载后的资源仅自己观赏，避免产生对应法律问题。

本工具仅提供基于学术研究的相关下载功能，如由使用者产生相关法律问题与此工具及相关开发者无关。

## 下载流程

关于m3u8地址可以通过某些下载器或浏览器扩展抓取（常用）。

```
开始下载 → 解析 M3U8 → 获取加密密钥 → 多线程下载分片 → 
文件完整性检查 → TS 转 MP4 封装 → [可选] WebDAV 上传 → 完成
```

## 核心特性

- **兼容识别下载部分普通视频类型（如mp4下载）**
- **支持HLS标准 AES-128-CBC 加密视频解密**
- **支持 MP4/MKV 等通用视频直链直接下载并中转**
- **支持伪后缀混淆识别下载**
- **支持伪装Referer 与 cookie 以绕过某些视频网站的反爬检测**
- **多线程下载（默认24线程）**
- **实时状态显示与进度更新（WebSocket 推送）**
- **一体化部署（前端 + 后端）**
- **任务历史记录管理**
- **可配置的下载参数**
- **现代化深色主题 UI**
- **WebDAV 自动上传支持**
- **支持任务暂停/恢复/停止/重试**
- **支持自动清除下载缓存文件**

## 快速开始

### 1. 环境要求

- Go 1.20+
- Node.js 18+

### 2. 安装依赖

**前端依赖：**

```powershell
cd frontend
npm install
```

**后端依赖：**

```powershell
cd backend
go mod download
```

### 3. 生产构建与部署


#### 启动开发服务器

```powershell
# 前端开发模式（端口 3000）
cd frontend
npm run dev

# 后端开发模式（端口 8080）
cd backend
go run main.go
``` 
后端启动后会输出编译的静态入口地址与api地址，可以支持前端实时调试与一体化部署

#### 部署到服务器

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

## API 接口

| 接口                          | 方法        | 描述           |
| --------------------------- | --------- | ------------ |
| `/api/auth/login`           | POST      | 用户登录         |
| `/api/auth/check`           | GET       | 检查认证状态       |
| `/api/download/start`       | POST      | 开始下载任务       |
| `/api/download/stop`        | POST      | 停止下载任务       |
| `/api/download/pause`       | POST      | 暂停下载任务       |
| `/api/download/resume`      | POST      | 恢复下载任务       |
| `/api/download/retry`       | POST      | 重试下载任务       |
| `/api/download/upload`      | POST      | 上传到 WebDAV   |
| `/api/download/analyze`     | POST      | 分析 M3U8 链接   |
| `/api/tasks`                | GET       | 获取任务列表       |
| `/api/tasks/{id}`           | GET       | 获取单个任务       |
| `/api/tasks/{id}`           | DELETE    | 删除任务         |
| `/api/settings`             | GET/POST  | 获取/保存设置      |
| `/api/settings/webdav/test` | POST      | 测试 WebDAV 连接 |
| `/ws`                       | WebSocket | 实时进度推送       |

## 配置说明

### 环境变量

复制 `.env.example` 为 `.env` 并修改配置：

| 变量              | 默认值    | 说明           |
| --------------- | ------ | ------------ |
| `PORT`          | `8080` | 服务器监听端口      |
| `AUTH_USERNAME` | -      | 设置以启用登录验证    |
| `AUTH_PASSWORD` | -      | 设置以启用登录验证    |
| `JWT_SECRET`    | -      | JWT 密钥（建议设置） |

## Docker 部署

项目根目录提供 `Dockerfile`，支持一键容器化部署：

```bash
docker build -t m3u8-downloader-web .
docker run -d -p 8080:8080 --name m3u8-downloader m3u8-downloader-web
```

## 功能说明

### 下载参数

- **线程数**: 默认24线程，控制并发下载数量
- **输出名称**: 自定义视频文件名
- **Host 类型**: 选择 URL 解析方式（v1/v2）
- **Cookie**: 自定义请求 Cookie
- **自动清理**: 下载完成后自动清理临时文件
- **保存路径**: 自定义下载目录

### WebDAV 集成

- 下载完成后自动上传到 WebDAV 服务器
- 支持上传后自动删除本地文件
- 支持手动重试上传失败的任务

### 任务管理

- 查看所有任务历史记录
- 支持暂停/恢复正在下载的任务
- 支持停止任意任务
- 支持重试失败或已完成的任务

## 鸣谢

- 项目基于 [m3u8-downloader](https://github.com/llychao/m3u8-downloader) 项目二次修改，感谢其贡献。
