package main

import (
	"embed"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"m3u8-downloader-web/handler"
	"m3u8-downloader-web/service"
	"m3u8-downloader-web/storage"
	"m3u8-downloader-web/websocket"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed static
var assets embed.FS

func main() {
	// 创建 Wails 应用
	app := NewApp()

	// 构建路由（REST API + WebSocket），同时挂到：
	// 1) 本地真实 TCP 服务 —— WebSocket 只能走这里（AssetServer 不支持 WS 升级）
	// 2) Wails AssetServer Handler —— 保持相对路径 HTTP API 可用
	router := createRouter()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("Failed to listen local API server: %v", err)
	}
	app.serverBaseURL = "http://" + ln.Addr().String()
	app.serverLn = ln
	app.server = &http.Server{Handler: router}
	// 在打开窗口前启动监听，确保 WebView 首屏请求（含 WS）时端口已 accept
	app.serveLocalAPI()

	// 获取静态文件目录
	staticDir := getStaticDir()
	log.Printf("Static directory: %s", staticDir)

	// 将嵌入的文件系统转换为 fs.FS
	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		log.Fatalf("Failed to create sub filesystem: %v", err)
	}

	err = wails.Run(&options.App{
		Title:  "HLSTo - M3U8 下载器",
		Width:  1280,
		Height: 800,
		MinWidth: 1024,
		MinHeight: 600,
		AssetServer: &assetserver.Options{
			Assets:  staticFS.(fs.FS),
			Handler: router,
		},
		BackgroundColour: &options.RGBA{R: 30, G: 30, B: 40, A: 255},
		OnStartup:        app.startup,
		OnBeforeClose:   app.beforeClose,
		Bind: []interface{}{
			app,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			DisableWindowIcon:    false,
		},
	})

	if err != nil {
		log.Fatal("Error running application:", err)
	}

	_ = staticDir
}

