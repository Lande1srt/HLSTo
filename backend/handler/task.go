package handler

import (
	"fmt"
	"io"
	"m3u8-downloader-web/model"
	"m3u8-downloader-web/service"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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
	OK(w, tasks)
}

func (h *TaskHandler) GetTask(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	task, exists := h.taskManager.GetTask(id)
	if !exists {
		Err(w, http.StatusNotFound, "任务不存在")
		return
	}

	OK(w, task)
}

func (h *TaskHandler) DeleteTask(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	id := vars["id"]

	// 在删除前先停止任务（如果正在运行）
	h.downloaderService.StopDownload(id)

	h.taskManager.DeleteTask(id)
	OK(w, map[string]string{"message": "删除成功"})
}

// DownloadKey 下载任务关联的 HLS 加密密钥文件（密钥随任务持久存放）
func (h *TaskHandler) DownloadKey(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	task, exists := h.taskManager.GetTask(id)
	if !exists {
		Err(w, http.StatusNotFound, "任务不存在")
		return
	}
	if task.KeyPath == "" {
		Err(w, http.StatusNotFound, "该任务没有关联加密密钥")
		return
	}

	file, err := os.Open(task.KeyPath)
	if err != nil {
		Err(w, http.StatusNotFound, "密钥文件不存在或已被删除")
		return
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || info.Size() == 0 {
		Err(w, http.StatusInternalServerError, "密钥文件异常")
		return
	}

	// 下载名取密钥索引中记录的原始文件名（如用户指定 URL 中的名称），无记录回退 enc.key
	fileName := service.LookupHLSKeyFileName(id)
	if fileName == "" {
		fileName = "enc.key"
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))

	if _, err := io.Copy(w, file); err != nil {
		// 响应已开始写出，无法再返回统一错误体，仅记录
		return
	}
}

// logLevelAllowed 查询参数合法化
func logLevelAllowed(level string) bool {
	switch level {
	case "", "debug", "info", "warn", "error":
		return true
	default:
		return false
	}
}

// GetLogs 查询任务持久化日志（?level=debug/info/warn/error，?limit=N，默认上限 5000 条）
func (h *TaskHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	if _, exists := h.taskManager.GetTask(id); !exists {
		Err(w, http.StatusNotFound, "任务不存在")
		return
	}

	level := strings.TrimSpace(r.URL.Query().Get("level"))
	if !logLevelAllowed(level) {
		Err(w, http.StatusBadRequest, "非法的日志级别")
		return
	}

	limit := 5000
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			Err(w, http.StatusBadRequest, "非法的 limit 参数")
			return
		}
		limit = n
	}

	logs, err := h.taskManager.GetTaskLogsFiltered(id, level, limit)
	if err != nil {
		Err(w, http.StatusInternalServerError, "查询日志失败")
		return
	}

	OK(w, logs)
}

// DownloadLogs 将任务日志导出为 .log 文本流
func (h *TaskHandler) DownloadLogs(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["id"]

	task, exists := h.taskManager.GetTask(id)
	if !exists {
		Err(w, http.StatusNotFound, "任务不存在")
		return
	}

	logs, err := h.taskManager.GetTaskLogs(id)
	if err != nil {
		Err(w, http.StatusInternalServerError, "查询日志失败")
		return
	}

	var sb strings.Builder
	for _, entry := range logs {
		sb.WriteString(formatLogLine(entry))
		sb.WriteByte('\n')
	}
	if sb.Len() == 0 {
		sb.WriteString("（该任务暂无日志记录）\n")
	}

	filename := fmt.Sprintf("%s.log", sanitizeFilename(task.Name))
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	_, _ = io.WriteString(w, sb.String())
}

// formatLogLine 将日志条目格式化为 [2006-01-02 15:04:05] [LEVEL] message
func formatLogLine(entry *model.LogEntry) string {
	ts := entry.Timestamp
	if t, err := time.Parse(time.RFC3339, entry.Timestamp); err == nil {
		ts = t.Format("2006-01-02 15:04:05")
	}
	return fmt.Sprintf("[%s] [%s] %s", ts, strings.ToUpper(entry.Level), entry.Message)
}

// sanitizeFilename 去除文件名中的非法字符
func sanitizeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "task"
	}
	replacer := strings.NewReplacer(
		"\\", "_", "/", "_", ":", "_", "*", "_",
		"?", "_", "\"", "_", "<", "_", ">", "_", "|", "_",
	)
	return replacer.Replace(name)
}
