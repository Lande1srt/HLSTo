package service

import (
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"m3u8-downloader-web/model"
	"m3u8-downloader-web/websocket"
)

const (
	HEAD_TIMEOUT     = 15 * time.Second
	TS_NAME_TEMPLATE = "%05d.ts"
)

type DownloaderService struct {
	taskManager   *TaskManager
	wsManager     *websocket.WebSocketManager
	ffmpegService *FFmpegService
	controls      map[string]*taskControl
	mu            sync.RWMutex

	downloadSem atomic.Pointer[semaphore]
	mergeSem    atomic.Pointer[semaphore]
	compressSem atomic.Pointer[semaphore] // 码率压缩（CPU 密集，默认串行）
	packSem     atomic.Pointer[semaphore] // 二次 HLS 分片（CPU 密集，默认串行）
	uploadSem   atomic.Pointer[semaphore]

	// 配置
	downloadConcurrency atomic.Int32
	mergeConcurrency    atomic.Int32
	compressConcurrency atomic.Int32
	packConcurrency     atomic.Int32
	uploadConcurrency   atomic.Int32
	singleMode          atomic.Bool
	singleSem           chan struct{} // 单状态模式下的全局信号量

	// 下载完成动作：合并（默认开启，自动方式）
	mergeAfterDownload atomic.Bool
	mergeMethod        atomic.Pointer[string]
	ffmpegMuxMode      atomic.Pointer[string]

	// 下载完成动作：二次 HLS 分片与加密（默认关闭）
	hlsPackEnabled    atomic.Bool
	hlsEncryptEnabled atomic.Bool
	hlsEncryptMode    atomic.Pointer[string]
	hlsKeyURL         atomic.Pointer[string]
	hlsPackForm       atomic.Pointer[string] // multi（默认）/ single（单 tsbin，可选）

	// 合并后高码率压缩（默认关闭，阈值默认 2048kbps）
	compressAfterMerge    atomic.Bool
	compressThresholdKbps atomic.Int32
	compressTargetKbps    atomic.Int32

	// 分片下载共享 HTTP client：复用 TCP/TLS 连接（keep-alive），避免每个分片、
	// 每次重试都新建 client→新建 Transport→重建连接，从而显著降低握手开销与
	// 拥塞窗口重启带来的额外带宽/时间消耗。所有分片 goroutine 并发安全共用。
	segHTTPClient *http.Client
}

func NewDownloaderService(taskManager *TaskManager, wsManager *websocket.WebSocketManager) *DownloaderService {
	ds := &DownloaderService{
		taskManager: taskManager,
		wsManager:   wsManager,
		controls:    make(map[string]*taskControl),
		singleSem:   make(chan struct{}, 1),
	}

	// 初始化原子指针
	ds.downloadSem.Store(newSemaphore(1))
	ds.mergeSem.Store(newSemaphore(1))
	ds.compressSem.Store(newSemaphore(1))
	ds.packSem.Store(newSemaphore(1))
	ds.uploadSem.Store(newSemaphore(1))

	// 初始化配置
	ds.downloadConcurrency.Store(1)
	ds.mergeConcurrency.Store(1)
	ds.compressConcurrency.Store(1)
	ds.packConcurrency.Store(1)
	ds.uploadConcurrency.Store(1)
	ds.singleMode.Store(false)

	// 初始化合并配置：默认开启合并，自动方式（检测 FFmpeg，兜底 Go）
	ds.mergeAfterDownload.Store(true)
	autoMethod := model.MergeMethodAuto
	ds.mergeMethod.Store(&autoMethod)
	defaultMode := model.MuxModeCopy
	ds.ffmpegMuxMode.Store(&defaultMode)

	// 初始化二次 HLS 配置：默认关闭，加密默认生成独立密钥
	generatedMode := model.HLSEncryptGenerated
	ds.hlsEncryptMode.Store(&generatedMode)
	emptyURL := ""
	ds.hlsKeyURL.Store(&emptyURL)

	// 初始化高码率压缩：默认关闭，阈值 2048kbps，目标码率 0（取阈值）
	ds.compressAfterMerge.Store(false)
	ds.compressThresholdKbps.Store(int32(model.DefaultCompressBitrateThreshold))
	ds.compressTargetKbps.Store(0)

	// 分片下载共享 client：开启 keep-alive，按主机保留足够空闲连接供多并发分片复用
	ds.segHTTPClient = &http.Client{
		Timeout: HEAD_TIMEOUT,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			// 分片下载并发度通常为个位数到十几，每主机保留 32 条空闲连接足够复用
			MaxIdleConns:          128,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}

	return ds
}

// SetFFmpegService 注入 ffmpeg 服务（合并阶段优先使用 ffmpeg）

