package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"m3u8-downloader-web/model"
	"m3u8-downloader-web/service"
	"m3u8-downloader-web/storage"

	"github.com/gorilla/mux"
)

type RemoteHandler struct {
	storage           *storage.SQLiteStorage
	taskManager       *service.TaskManager
	downloaderService *service.DownloaderService
	schedulerService  *service.SchedulerService
	diskHandler       *DiskHandler
	speedTestService  *service.SpeedTestService
}

func NewRemoteHandler(
	storage *storage.SQLiteStorage,
	taskManager *service.TaskManager,
	downloaderService *service.DownloaderService,
	schedulerService *service.SchedulerService,
	speedTestService *service.SpeedTestService,
) *RemoteHandler {
	return &RemoteHandler{
		storage:           storage,
		taskManager:       taskManager,
		downloaderService: downloaderService,
		schedulerService:  schedulerService,
		diskHandler:       NewDiskHandler(),
		speedTestService:  speedTestService,
	}
}

func (h *RemoteHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	tasks, err := h.storage.GetAllTasks()
	if err != nil {
		Err(w, http.StatusInternalServerError, "获取任务列表失败")
		return
	}
	OK(w, tasks)
}

func (h *RemoteHandler) GetTask(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	if id == "" {
		Err(w, http.StatusBadRequest, "任务ID不能为空")
		return
	}

	task, err := h.storage.GetTask(id)
	if err != nil {
		Err(w, http.StatusInternalServerError, "获取任务失败")
		return
	}
	if task == nil {
		Err(w, http.StatusNotFound, "任务不存在")
		return
	}
	OK(w, task)
}

func (h *RemoteHandler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]
	if id == "" {
		Err(w, http.StatusBadRequest, "任务ID不能为空")
		return
	}

	if err := h.storage.DeleteTask(id); err != nil {
		Err(w, http.StatusInternalServerError, "删除任务失败")
		return
	}
	OK(w, map[string]string{"message": "任务已删除"})
}

func (h *RemoteHandler) StartDownload(w http.ResponseWriter, r *http.Request) {
	var req model.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	if req.URL == "" {
		Err(w, http.StatusBadRequest, "URL 不能为空")
		return
	}

	if req.ThreadCount <= 0 {
		req.ThreadCount = 24
	}

	if req.OutputName == "" {
		req.OutputName = "movie"
	}

	// 记录节点源地址，供 single 产物 meta.json 使用
	req.NodeOrigin = requestOrigin(r)

	task, err := h.downloaderService.StartDownload(req)
	if err != nil {
		log.Printf("[Remote] Start download error: %v", err)
		Err(w, http.StatusInternalServerError, err.Error())
		return
	}

	OK(w, map[string]string{
		"taskId": task.ID,
	})
}

func (h *RemoteHandler) StopDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"taskId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	h.downloaderService.StopDownload(req.TaskID)
	OK(w, map[string]string{"message": "下载已停止"})
}

func (h *RemoteHandler) PauseDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"taskId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	h.downloaderService.PauseDownload(req.TaskID)
	OK(w, map[string]string{"message": "下载已暂停"})
}

func (h *RemoteHandler) ResumeDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"taskId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	h.downloaderService.ResumeDownload(req.TaskID)
	OK(w, map[string]string{"message": "下载已恢复"})
}

func (h *RemoteHandler) RetryDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"taskId"`
		Mode   string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	mode := req.Mode
	if mode == "" {
		mode = "retry_missing"
	}

	if err := h.downloaderService.RetryDownload(req.TaskID, mode); err != nil {
		Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{"message": "重试已启动"})
}

func (h *RemoteHandler) UploadToWebDAV(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string                `json:"taskId"`
		Config *service.WebDAVConfig `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	if err := h.downloaderService.UploadTaskToWebDAV(req.TaskID, req.Config); err != nil {
		Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, map[string]string{"message": "上传已启动"})
}

