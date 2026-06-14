package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"m3u8-downloader-web/model"
	"m3u8-downloader-web/service"
	"m3u8-downloader-web/storage"
)

type SettingsHandler struct {
	storage          *storage.SQLiteStorage
	schedulerService *service.SchedulerService
	downloaderService *service.DownloaderService
}

func NewSettingsHandler(storage *storage.SQLiteStorage, scheduler *service.SchedulerService, downloader *service.DownloaderService) *SettingsHandler {
	return &SettingsHandler{
		storage:          storage,
		schedulerService: scheduler,
		downloaderService: downloader,
	}
}

func (h *SettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.storage.GetSettings()
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "获取设置失败")
		return
	}
	h.sendSuccess(w, settings)
}

func (h *SettingsHandler) TestWebDAV(w http.ResponseWriter, r *http.Request) {
	var config model.Settings
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	webdavConfig := service.WebDAVConfig{
		Enabled:  true,
		URL:      config.WebDAVURL,
		Username: config.WebDAVUsername,
		Password: config.WebDAVPassword,
	}

	webdavService := service.NewWebDAVService(webdavConfig)
	err := webdavService.TestConnection()
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.sendSuccess(w, map[string]string{"message": "连接测试成功"})
}

func (h *SettingsHandler) ListWebDAVDir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
		Path     string `json:"path"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	webdavConfig := service.WebDAVConfig{
		Enabled:  true,
		URL:      req.URL,
		Username: req.Username,
		Password: req.Password,
	}

	webdavService := service.NewWebDAVService(webdavConfig)
	files, err := webdavService.ReadDir(req.Path)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	type FileItem struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Path  string `json:"path"`
	}

	var result = []FileItem{}
	for _, f := range files {
		if f.IsDir() {
			itemPath := req.Path
			if itemPath == "" || itemPath == "/" {
				itemPath = "/" + f.Name()
			} else {
				if itemPath[len(itemPath)-1] != '/' {
					itemPath += "/"
				}
				itemPath += f.Name()
			}

			result = append(result, FileItem{
				Name:  f.Name(),
				IsDir: f.IsDir(),
				Path:  itemPath,
			})
		}
	}

	h.sendSuccess(w, result)
}

func (h *SettingsHandler) ClearCache(w http.ResponseWriter, r *http.Request) {
	// 1. 获取当前工作目录
	pwd, err := os.Getwd()
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "获取工作目录失败")
		return
	}

	// 2. 获取设置中的默认保存目录
	dirsToClear := []string{pwd}
	settings, err := h.storage.GetSettings()
	if err == nil && settings.DefaultSavePath != "" && settings.DefaultSavePath != pwd {
		dirsToClear = append(dirsToClear, settings.DefaultSavePath)
	}

	count := 0
	for _, dir := range dirsToClear {
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() && strings.HasPrefix(f.Name(), "download_") {
				err := os.RemoveAll(filepath.Join(dir, f.Name()))
				if err != nil {
					log.Printf("[Settings] 删除缓存目录失败: %v\n", err)
				} else {
					count++
				}
			}
		}
	}

	h.sendSuccess(w, map[string]interface{}{
		"message": fmt.Sprintf("已成功清除 %d 个缓存文件夹", count),
		"count":   count,
	})
}

func (h *SettingsHandler) SaveSettings(w http.ResponseWriter, r *http.Request) {
	var newSettings model.Settings
	if err := json.NewDecoder(r.Body).Decode(&newSettings); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	if err := h.storage.SaveSettings(&newSettings); err != nil {
		h.sendError(w, http.StatusInternalServerError, "保存设置失败")
		return
	}

	// 更新下载服务的并发配置
	if h.downloaderService != nil {
		h.downloaderService.UpdateConcurrencyConfig(
			newSettings.DownloadConcurrency,
			newSettings.MergeConcurrency,
			newSettings.UploadConcurrency,
			newSettings.SingleMode,
		)
	}

	h.sendSuccess(w, newSettings)
}

func (h *SettingsHandler) GetCleanupConfig(w http.ResponseWriter, r *http.Request) {
	config := h.schedulerService.GetConfig()
	h.sendSuccess(w, config)
}

func (h *SettingsHandler) UpdateCleanupConfig(w http.ResponseWriter, r *http.Request) {
	var config service.CleanupConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	h.schedulerService.UpdateConfig(config)
	h.sendSuccess(w, config)
}

func (h *SettingsHandler) sendSuccess(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(model.APIResponse{
		Code:    200,
		Message: "success",
		Data:    data,
	})
}

func (h *SettingsHandler) sendError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(model.APIResponse{
		Code:    status,
		Message: message,
	})
}