func (ds *DownloaderService) SetFFmpegService(fs *FFmpegService) {
	ds.ffmpegService = fs
}

// UpdatePostDownloadConfig 更新下载完成后的合并配置；参数会被合法化

func (ds *DownloaderService) UpdatePostDownloadConfig(after bool, method, mode string) {
	normalizedMethod := model.NormalizeMergeMethod(method)
	normalizedMode := model.NormalizeMuxMode(mode)

	ds.mergeAfterDownload.Store(after)
	methodCopy := normalizedMethod
	ds.mergeMethod.Store(&methodCopy)
	modeCopy := normalizedMode
	ds.ffmpegMuxMode.Store(&modeCopy)

	log.Printf("[Downloader] 合并配置已更新: 合并=%v, 方式=%s, 编码模式=%s\n",
		after, normalizedMethod, normalizedMode)
}

// UpdateHLSPackConfig 更新二次 HLS 分片与加密配置；参数会被合法化

func (ds *DownloaderService) UpdateHLSPackConfig(enabled bool, encrypt bool, encMode, keyURL, packForm string) {
	normalizedEncMode := model.NormalizeHLSEncryptMode(encMode)
	normalizedForm := model.NormalizeHLSPackForm(packForm)

	ds.hlsPackEnabled.Store(enabled)
	ds.hlsEncryptEnabled.Store(encrypt)
	modeCopy := normalizedEncMode
	ds.hlsEncryptMode.Store(&modeCopy)
	URLCopy := strings.TrimSpace(keyURL)
	ds.hlsKeyURL.Store(&URLCopy)
	formCopy := normalizedForm
	ds.hlsPackForm.Store(&formCopy)

	log.Printf("[Downloader] 二次HLS配置已更新: 分片=%v, 加密=%v, 密钥来源=%s, 产物形态=%s\n",
		enabled, encrypt, normalizedEncMode, normalizedForm)
}

// UpdateCompressConfig 更新合并后码率检查与压缩配置；参数会被合法化

func (ds *DownloaderService) UpdateCompressConfig(enabled bool, thresholdKbps, targetKbps int) {
	threshold := thresholdKbps
	if threshold < 1 {
		threshold = model.DefaultCompressBitrateThreshold
	}
	target := targetKbps
	if target < 0 {
		target = 0
	}
	// 目标码率若指定，不应高于阈值（否则没有压缩意义），钳制到阈值
	if target > threshold {
		target = threshold
	}

	ds.compressAfterMerge.Store(enabled)
	ds.compressThresholdKbps.Store(int32(threshold))
	ds.compressTargetKbps.Store(int32(target))

	log.Printf("[Downloader] 高码率压缩配置已更新: 启用=%v, 阈值=%dkbps, 目标=%dkbps\n",
		enabled, threshold, target)
}

// getMergeMethod 读取当前合并方式（保证非空）

func (ds *DownloaderService) getMergeMethod() string {
	if p := ds.mergeMethod.Load(); p != nil {
		return *p
	}
	return model.MergeMethodAuto
}

// getMuxMode 读取当前编码模式（保证非空）

func (ds *DownloaderService) getMuxMode() string {
	if p := ds.ffmpegMuxMode.Load(); p != nil {
		return *p
	}
	return model.MuxModeCopy
}

// getHLSEncryptMode 读取加密密钥来源（保证非空）

func (ds *DownloaderService) getHLSEncryptMode() string {
	if p := ds.hlsEncryptMode.Load(); p != nil {
		return *p
	}
	return model.HLSEncryptGenerated
}

// getHLSKeyURL 读取指定密钥 URL（保证非空）

func (ds *DownloaderService) getHLSKeyURL() string {
	if p := ds.hlsKeyURL.Load(); p != nil {
		return *p
	}
	return ""
}

// getHLSPackForm 读取产物形态（保证非空，默认 multi）

func (ds *DownloaderService) getHLSPackForm() string {
	if p := ds.hlsPackForm.Load(); p != nil {
		return *p
	}
	return model.HLSPackFormMulti
}

