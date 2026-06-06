package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

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
	wsManager := websocket.NewWebSocketManager()
	downloaderService := service.NewDownloaderService(taskManager, wsManager)

	taskHandler := handler.NewTaskHandler(taskManager, downloaderService)
	downloadHandler := handler.NewDownloadHandler(downloaderService, taskManager)
	settingsHandler := handler.NewSettingsHandler(dbStorage)
	authHandler := handler.NewAuthHandler()
	wsHandler := websocket.NewWebSocketHandler(wsManager)

	router := mux.NewRouter()

	router.Use(corsMiddleware)

	api := router.PathPrefix("/api").Subrouter()

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
	protectedAPI.HandleFunc("/settings", settingsHandler.GetSettings).Methods("GET")
	protectedAPI.HandleFunc("/settings", settingsHandler.SaveSettings).Methods("POST")
	protectedAPI.HandleFunc("/settings/webdav/test", settingsHandler.TestWebDAV).Methods("POST")
	protectedAPI.HandleFunc("/settings/webdav/list", settingsHandler.ListWebDAVDir).Methods("POST")
	protectedAPI.HandleFunc("/settings/clear-cache", settingsHandler.ClearCache).Methods("POST")

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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
