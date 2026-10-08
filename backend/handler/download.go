package handler

import (
	"encoding/json"
	"m3u8-downloader-web/model"
	"m3u8-downloader-web/service"
	"net/http"
)

type DownloadHandler struct {
	downloaderService *service.DownloaderService
	taskManager       *service.TaskManager
}

func NewDownloadHandler(downloaderService *service.DownloaderService, taskManager *service.TaskManager) *DownloadHandler {
	return &DownloadHandler{
		downloaderService: downloaderService,
		taskManager:       taskManager,
	}
}

func (h *DownloadHandler) StartDownload(w http.ResponseWriter, r *http.Request) {
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

	if req.HostType == "" {
		req.HostType = "v1"
	}

	// 记录节点源地址，供 single 产物 meta.json 使用
	req.NodeOrigin = requestOrigin(r)

	task, err := h.downloaderService.StartDownload(req)
	if err != nil {
		Err(w, http.StatusInternalServerError, err.Error())
		return
	}

	OK(w, map[string]interface{}{
		"taskId": task.ID,
		"status": task.Status,
	})
}

func (h *DownloadHandler) StopDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"taskId"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	h.downloaderService.StopDownload(req.TaskID)
	OK(w, map[string]string{"message": "停止成功"})
}

func (h *DownloadHandler) PauseDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"taskId"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	h.downloaderService.PauseDownload(req.TaskID)
	OK(w, map[string]string{"message": "暂停成功"})
}

func (h *DownloadHandler) ResumeDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"taskId"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	h.downloaderService.ResumeDownload(req.TaskID)
	OK(w, map[string]string{"message": "恢复成功"})
}

func (h *DownloadHandler) RetryDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"taskId"`
		Mode   string `json:"mode"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	err := h.downloaderService.RetryDownload(req.TaskID, req.Mode)
	if err != nil {
		Err(w, http.StatusInternalServerError, err.Error())
		return
	}

	OK(w, map[string]string{"message": "重试任务已启动"})
}

func (h *DownloadHandler) UploadToWebDAV(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string                `json:"taskId"`
		Config *service.WebDAVConfig `json:"config"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	err := h.downloaderService.UploadTaskToWebDAV(req.TaskID, req.Config)
	if err != nil {
		Err(w, http.StatusInternalServerError, err.Error())
		return
	}

	OK(w, map[string]string{"message": "上传任务已启动"})
}

func (h *DownloadHandler) AnalyzeM3U8(w http.ResponseWriter, r *http.Request) {
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

	info, err := h.downloaderService.AnalyzeURL(req.URL, req.Referer, req.Cookie)
	if err != nil {
		Err(w, http.StatusInternalServerError, err.Error())
		return
	}

	OK(w, info)
}