func (ds *DownloaderService) UpdateConcurrencyConfig(download, merge, compress, pack, upload int, singleMode bool) {
	// 非法值兜底，防止 0/负数导致无缓冲信号量永久阻塞
	if download < 1 {
		download = 1
	}
	if merge < 1 {
		merge = 1
	}
	if compress < 1 {
		compress = 1
	}
	if pack < 1 {
		pack = 1
	}
	if upload < 1 {
		upload = 1
	}

	// 使用原子操作更新配置
	ds.downloadConcurrency.Store(int32(download))
	ds.mergeConcurrency.Store(int32(merge))
	ds.compressConcurrency.Store(int32(compress))
	ds.packConcurrency.Store(int32(pack))
	ds.uploadConcurrency.Store(int32(upload))
	ds.singleMode.Store(singleMode)

	// 动态调整同一信号量容量，不替换对象：运行中/等待中的任务计数连续，
	// 扩容立即唤醒等待者，缩容不影响已占用许可
	ds.downloadSem.Load().resize(download)
	ds.mergeSem.Load().resize(merge)
	ds.compressSem.Load().resize(compress)
	ds.packSem.Load().resize(pack)
	ds.uploadSem.Load().resize(upload)

	log.Printf("[Downloader] 并发配置已更新: 下载=%d, 合并=%d, 压缩=%d, 分片=%d, 上传=%d, 单模式=%v\n",
		download, merge, compress, pack, upload, singleMode)
}

func (ds *DownloaderService) getControl(taskID string) (*taskControl, bool) {
	ds.mu.RLock()
	defer ds.mu.RUnlock()
	ctrl, exists := ds.controls[taskID]
	return ctrl, exists
}

func (ds *DownloaderService) createControl(taskID string) *taskControl {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ctrl := &taskControl{
		taskID:  taskID,
		paused:  make(chan struct{}),
		resumed: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	ds.controls[taskID] = ctrl
	return ctrl
}

// removeControl 移除任务控制块。仅当 map 中仍是本代 ctrl 时才删除——
// 重试会创建新一代 ctrl，避免旧 goroutine 退出时误删新 ctrl 导致暂停/停止信号失效。

func (ds *DownloaderService) removeControl(taskID string, ctrl *taskControl) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if cur, ok := ds.controls[taskID]; ok && cur == ctrl {
		delete(ds.controls, taskID)
	}
}

func (ds *DownloaderService) StartDownload(req model.DownloadRequest) (*model.Task, error) {
	taskID := uuid.New().String()
	task := &model.Task{
		ID:          taskID,
		URL:         req.URL,
		Name:        req.OutputName,
		Status:      model.StatusPending,
		Progress:    0,
		Speed:       "0 KB/s",
		ThreadCount: req.ThreadCount,
		HostType:    req.HostType,
		Cookie:      req.Cookie,
		Referer:     req.Referer,
		AutoClear:   req.AutoClear,
		SavePath:    req.SavePath,
		CreatedAt:   time.Now(),
	}

	// 保存 WebDAV 相关设置到任务对象中，以便后续重试或手动上传
	task.EnableWebDAV = req.EnableWebDAV
	task.WebDAVURL = req.WebDAVURL
	task.WebDAVUsername = req.WebDAVUsername
	task.WebDAVPassword = req.WebDAVPassword
	task.WebDAVRemoteDir = req.WebDAVRemoteDir
	task.DeleteAfterUpload = req.DeleteAfterUpload

	ds.taskManager.AddTask(task)
	ds.createControl(taskID)

	go ds.download(taskID, req)

	return task, nil
}

func (ds *DownloaderService) sendProgress(taskID string, progress float64, speed string, downloaded, total int) {
	// 同时更新 TaskManager 中的状态，确保刷新页面后进度不会回退
	ds.taskManager.UpdateProgress(taskID, progress, speed, downloaded, total)

	msg := model.WebSocketMessage{
		Type:               "progress",
		TaskID:             taskID,
		Progress:           progress,
		Speed:              speed,
		DownloadedSegments: downloaded,
		TotalSegments:      total,
		Timestamp:          time.Now().Format(time.RFC3339),
	}
	ds.wsManager.BroadcastToTask(taskID, msg)
}

func (ds *DownloaderService) sendLog(taskID, level, message string) {
	log.Printf("[%s] [%s] %s\n", taskID, level, message)

	ds.taskManager.AddLog(taskID, level, message)

	msg := model.WebSocketMessage{
		Type:      "log",
		TaskID:    taskID,
		Level:     level,
		Message:   message,
		Timestamp: time.Now().Format(time.RFC3339),
	}
	ds.wsManager.BroadcastToTask(taskID, msg)
}

func (ds *DownloaderService) sendStatus(taskID string, status model.TaskStatus, message string) {
	ds.taskManager.UpdateStatus(taskID, status, message)

	var outputPath string
	if status == model.StatusCompleted {
		if task, exists := ds.taskManager.GetTask(taskID); exists {
			outputPath = task.OutputPath
		}
	}

	msg := model.WebSocketMessage{
		Type:       "status",
		TaskID:     taskID,
		Status:     string(status),
		Message:    message,
		Timestamp:  time.Now().Format(time.RFC3339),
		OutputPath: outputPath,
	}
	ds.wsManager.BroadcastToTask(taskID, msg)
}

func pathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