// 获取静态文件目录
func getStaticDir() string {
	// 优先使用运行目录下的 static
	if execDir, err := os.Executable(); err == nil {
		staticPath := filepath.Join(filepath.Dir(execDir), "static")
		if _, err := os.Stat(staticPath); err == nil {
			return staticPath
		}
	}

	// 开发模式：使用相对路径
	paths := []string{
		"static",
		"../backend/static",
		"../frontend/dist",
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	return "static"
}

func createRouter() *mux.Router {
	// 尝试加载 .env 文件
	_ = godotenv.Load()
	_ = godotenv.Load("../.env")

	// 初始化认证
	handler.InitAuth()

	// 初始化数据库
	dbStorage, err := storage.NewSQLiteStorage()
	if err != nil {
		log.Printf("Warning: Failed to initialize SQLite storage: %v", err)
	}

	// 初始化服务
	taskManager := service.NewTaskManager(dbStorage)
	wsManager := websocket.NewWebSocketManager()
	downloaderService := service.NewDownloaderService(taskManager, wsManager)

	// FFmpeg 环境服务：检测系统/PATH/私有目录，并支持私有下载安装
	ffmpegService := service.NewFFmpegService()
	downloaderService.SetFFmpegService(ffmpegService)

	schedulerService := service.NewSchedulerService(dbStorage, taskManager)
	schedulerService.Start()

	// 启动时将数据库中已持久化的并发与封装配置应用到下载服务
	if persisted, err := dbStorage.GetSettings(); err != nil {
		log.Printf("[WinUI] 启动加载设置失败: %v\n", err)
	} else if persisted != nil {
		downloaderService.UpdateConcurrencyConfig(
			persisted.DownloadConcurrency,
			persisted.MergeConcurrency,
			persisted.CompressConcurrency,
			persisted.PackConcurrency,
			persisted.UploadConcurrency,
			persisted.SingleMode,
		)
		downloaderService.UpdatePostDownloadConfig(
			persisted.MergeAfterDownload,
			persisted.MergeMethod,
			persisted.FFmpegMuxMode,
		)
		downloaderService.UpdateHLSPackConfig(
			persisted.HLSPackEnabled,
			persisted.HLSEncryptEnabled,
			persisted.HLSEncryptMode,
			persisted.HLSKeyURL,
			persisted.HLSPackForm,
		)
		downloaderService.UpdateCompressConfig(
			persisted.CompressAfterMerge,
			persisted.CompressBitrateThreshold,
			persisted.CompressTargetBitrate,
		)
	}

	// 初始化处理器
	taskHandler := handler.NewTaskHandler(taskManager, downloaderService)
	downloadHandler := handler.NewDownloadHandler(downloaderService, taskManager)
	speedTestService := service.NewSpeedTestService()
	settingsHandler := handler.NewSettingsHandler(dbStorage, schedulerService, downloaderService, ffmpegService, speedTestService)
	ffmpegHandler := handler.NewFFmpegHandler(ffmpegService)
	authHandler := handler.NewAuthHandler()
	diskHandler := handler.NewDiskHandler()
	wsHandler := websocket.NewWebSocketHandler(wsManager)

	router := mux.NewRouter()
	// 必须在路由匹配失败前处理 CORS/PNA 预检：
	// gorilla/mux 的中间件仅在路由命中后执行，未注册的 OPTIONS 会直接 405，
	// 导致 WebView2 的 Private Network Access 预检失败（前端表现为 Network Error）。
	router.Use(corsMiddleware, wailsLocalhostMiddleware)
	// 全局 OPTIONS 兜底：所有跨域/PNA 预检统一 204
	router.Methods(http.MethodOptions).Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Private-Network", "true")
		w.WriteHeader(http.StatusNoContent)
	}))

	// API 路由
	api := router.PathPrefix("/api").Subrouter()

	// 公开 API
	api.HandleFunc("/auth/login", authHandler.Login).Methods("POST")
	api.HandleFunc("/auth/check", authHandler.CheckAuth).Methods("GET")

	// 受保护的 API
	protectedAPI := api.PathPrefix("").Subrouter()
	protectedAPI.Use(handler.AuthMiddleware)
	protectedAPI.HandleFunc("/download/start", downloadHandler.StartDownload).Methods("POST")
	protectedAPI.HandleFunc("/download/stop", downloadHandler.StopDownload).Methods("POST")
	protectedAPI.HandleFunc("/download/pause", downloadHandler.PauseDownload).Methods("POST")
	protectedAPI.HandleFunc("/download/resume", downloadHandler.ResumeDownload).Methods("POST")
	protectedAPI.HandleFunc("/download/retry", downloadHandler.RetryDownload).Methods("POST")
	protectedAPI.HandleFunc("/download/upload", downloadHandler.UploadToWebDAV).Methods("POST")
	protectedAPI.HandleFunc("/download/analyze", downloadHandler.AnalyzeM3U8).Methods("POST")
	protectedAPI.HandleFunc("/tasks", taskHandler.ListTasks).Methods("GET")
	protectedAPI.HandleFunc("/tasks/{id}", taskHandler.GetTask).Methods("GET")
	protectedAPI.HandleFunc("/tasks/{id}", taskHandler.DeleteTask).Methods("DELETE")
	protectedAPI.HandleFunc("/settings", settingsHandler.GetSettings).Methods("GET")
	protectedAPI.HandleFunc("/settings", settingsHandler.SaveSettings).Methods("POST")
	protectedAPI.HandleFunc("/settings/webdav/test", settingsHandler.TestWebDAV).Methods("POST")
	protectedAPI.HandleFunc("/settings/webdav/list", settingsHandler.ListWebDAVDir).Methods("POST")
	protectedAPI.HandleFunc("/settings/clear-cache", settingsHandler.ClearCache).Methods("POST")
	protectedAPI.HandleFunc("/settings/cleanup-config", settingsHandler.GetCleanupConfig).Methods("GET")
	protectedAPI.HandleFunc("/settings/cleanup-config", settingsHandler.UpdateCleanupConfig).Methods("POST")
	protectedAPI.HandleFunc("/settings/speedtest", settingsHandler.GetSpeedTestLog).Methods("GET")
	protectedAPI.HandleFunc("/settings/speedtest/start", settingsHandler.StartSpeedTest).Methods("POST")
	protectedAPI.HandleFunc("/settings/speedtest/stop", settingsHandler.StopSpeedTest).Methods("POST")
	protectedAPI.HandleFunc("/ffmpeg/status", ffmpegHandler.Status).Methods("GET")
	protectedAPI.HandleFunc("/ffmpeg/install", ffmpegHandler.Install).Methods("POST")

	// 磁盘信息
	router.HandleFunc("/api/disk/info", diskHandler.GetDiskInfo).Methods("GET")
	router.HandleFunc("/api/disk/all", diskHandler.GetAllDisks).Methods("GET")
	router.HandleFunc("/api/disk/check-space", diskHandler.CheckSpace).Methods("POST")

	// WebSocket
	router.HandleFunc("/ws", wsHandler.HandleWebSocket)

	return router
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 桌面端页面源为 wails://wails.localhost / http://wails.localhost，
		// 回显具体源并允许凭据，避免 " * + credentials" 与 PNA 拦截
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
		} else {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// wailsLocalhostMiddleware 处理 WebView2 的 Private Network Access 预检：
// Chromium 对从 wails.localhost（public 空间）访问 127.0.0.1（local 空间）
// 会先发带 Access-Control-Request-Private-Network 的 OPTIONS，
// 服务端必须回 Access-Control-Allow-Private-Network: true，否则请求直接被拦（Network Error）。
func wailsLocalhostMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Access-Control-Request-Private-Network") == "true" {
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}
		next.ServeHTTP(w, r)
	})
}
