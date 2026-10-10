package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m3u8-downloader-web/handler"
	"m3u8-downloader-web/service"
	"m3u8-downloader-web/storage"
	"m3u8-downloader-web/websocket"

	"github.com/gorilla/mux"
	"github.com/joho/godotenv"
)

func main() {
	log.Println("Starting M3U8 Downloader Web Server...")

	// Load .env file
	err := godotenv.Load()
	if err != nil {
		// Try loading from root directory if running from backend
		err = godotenv.Load("../.env")
	}

	if err != nil {
		log.Println("Note: .env file not found, using system environment variables")
	} else {
		log.Println("Loaded environment variables from .env")
	}

	// Re-initialize auth with potential .env values
	handler.InitAuth()

	staticDir := getStaticDir()

	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		log.Printf("Warning: Static directory not found at %s", staticDir)
		log.Println("Please run 'npm run build' in the frontend directory first")
	}

	if handler.USERNAME != "" && handler.PASSWORD != "" {
		log.Println("Authentication is enabled")
	} else {
		log.Println("Authentication is disabled (set AUTH_USERNAME and AUTH_PASSWORD env vars to enable)")
	}

	dbStorage, err := storage.NewSQLiteStorage()
	if err != nil {
		log.Printf("Warning: Failed to initialize SQLite storage: %v", err)
	}

	taskManager := service.NewTaskManager(dbStorage)
	defer taskManager.Close()

	wsManager := websocket.NewWebSocketManager()
	downloaderService := service.NewDownloaderService(taskManager, wsManager)

	// FFmpeg 环境服务：检测系统/PATH/私有目录，并支持私有下载安装
	ffmpegService := service.NewFFmpegService()
	downloaderService.SetFFmpegService(ffmpegService)

	schedulerService := service.NewSchedulerService(dbStorage, taskManager)
	schedulerService.Start()

	// 启动时将数据库中已持久化的并发与封装配置应用到下载服务
	// （服务内存默认值可能与用户之前保存的配置不一致）
	if persisted, err := dbStorage.GetSettings(); err != nil {
		log.Printf("[Main] 启动加载设置失败: %v\n", err)
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

	taskHandler := handler.NewTaskHandler(taskManager, downloaderService)
	downloadHandler := handler.NewDownloadHandler(downloaderService, taskManager)
	speedTestService := service.NewSpeedTestService()
	settingsHandler := handler.NewSettingsHandler(dbStorage, schedulerService, downloaderService, ffmpegService, speedTestService)
	authHandler := handler.NewAuthHandler()
	diskHandler := handler.NewDiskHandler()
	wsHandler := websocket.NewWebSocketHandler(wsManager)
	apiKeyHandler := handler.NewAPIKeyHandler(dbStorage)
	ffmpegHandler := handler.NewFFmpegHandler(ffmpegService)
	remoteHandler := handler.NewRemoteHandler(dbStorage, taskManager, downloaderService, schedulerService, speedTestService)

	router := mux.NewRouter()

	router.Use(corsMiddleware)

	api := router.PathPrefix("/api").Subrouter()

	// 免鉴权健康检查：用于在云端直接判断 Go 进程是否存活/可响应，
	// 与反向代理、API Key 等环节解耦。curl http(s)://<host>/api/health
	api.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		handler.OK(w, map[string]interface{}{
			"status": "ok",
			"time":   time.Now().UTC().Format(time.RFC3339),
		})
	}).Methods("GET")

	api.HandleFunc("/auth/login", authHandler.Login).Methods("POST")
	api.HandleFunc("/auth/check", authHandler.CheckAuth).Methods("GET")

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
	protectedAPI.HandleFunc("/tasks/{id}/key", taskHandler.DownloadKey).Methods("GET")
	protectedAPI.HandleFunc("/tasks/{id}/logs", taskHandler.GetLogs).Methods("GET")
	protectedAPI.HandleFunc("/tasks/{id}/logs/download", taskHandler.DownloadLogs).Methods("GET")
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

	apiKeyAPI := api.PathPrefix("/apikey").Subrouter()
	apiKeyAPI.Use(handler.AuthMiddleware)
	apiKeyAPI.HandleFunc("/generate", apiKeyHandler.GenerateKey).Methods("POST")
	apiKeyAPI.HandleFunc("/list", apiKeyHandler.ListKeys).Methods("POST")
	apiKeyAPI.HandleFunc("/revoke", apiKeyHandler.RevokeKey).Methods("POST")
	apiKeyAPI.HandleFunc("/delete", apiKeyHandler.DeleteKey).Methods("POST")

	remoteAPI := api.PathPrefix("/remote").Subrouter()
	remoteAPI.Use(handler.APIKeyMiddleware(dbStorage, ""))
	remoteAPI.HandleFunc("/tasks", remoteHandler.ListTasks).Methods("GET")
	remoteAPI.HandleFunc("/tasks/{id}", remoteHandler.GetTask).Methods("GET")
	remoteAPI.HandleFunc("/tasks/{id}", remoteHandler.DeleteTask).Methods("DELETE")
	remoteAPI.HandleFunc("/tasks/{id}/key", taskHandler.DownloadKey).Methods("GET")
	remoteAPI.HandleFunc("/tasks/{id}/logs", taskHandler.GetLogs).Methods("GET")
	remoteAPI.HandleFunc("/tasks/{id}/logs/download", taskHandler.DownloadLogs).Methods("GET")
	remoteAPI.HandleFunc("/download/start", remoteHandler.StartDownload).Methods("POST")
	remoteAPI.HandleFunc("/download/stop", remoteHandler.StopDownload).Methods("POST")
	remoteAPI.HandleFunc("/download/pause", remoteHandler.PauseDownload).Methods("POST")
	remoteAPI.HandleFunc("/download/resume", remoteHandler.ResumeDownload).Methods("POST")
	remoteAPI.HandleFunc("/download/retry", remoteHandler.RetryDownload).Methods("POST")
	remoteAPI.HandleFunc("/download/upload", remoteHandler.UploadToWebDAV).Methods("POST")
	remoteAPI.HandleFunc("/download/analyze", remoteHandler.AnalyzeM3U8).Methods("POST")
	remoteAPI.HandleFunc("/settings", remoteHandler.GetSettings).Methods("GET")
	remoteAPI.HandleFunc("/settings", remoteHandler.SaveSettings).Methods("POST")
	remoteAPI.HandleFunc("/settings/webdav/test", remoteHandler.TestWebDAV).Methods("POST")
	remoteAPI.HandleFunc("/settings/webdav/list", remoteHandler.ListWebDAVDir).Methods("POST")
	remoteAPI.HandleFunc("/settings/clear-cache", remoteHandler.ClearCache).Methods("POST")
	remoteAPI.HandleFunc("/settings/cleanup-config", remoteHandler.GetCleanupConfig).Methods("GET")
	remoteAPI.HandleFunc("/settings/cleanup-config", remoteHandler.UpdateCleanupConfig).Methods("POST")
	remoteAPI.HandleFunc("/settings/speedtest", remoteHandler.GetSpeedTestLog).Methods("GET")
	remoteAPI.HandleFunc("/settings/speedtest/start", remoteHandler.StartSpeedTest).Methods("POST")
	remoteAPI.HandleFunc("/settings/speedtest/stop", remoteHandler.StopSpeedTest).Methods("POST")
	remoteAPI.HandleFunc("/disk/info", remoteHandler.GetDiskInfo).Methods("GET")
	remoteAPI.HandleFunc("/disk/all", remoteHandler.GetAllDisks).Methods("GET")
	remoteAPI.HandleFunc("/disk/check-space", remoteHandler.CheckSpace).Methods("POST")
	remoteAPI.HandleFunc("/ffmpeg/status", ffmpegHandler.Status).Methods("GET")
	remoteAPI.HandleFunc("/ffmpeg/install", ffmpegHandler.Install).Methods("POST")

	router.HandleFunc("/api/disk/info", diskHandler.GetDiskInfo).Methods("GET")
	router.HandleFunc("/api/disk/all", diskHandler.GetAllDisks).Methods("GET")
	router.HandleFunc("/api/disk/check-space", diskHandler.CheckSpace).Methods("POST")

	router.HandleFunc("/ws", wsHandler.HandleWebSocket)

	if _, err := os.Stat(staticDir); err == nil {
		router.PathPrefix("/").Handler(spaHandler{staticDir: staticDir})
		log.Printf("Serving static files from: %s", staticDir)
	} else {
		router.PathPrefix("/").HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Frontend not built. Run 'npm run build' in frontend directory.", http.StatusServiceUnavailable)
		})
	}

	port := getPort()
	log.Printf("Server starting on http://localhost%s", port)
	log.Printf("API endpoint: http://localhost%s/api", port)
	log.Fatal(http.ListenAndServe(port, router))
}

func getStaticDir() string {
	execDir, err := os.Executable()
	if err != nil {
		return "./static"
	}
	execPath := filepath.Dir(execDir)

	staticPath := filepath.Join(execPath, "static")
	if _, err := os.Stat(staticPath); err == nil {
		return staticPath
	}

	staticPath = "./static"
	if _, err := os.Stat(staticPath); err == nil {
		return staticPath
	}

	return staticPath
}

func getPort() string {
	if port := os.Getenv("PORT"); port != "" {
		return ":" + port
	}
	return ":8080"
}

type spaHandler struct {
	staticDir string
}

func (h spaHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	if strings.HasPrefix(path, "/api") || strings.HasPrefix(path, "/ws") {
		http.NotFound(w, r)
		return
	}

	filePath := filepath.Join(h.staticDir, path)

	if _, err := os.Stat(filePath); err == nil {
		if !isDir(filePath) {
			http.ServeFile(w, r, filePath)
			return
		}
	}

	indexPath := filepath.Join(h.staticDir, "index.html")
	if _, err := os.Stat(indexPath); err == nil {
		http.ServeFile(w, r, indexPath)
		return
	}

	http.NotFound(w, r)
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, X-Api-Key")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
