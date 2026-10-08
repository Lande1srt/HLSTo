package handler

import (
	"net/http"

	"m3u8-downloader-web/service"
)

// FFmpegHandler 处理 ffmpeg 环境检测与私有安装请求
type FFmpegHandler struct {
	ffmpegService *service.FFmpegService
}

// NewFFmpegHandler 创建 ffmpeg handler
func NewFFmpegHandler(ffmpegService *service.FFmpegService) *FFmpegHandler {
	return &FFmpegHandler{ffmpegService: ffmpegService}
}

// Status 返回 ffmpeg 检测与安装状态
// GET /api/ffmpeg/status
func (h *FFmpegHandler) Status(w http.ResponseWriter, r *http.Request) {
	// forceRefresh 参数可跳过检测缓存
	forceRefresh := r.URL.Query().Get("forceRefresh") == "1"
	OK(w, h.ffmpegService.Status(forceRefresh))
}

// Install 启动后台私有安装；安装进度通过 Status 接口轮询获取
// POST /api/ffmpeg/install
func (h *FFmpegHandler) Install(w http.ResponseWriter, r *http.Request) {
	if err := h.ffmpegService.StartInstall(); err != nil {
		Err(w, http.StatusConflict, err.Error())
		return
	}
	OK(w, h.ffmpegService.Status(false))
}
