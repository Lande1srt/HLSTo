package handler

import (
	"encoding/json"
	"net/http"

	"m3u8-downloader-web/model"
)

type DiskHandler struct{}

func NewDiskHandler() *DiskHandler {
	return &DiskHandler{}
}

type DiskInfo struct {
	MountPoint  string  `json:"mountPoint"`
	Device      string  `json:"device"`
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"`
	FreePercent float64 `json:"freePercent"`
	UsedPercent float64 `json:"usedPercent"`
	ColorClass  string  `json:"colorClass"`
	FsType      string  `json:"fsType"`
}

func (h *DiskHandler) GetDiskInfo(w http.ResponseWriter, r *http.Request) {
	path := "."
	
	if r.URL.Query().Get("path") != "" {
		path = r.URL.Query().Get("path")
	}
	
	diskInfo, err := getDiskInfo(path)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "获取磁盘信息失败: "+err.Error())
		return
	}
	
	h.sendSuccess(w, diskInfo)
}

func (h *DiskHandler) GetAllDisks(w http.ResponseWriter, r *http.Request) {
	disks, err := getAllDisks()
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "获取磁盘列表失败: "+err.Error())
		return
	}
	
	h.sendSuccess(w, disks)
}

func (h *DiskHandler) CheckSpace(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path string `json:"path"`
	}
	
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.sendError(w, http.StatusBadRequest, "无效的请求体")
		return
	}
	
	path := req.Path
	if path == "" {
		path = "."
	}
	
	diskInfo, err := getDiskInfo(path)
	if err != nil {
		h.sendError(w, http.StatusInternalServerError, "检查磁盘空间失败: "+err.Error())
		return
	}
	
	h.sendSuccess(w, diskInfo)
}

func getDiskInfo(path string) (*DiskInfo, error) {
	info, err := getDiskSpace(path)
	if err != nil {
		return nil, err
	}
	
	total := info.total
	free := info.free
	used := total - free
	
	freePercent := float64(free) / float64(total) * 100
	usedPercent := float64(used) / float64(total) * 100
	
	return &DiskInfo{
		MountPoint:  path,
		Total:       total,
		Used:        used,
		Free:        free,
		FreePercent: freePercent,
		UsedPercent: usedPercent,
		ColorClass:  getColorClass(usedPercent),
	}, nil
}

func getColorClass(usedPercent float64) string {
	if usedPercent >= 90 {
		return "danger"
	} else if usedPercent >= 75 {
		return "warning"
	} else if usedPercent >= 50 {
		return "info"
	}
	return "success"
}

type diskSpaceInfo struct {
	total uint64
	free  uint64
}

var getDiskSpace func(path string) (*diskSpaceInfo, error)
var getAllDisks func() ([]*DiskInfo, error)

func (h *DiskHandler) sendSuccess(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(model.APIResponse{
		Code:    200,
		Message: "success",
		Data:    data,
	})
}

func (h *DiskHandler) sendError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(model.APIResponse{
		Code:    status,
		Message: message,
	})
}