func (h *RemoteHandler) AnalyzeM3U8(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL     string `json:"url"`
		Referer string `json:"referer"`
		Cookie  string `json:"cookie"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	if req.URL == "" {
		Err(w, http.StatusBadRequest, "URL 不能为空")
		return
	}

	result, err := h.downloaderService.AnalyzeURL(req.URL, req.Referer, req.Cookie)
	if err != nil {
		Err(w, http.StatusInternalServerError, err.Error())
		return
	}
	OK(w, result)
}

func (h *RemoteHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.storage.GetSettings()
	if err != nil {
		Err(w, http.StatusInternalServerError, "获取设置失败")
		return
	}
	OK(w, settings)
}

func (h *RemoteHandler) SaveSettings(w http.ResponseWriter, r *http.Request) {
	var newSettings model.Settings
	if err := json.NewDecoder(r.Body).Decode(&newSettings); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	if err := h.storage.SaveSettings(&newSettings); err != nil {
		Err(w, http.StatusInternalServerError, "保存设置失败")
		return
	}

	if h.downloaderService != nil {
		h.downloaderService.UpdateConcurrencyConfig(
			newSettings.DownloadConcurrency,
			newSettings.MergeConcurrency,
			newSettings.CompressConcurrency,
			newSettings.PackConcurrency,
			newSettings.UploadConcurrency,
			newSettings.SingleMode,
		)
	}

	OK(w, newSettings)
}

func (h *RemoteHandler) TestWebDAV(w http.ResponseWriter, r *http.Request) {
	var config model.Settings
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
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
		Err(w, http.StatusInternalServerError, err.Error())
		return
	}

	OK(w, map[string]string{"message": "连接测试成功"})
}

func (h *RemoteHandler) ListWebDAVDir(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
		Path     string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
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
		Err(w, http.StatusInternalServerError, err.Error())
		return
	}

	type FileItem struct {
		Name  string `json:"name"`
		IsDir bool   `json:"isDir"`
		Path  string `json:"path"`
	}

	var result []FileItem
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

	OK(w, result)
}

func (h *RemoteHandler) ClearCache(w http.ResponseWriter, r *http.Request) {
	settings, err := h.storage.GetSettings()
	if err != nil {
		Err(w, http.StatusInternalServerError, "获取设置失败")
		return
	}

	count := 0
	if h.schedulerService != nil {
		count = h.schedulerService.ClearCache()
	}

	OK(w, map[string]interface{}{
		"message":  "缓存清理完成",
		"count":    count,
		"settings": settings,
	})
}

func (h *RemoteHandler) GetCleanupConfig(w http.ResponseWriter, r *http.Request) {
	config := h.schedulerService.GetConfig()
	OK(w, config)
}

func (h *RemoteHandler) UpdateCleanupConfig(w http.ResponseWriter, r *http.Request) {
	var config service.CleanupConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	h.schedulerService.UpdateConfig(config)
	OK(w, config)
}

// StartSpeedTest 启动 WebDAV 上传测速
func (h *RemoteHandler) StartSpeedTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WebDAVURL       string `json:"webDAVURL"`
		WebDAVUsername  string `json:"webDAVUsername"`
		WebDAVPassword  string `json:"webDAVPassword"`
		WebDAVRemoteDir string `json:"webDAVRemoteDir"`
		SizeMB          int    `json:"sizeMB"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}
	if strings.TrimSpace(req.WebDAVURL) == "" {
		Err(w, http.StatusBadRequest, "WebDAV 地址不能为空")
		return
	}
	cfg := service.WebDAVConfig{
		Enabled:   true,
		URL:       req.WebDAVURL,
		Username:  req.WebDAVUsername,
		Password:  req.WebDAVPassword,
		RemoteDir: req.WebDAVRemoteDir,
	}
	if err := h.speedTestService.Start(cfg, req.SizeMB); err != nil {
		Err(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, map[string]string{"message": "测速已启动"})
}

// StopSpeedTest 手动中断测速
func (h *RemoteHandler) StopSpeedTest(w http.ResponseWriter, r *http.Request) {
	h.speedTestService.Stop()
	OK(w, map[string]string{"message": "已请求中断测速"})
}

// GetSpeedTestLog 返回最后一次测速日志
func (h *RemoteHandler) GetSpeedTestLog(w http.ResponseWriter, r *http.Request) {
	OK(w, h.speedTestService.Snapshot())
}

func (h *RemoteHandler) GetDiskInfo(w http.ResponseWriter, r *http.Request) {
	path := "."
	if r.URL.Query().Get("path") != "" {
		path = r.URL.Query().Get("path")
	}
	info, err := getDiskInfo(path)
	if err != nil {
		Err(w, http.StatusInternalServerError, "获取磁盘信息失败: "+err.Error())
		return
	}
	OK(w, info)
}

func (h *RemoteHandler) GetAllDisks(w http.ResponseWriter, r *http.Request) {
	disks, err := getAllDisks()
	if err != nil {
		Err(w, http.StatusInternalServerError, "获取磁盘列表失败: "+err.Error())
		return
	}
	OK(w, disks)
}

func (h *RemoteHandler) CheckSpace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	path := req.Path
	if path == "" {
		path = "."
	}

	diskInfo, err := getDiskInfo(path)
	if err != nil {
		Err(w, http.StatusInternalServerError, "检查磁盘空间失败: "+err.Error())
		return
	}

	OK(w, diskInfo)
}
