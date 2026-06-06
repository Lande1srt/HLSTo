package handler

import (
	"encoding/json"
	"net/http"
	"m3u8-downloader-web/model"
	"m3u8-downloader-web/service"

	"github.com/gorilla/mux"
)

type TaskHandler struct {
	taskManager       *service.TaskManager
	downloaderService *service.DownloaderService
}

func NewTaskHandler(taskManager *service.TaskManager, downloaderService *service.DownloaderService) *TaskHandler {
	return &TaskHandler{
		taskManager:       taskManager,
		downloaderService: downloaderService,
	}
}

func (h *TaskHandler) ListTasks(w http.ResponseWriter, r *http.Request) {
	tasks := h.taskManager.ListTasks()
	h.sendSuccess(w, tasks)
}

func (h *TaskHandler) GetTask(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	task, exists := h.taskManager.GetTask(id)
	if !exists {
		h.sendError(w, http.StatusNotFound, "任务不存在")
		return
	}

	h.sendSuccess(w, task)
}

func (h *TaskHandler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	// 在删除前先停止任务（如果正在运行）
	h.downloaderService.StopDownload(id)
	
	h.taskManager.DeleteTask(id)
	h.sendSuccess(w, map[string]string{"message": "删除成功"})
}

func (h *TaskHandler) sendSuccess(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(model.APIResponse{
		Code:    200,
		Message: "success",
		Data:    data,
	})
}

func (h *TaskHandler) sendError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(model.APIResponse{
		Code:    status,
		Message: message,
	})
}
