package main

import (
	"embed"
	"io/fs"
	"log"
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

var (
	server      *http.Server
	appInstance *App
)

func main() {
	// 创建 Wails 应用
	app := NewApp()

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
			Handler: createRouter(),
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
	schedulerService := service.NewSchedulerService(dbStorage)
	schedulerService.Start()

	// 初始化处理器
	taskHandler := handler.NewTaskHandler(taskManager, downloaderService)
	downloadHandler := handler.NewDownloadHandler(downloaderService, taskManager)
	settingsHandler := handler.NewSettingsHandler(dbStorage, schedulerService, downloaderService)
	authHandler := handler.NewAuthHandler()
	diskHandler := handler.NewDiskHandler()
	wsHandler := websocket.NewWebSocketHandler(wsManager)

	router := mux.NewRouter()
	router.Use(corsMiddleware)

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
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
