package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
)

// App 结构体 - Wails 绑定的主要应用
type App struct {
	ctx context.Context

	// 本地 HTTP 服务（承载 REST API 与 WebSocket）。
	// Wails 的 AssetServer 基于 WebView2 WebResourceRequested，
	// 不支持 WebSocket 升级，因此桌面端必须监听真实 TCP 端口供前端直连。
	server        *http.Server
	serverLn      net.Listener
	serverBaseURL string
}

// NewApp 创建新的应用实例
func NewApp() *App {
	return &App{}
}

// serverInfoHandler 暴露给前端的 HTTP handler（AssetServer Handler 会路由到这里）。
// 返回本地 HTTP 服务基址（REST + WebSocket 真实端口）。
// 这样前端完全绕开 Wails v2.17+ 绑定生成器对 GetServerBaseURL/ServerPort 等方法
// 的静默过滤——走 AssetServer 上已挂载的 HTTP 路由就能拿到真实端口。
func (a *App) serverInfoHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{"baseURL": a.serverBaseURL})
}

// startup 在应用启动时调用
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	log.Println("HLSTo 应用程序已启动")
}

// serveLocalAPI 在 wails.Run 之前启动本地 API 服务（阻塞调用方风险低，
// 由 main 以 goroutine 方式调用），保证 WebView 首帧加载时端口已就绪
func (a *App) serveLocalAPI() {
	if a.server == nil || a.serverLn == nil {
		return
	}
	go func() {
		log.Printf("[WinUI] 本地 API 服务已启动: %s", a.serverBaseURL)
		if err := a.server.Serve(a.serverLn); err != nil && err != http.ErrServerClosed {
			log.Printf("[WinUI] 本地 API 服务异常: %v", err)
		}
	}()
}

// beforeClose 在应用关闭前调用
func (a *App) beforeClose(ctx context.Context) bool {
	if a.server != nil {
		if err := a.server.Shutdown(ctx); err != nil {
			log.Printf("[WinUI] 本地 API 服务关闭异常: %v", err)
		}
	}
	log.Println("HLSTo 应用程序即将关闭")
	return false
}

// Greet 演示 Wails 绑定方法
func (a *App) Greet(name string) string {
	return "Hello, " + name + "! 欢迎使用 HLSTo!"
}

// GetVersion 返回应用版本
func (a *App) GetVersion() string {
	return "1.0.0"
}
