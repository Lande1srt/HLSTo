package handler

import (
	"encoding/json"
	"fmt"
	"m3u8-downloader-web/model"
	"m3u8-downloader-web/service"
	"m3u8-downloader-web/storage"
	"net/http"
	"strings"
)

type SettingsHandler struct {
	storage           *storage.SQLiteStorage
	schedulerService  *service.SchedulerService
	downloaderService *service.DownloaderService
	ffmpegService     *service.FFmpegService
	speedTestService  *service.SpeedTestService
}

func NewSettingsHandler(storage *storage.SQLiteStorage, scheduler *service.SchedulerService,
	downloader *service.DownloaderService, ffmpeg *service.FFmpegService,
	speedTest *service.SpeedTestService) *SettingsHandler {
	return &SettingsHandler{
		storage:           storage,
		schedulerService:  scheduler,
		downloaderService: downloader,
		ffmpegService:     ffmpeg,
		speedTestService:  speedTest,
	}
}

func (h *SettingsHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := h.storage.GetSettings()
	if err != nil {
		Err(w, http.StatusInternalServerError, "获取设置失败")
		return
	}
	OK(w, settings)
}

func (h *SettingsHandler) TestWebDAV(w http.ResponseWriter, r *http.Request) {
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

func (h *SettingsHandler) ListWebDAVDir(w http.ResponseWriter, r *http.Request) {
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

	OK(w, result)
}

func (h *SettingsHandler) ClearCache(w http.ResponseWriter, r *http.Request) {
	// 统一委托给 SchedulerService：避让活跃任务、跳过仍被任务引用的工作目录，
	// 保证设置页手动清理与定时/其他手动入口边界一致，避免重复实现产生遗漏。
	count := h.schedulerService.ClearCache()

	OK(w, map[string]interface{}{
		"message": fmt.Sprintf("已成功清除 %d 个缓存文件夹", count),
		"count":   count,
	})
}

func (h *SettingsHandler) SaveSettings(w http.ResponseWriter, r *http.Request) {
	var newSettings model.Settings
	if err := json.NewDecoder(r.Body).Decode(&newSettings); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	// 合法化字段，避免脏数据入库
	newSettings.MergeMethod = model.NormalizeMergeMethod(newSettings.MergeMethod)
	newSettings.FFmpegMuxMode = model.NormalizeMuxMode(newSettings.FFmpegMuxMode)
	newSettings.HLSEncryptMode = model.NormalizeHLSEncryptMode(newSettings.HLSEncryptMode)
	newSettings.HLSPackForm = model.NormalizeHLSPackForm(newSettings.HLSPackForm)

	// 加密以二次 HLS 分片开启为前提
	if newSettings.HLSEncryptEnabled && !newSettings.HLSPackEnabled {
		Err(w, http.StatusBadRequest, "HLS 加密需要先开启二次 HLS 分片")
		return
	}

	// 需要 FFmpeg 的设置：仅FFmpeg合并、二次 HLS 分片
	needFFmpeg := newSettings.MergeMethod == model.MergeMethodFFmpeg || newSettings.HLSPackEnabled
	if needFFmpeg {
		if h.ffmpegService == nil {
			Err(w, http.StatusBadRequest, "该设置依赖 FFmpeg 环境")
			return
		}
		status := h.ffmpegService.Status(true)
		if !status.Available {
			Err(w, http.StatusBadRequest, "该设置要求已检测到 FFmpeg 环境，请先检测或私有安装 FFmpeg")
			return
		}
	}

	// 指定密钥模式必须提供 key 文件 URL
	if newSettings.HLSEncryptEnabled &&
		newSettings.HLSEncryptMode == model.HLSEncryptSpecified &&
		strings.TrimSpace(newSettings.HLSKeyURL) == "" {
		Err(w, http.StatusBadRequest, "指定密钥模式必须填写 key 文件的 URL")
		return
	}

	if err := h.storage.SaveSettings(&newSettings); err != nil {
		Err(w, http.StatusInternalServerError, "保存设置失败")
		return
	}

	// 更新下载服务的并发、合并与二次 HLS 配置
	if h.downloaderService != nil {
		h.downloaderService.UpdateConcurrencyConfig(
			newSettings.DownloadConcurrency,
			newSettings.MergeConcurrency,
			newSettings.CompressConcurrency,
			newSettings.PackConcurrency,
			newSettings.UploadConcurrency,
			newSettings.SingleMode,
		)
		h.downloaderService.UpdatePostDownloadConfig(
			newSettings.MergeAfterDownload,
			newSettings.MergeMethod,
			newSettings.FFmpegMuxMode,
		)
		h.downloaderService.UpdateHLSPackConfig(
			newSettings.HLSPackEnabled,
			newSettings.HLSEncryptEnabled,
			newSettings.HLSEncryptMode,
			newSettings.HLSKeyURL,
			newSettings.HLSPackForm,
		)
		h.downloaderService.UpdateCompressConfig(
			newSettings.CompressAfterMerge,
			newSettings.CompressBitrateThreshold,
			newSettings.CompressTargetBitrate,
		)
	}

	OK(w, newSettings)
}

func (h *SettingsHandler) GetCleanupConfig(w http.ResponseWriter, r *http.Request) {
	config := h.schedulerService.GetConfig()
	OK(w, config)
}

func (h *SettingsHandler) UpdateCleanupConfig(w http.ResponseWriter, r *http.Request) {
	var config service.CleanupConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		Err(w, http.StatusBadRequest, "无效的请求体")
		return
	}

	h.schedulerService.UpdateConfig(config)
	OK(w, config)
}

// StartSpeedTest 启动 WebDAV 上传测速；请求体可携带 WebDAV 配置与 sizeMB（默认100）
func (h *SettingsHandler) StartSpeedTest(w http.ResponseWriter, r *http.Request) {
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
func (h *SettingsHandler) StopSpeedTest(w http.ResponseWriter, r *http.Request) {
	h.speedTestService.Stop()
	OK(w, map[string]string{"message": "已请求中断测速"})
}

// GetSpeedTestLog 返回最后一次测速日志（含实时行）
func (h *SettingsHandler) GetSpeedTestLog(w http.ResponseWriter, r *http.Request) {
	OK(w, h.speedTestService.Snapshot())
}
