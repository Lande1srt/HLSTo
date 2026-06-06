package handler

import (
	"encoding/json"
	"net/http"
	"m3u8-downloader-web/model"
	"m3u8-downloader-web/service"
)

type DownloadHandler struct {
	downloaderService *service.DownloaderService
	taskManager      *service.TaskManager
}

func NewDownloadHandler(downloaderService *service.DownloaderService, taskManager *service.TaskManager) *DownloadHandler {
	return &DownloadHandler{
		downloaderService: downloaderService,
		taskManager:      taskManager,
	}
}

func (h *DownloadHandler) StartDownload(w http.ResponseWriter, r *http.Request) {
	var req model.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	if req.URL == "" {
		h.sendError(w, http.StatusBadRequest, "URL 不能为空")
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

	task, err := h.downloaderService.StartDownload(req)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.sendSuccess(w, map[string]interface{}{
		"taskId": task.ID,
		"status": task.Status,
	})
}

func (h *DownloadHandler) StopDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"taskId"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	h.downloaderService.StopDownload(req.TaskID)
	h.sendSuccess(w, map[string]string{"message": "停止成功"})
}

func (h *DownloadHandler) PauseDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"taskId"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	h.downloaderService.PauseDownload(req.TaskID)
	h.sendSuccess(w, map[string]string{"message": "暂停成功"})
}

func (h *DownloadHandler) ResumeDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"taskId"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	h.downloaderService.ResumeDownload(req.TaskID)
	h.sendSuccess(w, map[string]string{"message": "恢复成功"})
}

func (h *DownloadHandler) RetryDownload(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"taskId"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	err := h.downloaderService.RetryDownload(req.TaskID)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.sendSuccess(w, map[string]string{"message": "重试任务已启动"})
}

func (h *DownloadHandler) UploadToWebDAV(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string               `json:"taskId"`
		Config *service.WebDAVConfig `json:"config"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	err := h.downloaderService.UploadTaskToWebDAV(req.TaskID, req.Config)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.sendSuccess(w, map[string]string{"message": "上传任务已启动"})
}

func (h *DownloadHandler) AnalyzeM3U8(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL     string `json:"url"`
		Referer string `json:"referer"`
		Cookie  string `json:"cookie"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	if req.URL == "" {
		h.sendError(w, http.StatusBadRequest, "URL 不能为空")
		return
	}

	info, err := h.downloaderService.AnalyzeURL(req.URL, req.Referer, req.Cookie)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, err.Error())
		return
	}

	h.sendSuccess(w, info)
}

func (h *DownloadHandler) sendSuccess(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(model.APIResponse{
		Code:    200,
		Message: "success",
		Data:    data,
	})
}

func (h *DownloadHandler) sendError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(model.APIResponse{
		Code:    status,
		Message: message,
	})
}
