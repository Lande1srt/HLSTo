# --- 前端构建阶段 ---
FROM node:20-alpine AS frontend-builder

WORKDIR /app/frontend
COPY frontend/package*.json ./
RUN npm install
COPY frontend/ ./
RUN npm run build

# --- 后端构建阶段 ---
FROM golang:1.21-alpine AS backend-builder

# 安装构建依赖 (sqlite3 需要 gcc 和 musl-dev)
RUN apk add --no-cache gcc musl-dev

WORKDIR /app
# 复制 go.mod 和 go.sum 并下载依赖
COPY backend/go.mod backend/go.sum ./backend/
RUN cd backend && go mod download

# 复制后端源码
COPY backend/ ./backend/
# 复制前端构建产物到后端静态目录
COPY --from=frontend-builder /app/backend/static ./backend/static

# 构建后端
WORKDIR /app/backend
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags "-s -w" -o m3u8-downloader-web main.go

# --- 最终运行阶段 ---
FROM alpine:latest

# 安装运行时必要的库
RUN apk add --no-cache ca-certificates libc6-compat

WORKDIR /app

# 从构建阶段复制二进制文件和静态资源
COPY --from=backend-builder /app/backend/m3u8-downloader-web .
COPY --from=backend-builder /app/backend/static ./static

# 暴露端口
EXPOSE 8080

# 启动命令
CMD ["./m3u8-downloader-web"]
