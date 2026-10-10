package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"m3u8-downloader-web/model"
	"m3u8-downloader-web/websocket"

	"github.com/google/uuid"
	"github.com/yapingcat/gomedia/go-mp4"
	"github.com/yapingcat/gomedia/go-mpeg2"
)

const (
	HEAD_TIMEOUT     = 15 * time.Second
	TS_NAME_TEMPLATE = "%05d.ts"
)

type TsInfo struct {
	Name string
	Url  string
}

type taskControl struct {
	paused   chan struct{}
	resumed  chan struct{}
	stopped  chan struct{}
	mu       sync.Mutex
	isPaused bool
}

// semaphore 动态容量信号量。容量可在运行时调整，正在等待/运行的任务
// 始终使用同一个对象——避免"替换 channel"导致新老任务计数脱节。
type semaphore struct {
	mu      sync.Mutex
	cur     int // 当前占用数
	size    int // 容量上限
	waiters []chan struct{}
}

func newSemaphore(size int) *semaphore {
	if size < 1 {
		size = 1
	}
	return &semaphore{size: size}
}

// resize 调整容量。扩容时立即唤醒可能满足条件的等待者。
func (s *semaphore) resize(size int) {
	if size < 1 {
		size = 1
	}
	s.mu.Lock()
	s.size = size
	s.wakeLocked()
	s.mu.Unlock()
}

// wakeLocked 按 FIFO 唤醒所有当前可获得许可的等待者（调用方须持锁）
func (s *semaphore) wakeLocked() {
	for len(s.waiters) > 0 && s.cur < s.size {
		w := s.waiters[0]
		s.waiters = s.waiters[1:]
		s.cur++
		close(w)
	}
}

// tryAcquire 非阻塞尝试获取许可
func (s *semaphore) tryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur < s.size {
		s.cur++
		return true
	}
	return false
}

// acquire 阻塞等待许可，stop 被关闭时放弃并返回 false（不占用许可）
func (s *semaphore) acquire(stop <-chan struct{}) bool {
	s.mu.Lock()
	if s.cur < s.size {
		s.cur++
		s.mu.Unlock()
		return true
	}
	wait := make(chan struct{})
	s.waiters = append(s.waiters, wait)
	s.mu.Unlock()

	select {
	case <-wait:
		return true
	case <-stop:
		s.mu.Lock()
		// 若已被唤醒（与 stop 竞争），保留许可视为成功
		select {
		case <-wait:
			s.mu.Unlock()
			return true
		default:
		}
		// 从等待队列移除自己
		for i, w := range s.waiters {
			if w == wait {
				s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		return false
	}
}

func (s *semaphore) release() {
	s.mu.Lock()
	if s.cur > 0 {
		s.cur--
	}
	s.wakeLocked()
	s.mu.Unlock()
}

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

func (ds *DownloaderService) download(taskID string, req model.DownloadRequest) {
	ctrl, _ := ds.getControl(taskID)
	defer ds.removeControl(taskID, ctrl)
	runtime.GOMAXPROCS(runtime.NumCPU())

	var downloadDir string

	// 记录任务是否成功完成
	taskCompleted := false
	defer func() {
		if !taskCompleted && downloadDir != "" {
			// 失败清理：仍含有有效分片时保留目录，供"重试缺失分片/强制合并"恢复；
			// 无任何分片（分析阶段失败等）才整目录清理，避免空壳残留
			if n := countValidSegments(filepath.Join(downloadDir, "cache")); n > 0 {
				log.Printf("[Cleanup] 任务失败但保留 %d 个已下载分片，等待恢复: %s\n", n, downloadDir)
				return
			}
			if err := os.RemoveAll(downloadDir); err != nil {
				log.Printf("[Cleanup] 清理失败目录时出错: %v\n", err)
			} else {
				log.Printf("[Cleanup] 已清理失败任务的空缓存目录: %s\n", downloadDir)
			}
		}
	}()

	ds.sendStatus(taskID, model.StatusPending, "正在等待下载队列...")

	if ds.singleMode.Load() {
		select {
		case ds.singleSem <- struct{}{}:
			// 获得全局锁
		case <-ctrl.stopped:
			return
		}
		defer func() { <-ds.singleSem }()
	}

	downloadSem := ds.downloadSem.Load()
	if !downloadSem.acquire(ctrl.stopped) {
		return
	}

	// 确保下载权释放
	downloadFinished := false
	defer func() {
		if !downloadFinished {
			downloadSem.release()
		}
	}()

	ds.sendLog(taskID, "info", fmt.Sprintf("开始下载: %s (WebDAV上传: %v)", req.URL, req.EnableWebDAV))

	pwd, err := os.Getwd()
	if err != nil {
		return
	}
	if req.SavePath != "" {
		// 防御路径穿越：清理并转换为绝对路径，确保在合法范围内
		cleanPath := filepath.Clean(req.SavePath)
		pwd = cleanPath
	}

	// 过滤文件名中的非法字符
	req.OutputName = sanitizeFileName(req.OutputName)

	if req.RetryMode != "" {
		// 重试：复用原工作目录（失败时保留下来的分片在此），修复新旧目录错位；
		// 老任务无 WorkDir 记录时回退 {pwd}/{OutputName}
		downloadDir = ""
		if t, ok := ds.taskManager.GetTask(taskID); ok && t.WorkDir != "" {
			downloadDir = t.WorkDir
		}
		if downloadDir == "" {
			downloadDir = filepath.Join(pwd, req.OutputName)
		}
	} else {
		// 新下载：使用时间戳作为目录名
		timestamp := time.Now().Format("0601020304")
		downloadDir = filepath.Join(pwd, fmt.Sprintf("download_%s", timestamp))
	}
	cacheDir := filepath.Join(downloadDir, "cache")

	if exists, _ := pathExists(cacheDir); !exists {
		os.MkdirAll(cacheDir, os.ModePerm)
	}

	// 持久化工作目录：后续重试/强制合并据此精确定位（老任务下次运行自动补齐）
	ds.setTaskWorkDir(taskID, downloadDir)

	ds.sendStatus(taskID, model.StatusDownloading, "正在分析下载地址...")

	isM3U8 := strings.Contains(strings.ToLower(req.URL), ".m3u8")
	var mv string

	if isM3U8 {
		m3u8Host := ds.getHost(req.URL, req.HostType)
		m3u8Body := ds.getM3u8Body(req.URL, req.Referer, req.Cookie)
		if m3u8Body == "" {
			ds.sendStatus(taskID, model.StatusFailed, "无法获取 m3u8 内容，请检查 URL 是否有效")
			return
		}

		key := ds.getM3u8Key(m3u8Host, m3u8Body, req.Referer, req.Cookie)
		if key != "" {
			ds.sendLog(taskID, "info", fmt.Sprintf("待解密 ts 文件 key: %s", key))
		}

		tsList := ds.getTsList(m3u8Host, m3u8Body)
		ds.sendLog(taskID, "info", fmt.Sprintf("待下载 ts 文件数量: %d", len(tsList)))

		ds.taskManager.mu.Lock()
		if task, exists := ds.taskManager.tasks[taskID]; exists {
			task.TotalSegments = len(tsList)
		}
		ds.taskManager.mu.Unlock()

		if !ds.downloader(taskID, req.ThreadCount, key, cacheDir, tsList, ctrl, req.Referer, req.Cookie) {
			// Task was stopped or failed
			return
		}

		if ok := ds.checkTsDownDir(taskID, cacheDir, tsList, false); !ok {
			ds.sendStatus(taskID, model.StatusFailed, "合并前检查失败: 文件不完整")
			return
		}

		downloadFinished = true
		downloadSem.release()

		// 用户关闭"下载完成后合并"：保留 TS 分片作为产物，任务完成；
		// 按确认结论不允许二次 HLS 分片与上传
		if !ds.mergeAfterDownload.Load() {
			ds.taskManager.mu.Lock()
			if t, exists := ds.taskManager.tasks[taskID]; exists {
				t.OutputPath = cacheDir
				now := time.Now()
				t.CompletedAt = &now
			}
			ds.taskManager.mu.Unlock()
			ds.sendStatus(taskID, model.StatusCompleted, "已保留 TS 分片（未合并）")
			return
		}

		// 暂停关卡：下载结束、进入合并前。被停止则直接退出
		if !waitIfPaused(ctrl) {
			return
		}

		// --- 阶段 2: 合并 ---
		ds.sendStatus(taskID, model.StatusMerging, "正在等待合并队列...")
		ds.sendProgress(taskID, 0, "等待队列", 0, 0)

		// 获取当前的合并信号量（使用原子指针）
		mergeSem := ds.mergeSem.Load()
		if !mergeSem.acquire(ctrl.stopped) {
			return
		}

		// 确保合并权释放
		mergeFinished := false
		defer func() {
			if !mergeFinished {
				mergeSem.release()
			}
		}()

		ds.sendStatus(taskID, model.StatusMerging, "正在合并文件...")
		ds.sendProgress(taskID, 0, "开始合并", 0, 0)
		mv = ds.mergeTs(taskID, cacheDir, downloadDir, req.OutputName, ctrl)

		if mv == "" {
			// 说明合并被停止或出错
			return
		}

		// 合并完成
		ds.sendStatus(taskID, model.StatusMerging, "合并完成")
		ds.sendLog(taskID, "info", "合并完成")

		if req.AutoClear {
			if err := os.RemoveAll(cacheDir); err != nil {
				log.Printf("[Downloader] 删除缓存目录失败: %v\n", err)
				ds.sendLog(taskID, "warn", fmt.Sprintf("删除缓存目录失败: %v", err))
			}
		}

		mergeFinished = true
		mergeSem.release() // 合并阶段结束，释放信号量
	} else {
		if !ds.checkIsVideoURL(req.URL, req.Referer, req.Cookie) {
			ds.sendLog(taskID, "error", "不支持的下载类型，仅支持主流视频格式")
			ds.sendStatus(taskID, model.StatusFailed, "不支持的视频格式")
			return
		}

		ds.sendStatus(taskID, model.StatusDownloading, "正在下载通用视频文件...")
		// 自动推断扩展名
		ext := ".mp4"
		if u, err := url.Parse(req.URL); err == nil {
			pathExt := filepath.Ext(u.Path)
			if pathExt != "" {
				ext = pathExt
			}
		}
		mv = filepath.Join(downloadDir, req.OutputName+ext)

		if !ds.downloadSingleFile(taskID, req.URL, mv, req.Referer, req.Cookie, ctrl) {
			return
		}
		downloadFinished = true
		downloadSem.release() // 下载阶段结束，释放信号量

		// 通用文件跳过合并阶段
		ds.sendLog(taskID, "info", "通用文件下载完成，跳过合并阶段")
	}

	// 更新输出路径到任务对象
	ds.taskManager.mu.Lock()
	if task, exists := ds.taskManager.tasks[taskID]; exists {
		task.OutputPath = mv
	}
	ds.taskManager.mu.Unlock()

	// 暂停关卡：合并结束、进入压缩前。被停止则直接退出
	if !waitIfPaused(ctrl) {
		return
	}

	// --- 阶段 2.4: 高码率压缩（可选，依赖 FFmpeg；在 HLS 分片/上传之前）---
	mv, compressOK, compressStopped := ds.compressIfHighBitrate(taskID, mv, ctrl)
	if compressStopped {
		return
	}
	if !compressOK {
		ds.sendStatus(taskID, model.StatusFailed, "码率压缩失败，请通过重试功能处理")
		return
	}
	ds.taskManager.mu.Lock()
	if task, exists := ds.taskManager.tasks[taskID]; exists {
		task.OutputPath = mv
	}
	ds.taskManager.mu.Unlock()

	// --- 阶段 2.5: 二次 HLS 分片（可选，依赖 FFmpeg）---
	hlsDir := ""
	if ds.hlsPackEnabled.Load() {
		// 暂停关卡：压缩结束、进入二次分片前。被停止则直接退出
		if !waitIfPaused(ctrl) {
			return
		}

		var packOK bool
		hlsDir, packOK = ds.packageAsHLS(taskID, mv, downloadDir, req.OutputName,
			ds.hlsEncryptEnabled.Load(), ds.getHLSEncryptMode(), ds.getHLSKeyURL(),
			ds.getHLSPackForm(), req.NodeOrigin, ctrl)
		if !packOK {
			return
		}
	}

	// --- 阶段 3: 上传 ---
	if req.EnableWebDAV && req.WebDAVURL != "" {
		// 暂停关卡：分片结束、进入上传前。被停止则直接退出
		if !waitIfPaused(ctrl) {
			return
		}

		ds.sendStatus(taskID, model.StatusUploading, "正在等待上传队列...")

		// 获取当前的上传信号量（使用原子指针）
		uploadSem := ds.uploadSem.Load()
		if !uploadSem.acquire(ctrl.stopped) {
			return
		}
		defer uploadSem.release()

		ds.sendStatus(taskID, model.StatusUploading, "正在上传到 WebDAV...")
		ds.sendLog(taskID, "info", fmt.Sprintf("开始上传到 WebDAV: %s", req.WebDAVURL))
		// 重置进度为 0，开始上传阶段
		ds.sendProgress(taskID, 0, "准备上传", 0, 0)

		webdavConfig := WebDAVConfig{
			Enabled:   true,
			URL:       req.WebDAVURL,
			Username:  req.WebDAVUsername,
			Password:  req.WebDAVPassword,
			RemoteDir: req.WebDAVRemoteDir,
		}

		webdavService := NewWebDAVService(webdavConfig)

		var err error
		if hlsDir != "" {
			// 二次 HLS 分片开启：上传整个 HLS 目录（m3u8 + 分片，密钥按策略剔除）
			folderName := req.OutputName + "_hls"
			err = ds.uploadHLSDirectory(taskID, webdavService, hlsDir,
				folderName, req.WebDAVRemoteDir, ctrl)
			if err == nil {
				// 回写可播放 m3u8 地址（含 BYTERANGE 或目录两种形态，路径一致）
				remoteM3U8 := path.Join(req.WebDAVRemoteDir, folderName, "index.m3u8")
				ds.setTaskPlayURL(taskID, webdavService.PublicURL(remoteM3U8))
			}
		} else {
			remoteFileName := req.OutputName + ".mp4"
			if !isM3U8 {
				remoteFileName = filepath.Base(mv)
			}

			err = webdavService.UploadFile(mv, remoteFileName, ctrl.stopped, func(downloaded, total int64, speed string) {
				progress := 0.0
				if total > 0 {
					progress = float64(downloaded) / float64(total) * 100
				}
				// 将字节转换为 KB 以便在前端显示，KB 比较稳妥且能显示更多细节
				curKB := int(downloaded / 1024)
				totalKB := int(total / 1024)
				ds.sendProgress(taskID, progress, speed, curKB, totalKB)
			})
		}

		if err != nil {
			// 检查是否是用户主动停止
			isStopped := false
			select {
			case <-ctrl.stopped:
				isStopped = true
			default:
			}

			if isStopped {
				ds.sendLog(taskID, "warn", "WebDAV 上传已被用户停止")
				ds.sendStatus(taskID, model.StatusFailed, "上传已停止")
				return
			}

			ds.sendLog(taskID, "error", fmt.Sprintf("WebDAV 上传失败: %v", err))
			// 上传失败后，任务仍标记为完成，但记录错误
			ds.markTaskCompleted(taskID, mv, fmt.Sprintf("下载完成但上传失败: %v", err))
		} else {
			ds.sendLog(taskID, "info", "WebDAV 上传成功")
			ds.markTaskCompleted(taskID, mv, "下载并上传完成")

			if req.DeleteAfterUpload {
				if err := os.RemoveAll(downloadDir); err != nil {
					log.Printf("[Downloader] 删除本地下载目录失败: %v\n", err)
					ds.sendLog(taskID, "warn", fmt.Sprintf("删除本地下载目录失败: %v", err))
				} else {
					ds.sendLog(taskID, "info", "已清理本地下载目录")
				}
			}
		}
	} else {
		// 未开启 WebDAV，直接标记为完成
		ds.markTaskCompleted(taskID, mv, "下载完成")
	}

	taskCompleted = true
	log.Printf("[Success] 下载保存路径：%s\n", mv)
}

// 辅助方法：统一标记任务完成
func (ds *DownloaderService) markTaskCompleted(taskID string, outputPath string, message string) {
	var total int
	ds.taskManager.mu.Lock()
	if task, exists := ds.taskManager.tasks[taskID]; exists {
		task.Status = model.StatusCompleted
		task.Progress = 100
		task.OutputPath = outputPath
		now := time.Now()
		task.CompletedAt = &now
		total = task.TotalSegments
		task.DownloadedSegments = total

		// 通用视频（TotalBytes > 0）收尾：确保 DownloadedBytes = TotalBytes
		if task.TotalBytes > 0 {
			task.DownloadedBytes = task.TotalBytes
		}
	}
	ds.taskManager.mu.Unlock()

	ds.sendStatus(taskID, model.StatusCompleted, message)
	ds.sendProgress(taskID, 100, "完成", total, total)
}

// setTaskPlayURL 记录任务可播放的 m3u8 远程地址并持久化
func (ds *DownloaderService) setTaskPlayURL(taskID, playURL string) {
	var taskForPersist *model.Task
	ds.taskManager.mu.Lock()
	if t, exists := ds.taskManager.tasks[taskID]; exists {
		t.PlayURL = playURL
		taskForPersist = t
	}
	ds.taskManager.mu.Unlock()

	if taskForPersist != nil {
		ds.taskManager.UpdateTask(taskForPersist)
	}
}

// setTaskWorkDir 记录任务工作目录并异步持久化
func (ds *DownloaderService) setTaskWorkDir(taskID, workDir string) {
	var taskForPersist *model.Task
	ds.taskManager.mu.Lock()
	if t, exists := ds.taskManager.tasks[taskID]; exists {
		t.WorkDir = workDir
		taskForPersist = t
	}
	ds.taskManager.mu.Unlock()

	if taskForPersist != nil {
		ds.taskManager.UpdateTask(taskForPersist)
	}
}

func (ds *DownloaderService) getHost(Url string, ht string) string {
	u, err := url.Parse(Url)
	if err != nil {
		return ""
	}
	switch ht {
	case "v1":
		return u.Scheme + "://" + u.Host + path.Dir(u.EscapedPath())
	case "v2":
		return u.Scheme + "://" + u.Host
	}
	return u.Scheme + "://" + u.Host
}

func (ds *DownloaderService) getM3u8Body(Url string, referer string, cookie string) string {
	referer = normalizeReferrer(referer)
	cookie = sanitizeHeader(cookie)

	// 使用自定义 http.Client 以便在重定向时保留 Referer 和 Cookie
	client := &http.Client{
		Timeout: HEAD_TIMEOUT,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			// 关键：在重定向请求中手动补回 Referer, Origin 和 Cookie
			if referer != "" {
				req.Header.Set("Referer", referer)
				if u, err := url.Parse(referer); err == nil {
					req.Header.Set("Origin", u.Scheme+"://"+u.Host)
				}
			}
			if cookie != "" {
				req.Header.Set("Cookie", cookie)
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
			return nil
		},
	}

	req, err := http.NewRequest("GET", Url, nil)
	if err != nil {
		return ""
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	if referer != "" {
		req.Header.Set("Referer", referer)
		if u, err := url.Parse(referer); err == nil {
			req.Header.Set("Origin", u.Scheme+"://"+u.Host)
		}
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}

	return string(body)
}

func (ds *DownloaderService) getM3u8Key(host, html string, referer string, cookie string) string {
	referer = normalizeReferrer(referer)
	cookie = sanitizeHeader(cookie)
	lines := strings.Split(html, "\n")
	for _, line := range lines {
		if strings.Contains(line, "#EXT-X-KEY") {
			if !strings.Contains(line, "URI") {
				continue
			}
			uriPos := strings.Index(line, "URI")
			quotationMarkPos := strings.LastIndex(line, "\"")
			keyUrl := strings.Split(line[uriPos:quotationMarkPos], "\"")[1]
			if !strings.Contains(line, "http") {
				keyUrl = fmt.Sprintf("%s/%s", host, keyUrl)
			}

			// 使用自定义 http.Client 以便在重定向时保留 Referer 和 Cookie
			client := &http.Client{
				Timeout: HEAD_TIMEOUT,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if len(via) >= 10 {
						return fmt.Errorf("too many redirects")
					}
					if referer != "" {
						req.Header.Set("Referer", referer)
						if u, err := url.Parse(referer); err == nil {
							req.Header.Set("Origin", u.Scheme+"://"+u.Host)
						}
					}
					if cookie != "" {
						req.Header.Set("Cookie", cookie)
					}
					req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
					return nil
				},
			}

			req, err := http.NewRequest("GET", keyUrl, nil)
			if err != nil {
				continue
			}

			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, Gecko) Chrome/120.0.0.0 Safari/537.36")
			req.Header.Set("Connection", "keep-alive")
			req.Header.Set("Accept", "*/*")

			if referer != "" {
				req.Header.Set("Referer", referer)
				if u, err := url.Parse(referer); err == nil {
					req.Header.Set("Origin", u.Scheme+"://"+u.Host)
				}
			}
			if cookie != "" {
				req.Header.Set("Cookie", cookie)
			}

			res, err := client.Do(req)
			if err != nil || res.StatusCode != 200 {
				continue
			}
			defer res.Body.Close()

			body, err := io.ReadAll(res.Body)
			if err != nil {
				continue
			}
			return string(body)
		}
	}
	return ""
}

func (ds *DownloaderService) getTsList(host, body string) []TsInfo {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	var tsList []TsInfo
	index := 0

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// 只要不是以 # 开头的行，都视为分片地址（兼容 .jpeg, .png 等伪装后缀）
		if !strings.HasPrefix(line, "#") {
			index++
			fullUrl := ""
			if strings.HasPrefix(line, "http") {
				fullUrl = line
			} else if strings.HasPrefix(line, "//") {
				// 处理协议相对路径
				fullUrl = "https:" + line
			} else {
				// 处理相对路径
				line = strings.TrimPrefix(line, "/")
				fullUrl = fmt.Sprintf("%s/%s", host, line)
			}

			tsList = append(tsList, TsInfo{
				Name: fmt.Sprintf(TS_NAME_TEMPLATE, index),
				Url:  fullUrl,
			})
		}
	}
	return tsList
}

func (ds *DownloaderService) downloadTsFile(ts TsInfo, downloadDir, key string, retries int, referer string, cookie string) (int64, bool) {
	referer = normalizeReferrer(referer)
	cookie = sanitizeHeader(cookie)
	currPathFile := filepath.Join(downloadDir, ts.Name)
	if exists, _ := pathExists(currPathFile); exists {
		if stat, err := os.Stat(currPathFile); err == nil && stat.Size() > 0 {
			return stat.Size(), true
		}
		// 0 字节残片不视为已下载，移除后重新获取
		os.Remove(currPathFile)
	}

	// 预先准备好 Origin
	var origin string
	if referer != "" {
		if u, err := url.Parse(referer); err == nil {
			origin = u.Scheme + "://" + u.Host
		}
	}

	for attempt := 0; attempt < retries; attempt++ {
		success, size := func() (bool, int64) {
			client := &http.Client{
				Timeout: HEAD_TIMEOUT,
				CheckRedirect: func(req *http.Request, via []*http.Request) error {
					if len(via) >= 10 {
						return fmt.Errorf("too many redirects")
					}
					if referer != "" {
						req.Header.Set("Referer", referer)
						if origin != "" {
							req.Header.Set("Origin", origin)
						}
					}
					if cookie != "" {
						req.Header.Set("Cookie", cookie)
					}
					req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
					return nil
				},
			}

			req, err := http.NewRequest("GET", ts.Url, nil)
			if err != nil {
				return false, 0
			}

			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
			req.Header.Set("Connection", "keep-alive")
			req.Header.Set("Accept", "*/*")
			req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

			if referer != "" {
				req.Header.Set("Referer", referer)
				if origin != "" {
					req.Header.Set("Origin", origin)
				}
			}
			if cookie != "" {
				req.Header.Set("Cookie", cookie)
			}

			res, err := client.Do(req)
			if err != nil || res.StatusCode != http.StatusOK {
				return false, 0
			}
			defer res.Body.Close()

			origData, err := io.ReadAll(res.Body)
			if err != nil || len(origData) == 0 {
				return false, 0
			}

			// 验证 Content-Length
			contentLen := res.ContentLength
			if contentLen > 0 && int64(len(origData)) != contentLen {
				return false, 0
			}

			// 如果有加密，先解密
			if key != "" {
				origData, err = ds.AesDecrypt(origData, []byte(key))
				if err != nil {
					return false, 0
				}
			}

			// 核心逻辑：处理伪装后缀（如 .jpeg 头部包含图片数据的情况）
			syncByte := uint8(71) // 0x47
			bLen := len(origData)
			foundSync := false
			for j := 0; j < bLen-188; j++ {
				if origData[j] == syncByte && origData[j+188] == syncByte {
					origData = origData[j:]
					foundSync = true
					break
				}
			}

			if !foundSync {
				for j := 0; j < bLen; j++ {
					if origData[j] == syncByte {
						origData = origData[j:]
						break
					}
				}
			}

			// 写入临时文件
			tempPath := currPathFile + ".tmp"
			err = os.WriteFile(tempPath, origData, 0666)
			if err != nil {
				return false, 0
			}

			// 重命名为正式文件
			err = os.Rename(tempPath, currPathFile)
			if err != nil {
				os.Remove(tempPath)
				return false, 0
			}

			return true, int64(len(origData))
		}()

		if success {
			return size, true
		}

		// 失败重试前稍微等待一下，避免瞬时网络问题
		if attempt < retries-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	return 0, false
}

func (ds *DownloaderService) downloader(taskID string, maxGoroutines int, key string, cacheDir string, tsList []TsInfo, ctrl *taskControl, referer string, cookie string) bool {
	retry := 3
	// 防御：并发数非法（老任务/异常配置为 0）会导致无缓冲 limiter 死锁，强制兜底为 1
	if maxGoroutines < 1 {
		maxGoroutines = 1
	}
	limiter := make(chan struct{}, maxGoroutines)
	tsLen := len(tsList)
	var countMu sync.Mutex

	// 速度统计
	var totalBytes int64
	startTime := time.Now()
	lastUpdateTime := time.Now()

	// 更新状态为正在下载分片
	ds.sendStatus(taskID, model.StatusDownloading, "正在下载分片...")

	// segmentReady 判断分片是否真正可用于合并：存在且非空。
	// 0 字节残片（CDN 空响应/异常中断）视为缺失，否则会永远跳过下载导致产物损坏。
	getMissing := func() []TsInfo {
		var missing []TsInfo
		for _, ts := range tsList {
			currPathFile := filepath.Join(cacheDir, ts.Name)
			info, err := os.Stat(currPathFile)
			if err != nil || info.Size() == 0 {
				if err == nil {
					os.Remove(currPathFile) // 清理 0 字节残片
				}
				missing = append(missing, ts)
			}
		}
		return missing
	}

	missingTs := getMissing()
	downloadCount := tsLen - len(missingTs)
	maxGlobalRetries := 3

	for attempt := 0; attempt <= maxGlobalRetries; attempt++ {
		if len(missingTs) == 0 {
			break
		}

		if attempt > 0 {
			ds.sendLog(taskID, "warn", fmt.Sprintf("发现 %d 个分片丢失，正在进行第 %d 次重试下载", len(missingTs), attempt))
		}

		var wg sync.WaitGroup
		for _, ts := range missingTs {
			// Check for stop or pause
			select {
			case <-ctrl.stopped:
				return false
			case <-ctrl.paused:
				ds.sendLog(taskID, "info", "下载已暂停")
				select {
				case <-ctrl.resumed:
					ds.sendLog(taskID, "info", "下载已恢复")
				case <-ctrl.stopped:
					return false
				}
			default:
			}

			wg.Add(1)
			limiter <- struct{}{}

			go func(ts TsInfo) {
				defer func() {
					wg.Done()
					<-limiter
				}()

				size, success := ds.downloadTsFile(ts, cacheDir, key, retry, referer, cookie)

				if success {
					countMu.Lock()
					downloadCount++
					totalBytes += size

					now := time.Now()
					// 每 500ms 更新一次进度和速度
					if now.Sub(lastUpdateTime) >= 500*time.Millisecond || downloadCount == tsLen {
						duration := now.Sub(startTime).Seconds()
						speedStr := "0 KB/s"
						if duration > 0 {
							speed := float64(totalBytes) / duration
							if speed > 1024*1024 {
								speedStr = fmt.Sprintf("%.2f MB/s", speed/1024/1024)
							} else {
								speedStr = fmt.Sprintf("%.2f KB/s", speed/1024)
							}
						}

						progress := float64(downloadCount) / float64(tsLen) * 100
						if progress > 100 {
							progress = 100
						}
						ds.sendProgress(taskID, progress, speedStr, downloadCount, tsLen)
						lastUpdateTime = now
					}
					countMu.Unlock()
				}
			}(ts)
		}
		wg.Wait()

		missingTs = getMissing()
		downloadCount = tsLen - len(missingTs)
	}

	if len(missingTs) > 0 {
		ds.sendLog(taskID, "error", fmt.Sprintf("下载失败，仍有 %d 个分片无法下载", len(missingTs)))
		ds.sendStatus(taskID, model.StatusFailed, fmt.Sprintf("分片丢失: %d 个", len(missingTs)))
		return false
	}

	return true
}

func (ds *DownloaderService) checkTsDownDir(taskID string, dir string, tsList []TsInfo, forceMerge bool) bool {
	if forceMerge {
		ds.sendLog(taskID, "warn", "强制合并模式：跳过完整性检查")
		return true
	}

	ds.sendLog(taskID, "info", "正在进行合并前的文件完整性检查...")
	var missingCount int
	for _, ts := range tsList {
		info, err := os.Stat(filepath.Join(dir, ts.Name))
		if err != nil || info.Size() == 0 {
			missingCount++
		}
	}

	if missingCount > 0 {
		ds.sendLog(taskID, "error", fmt.Sprintf("完整性检查失败：缺失或空分片 %d 个", missingCount))
		return false
	}

	ds.sendLog(taskID, "info", "文件完整性检查通过，开始合并...")
	return true
}

func (ds *DownloaderService) mergeTs(taskID string, cacheDir, downloadDir, outputName string, ctrl *taskControl) string {
	mvName := filepath.Join(downloadDir, outputName+".mp4")

	// 收集 TS 分片（ffmpeg 与 gomedia 两条路径共用）
	tsFiles, err := collectTsFiles(cacheDir)
	if err != nil {
		log.Printf("[Error] 读取分片目录失败: %v\n", err)
		return ""
	}
	totalFiles := len(tsFiles)
	if totalFiles == 0 {
		ds.sendStatus(taskID, model.StatusFailed, "未找到可合并的 TS 分片文件")
		return ""
	}

	// 获取原始总片段数以保持进度条显示的一致性
	displayTotal := totalFiles
	ds.taskManager.mu.RLock()
	if t, exists := ds.taskManager.tasks[taskID]; exists && t.TotalSegments > 0 {
		displayTotal = t.TotalSegments
	}
	ds.taskManager.mu.RUnlock()

	// 解析合并方式与编码模式
	method := ds.getMergeMethod()
	mode := ds.getMuxMode()

	// 检测 FFmpeg 环境
	ffmpegAvailable := false
	var ffmpegPath string
	if ds.ffmpegService != nil {
		ffmpegStatus := ds.ffmpegService.Status(false)
		ffmpegAvailable = ffmpegStatus.Available
		ffmpegPath = ffmpegStatus.Path
	}

	// 仅 Go：不使用 FFmpeg
	if method == model.MergeMethodGomedia {
		ds.sendStatus(taskID, model.StatusMerging, "正在转码封装 MP4...")
		return ds.mergeWithGomedia(taskID, tsFiles, mvName, displayTotal, ctrl)
	}

	// 仅 FFmpeg：要求环境可用，失败不回退 Go
	if method == model.MergeMethodFFmpeg {
		if !ffmpegAvailable {
			ds.sendStatus(taskID, model.StatusFailed,
				"合并方式为仅 FFmpeg 但未检测到可用环境，请安装 FFmpeg 或改为自动/仅 Go")
			return ""
		}

		success, stopped := ds.mergeWithFFmpeg(taskID, ffmpegPath, mode, tsFiles, mvName, displayTotal, ctrl)
		if stopped {
			// 用户主动停止，不回退
			return ""
		}
		if success {
			return mvName
		}

		ds.sendStatus(taskID, model.StatusFailed,
			"FFmpeg 合并失败（按设置不回退 Go 封装），请通过重试功能处理")
		return ""
	}

	// 自动（默认）：FFmpeg 可用则优先使用，失败回退 gomedia
	if ffmpegAvailable {
		success, stopped := ds.mergeWithFFmpeg(taskID, ffmpegPath, mode, tsFiles, mvName, displayTotal, ctrl)
		if stopped {
			// 用户主动停止，不回退
			return ""
		}
		if success {
			return mvName
		}
		ds.sendLog(taskID, "warn", "FFmpeg 合并失败，回退内置 gomedia 封装")
		os.Remove(mvName) // 清理残留文件后再走 gomedia
	}

	ds.sendStatus(taskID, model.StatusMerging, "正在转码封装 MP4...")
	return ds.mergeWithGomedia(taskID, tsFiles, mvName, displayTotal, ctrl)
}

// collectTsFiles 收集 cacheDir 中按文件名排序的 TS 分片路径
func collectTsFiles(cacheDir string) ([]string, error) {
	files, err := os.ReadDir(cacheDir)
	if err != nil {
		return nil, err
	}

	var tsFiles []string
	for _, f := range files {
		if !f.IsDir() && filepath.Ext(f.Name()) == ".ts" {
			tsFiles = append(tsFiles, filepath.Join(cacheDir, f.Name()))
		}
	}
	sort.Strings(tsFiles)
	return tsFiles, nil
}

// concatenateTSFiles 将 TS 分片按顺序二进制拼接为单个连续流文件。
// TS 是天然可连接的容器格式；拼成单流后 SPS/PPS 只需在流中出现一次，
// 解码器即可缓存参数集用于后续所有 P 帧（concat demuxer 逐文件打开会丢失参数集）。
func concatenateTSFiles(tsFiles []string, rawPath string) (err error) {
	out, err := os.Create(rawPath)
	if err != nil {
		return fmt.Errorf("创建预拼接文件失败: %w", err)
	}
	defer func() {
		if closeErr := out.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("关闭预拼接文件失败: %w", closeErr)
		}
	}()

	buf := make([]byte, 1024*1024) // 1MB 复用缓冲
	for _, file := range tsFiles {
		in, openErr := os.Open(file)
		if openErr != nil {
			return fmt.Errorf("打开分片 %s 失败: %w", filepath.Base(file), openErr)
		}

		_, copyErr := io.CopyBuffer(out, in, buf)
		in.Close()
		if copyErr != nil {
			return fmt.Errorf("拼接分片 %s 失败: %w", filepath.Base(file), copyErr)
		}
	}
	return nil
}

// compressIfHighBitrate 合并完成后检查成片码率，超过阈值则用 FFmpeg 压缩。
// 返回：outPath 最终产物路径（未压缩时为原路径）；ok 流程是否正常；
// stopped 是否因用户停止而退出。
func (ds *DownloaderService) compressIfHighBitrate(taskID, inputPath string,
	ctrl *taskControl) (outPath string, ok, stopped bool) {

	if !ds.compressAfterMerge.Load() {
		return inputPath, true, false
	}

	threshold := int(ds.compressThresholdKbps.Load())
	if threshold < 1 {
		threshold = model.DefaultCompressBitrateThreshold
	}

	if ds.ffmpegService == nil {
		ds.sendLog(taskID, "warn", "已启用码率压缩但 FFmpeg 服务不可用，跳过压缩")
		return inputPath, true, false
	}
	st := ds.ffmpegService.Status(false)
	if !st.Available {
		ds.sendLog(taskID, "warn", "未检测到可用 FFmpeg，跳过码率压缩")
		return inputPath, true, false
	}
	ffmpegPath := st.Path

	curKbps, probeOK := probeBitrateKbps(ffmpegPath, inputPath)
	if !probeOK {
		ds.sendLog(taskID, "warn", "无法解析成片码率，跳过压缩")
		return inputPath, true, false
	}
	ds.sendLog(taskID, "info", fmt.Sprintf("成片总码率约 %dkbps（阈值 %dkbps）", curKbps, threshold))
	if curKbps <= threshold {
		return inputPath, true, false
	}

	// 需要压缩：进入压缩队列（CPU 密集，默认串行，避免多个转码并发抢占 CPU）
	ds.sendStatus(taskID, model.StatusCompressing, "正在等待压缩队列...")
	compressSem := ds.compressSem.Load()
	if !compressSem.acquire(ctrl.stopped) {
		return inputPath, false, true
	}
	compressSlotReleased := false
	defer func() {
		if !compressSlotReleased {
			compressSem.release()
		}
	}()

	// 目标视频码率：显式配置优先，否则取阈值
	target := int(ds.compressTargetKbps.Load())
	if target < 1 {
		target = threshold
	}
	if target > threshold {
		target = threshold
	}

	// 记录原文件大小，用于压缩前后体积对比日志
	origSize := int64(-1)
	if origFi, err := os.Stat(inputPath); err == nil {
		origSize = origFi.Size()
	}

	ds.sendStatus(taskID, model.StatusCompressing,
		fmt.Sprintf("码率超阈值，正在压缩至约 %dkbps...", target))
	sizePart := ""
	if origSize >= 0 {
		sizePart = fmt.Sprintf("，原文件大小 %s", humanBytes(origSize))
	}
	ds.sendLog(taskID, "info", fmt.Sprintf("开始压缩：%dkbps -> 目标 %dkbps%s", curKbps, target, sizePart))

	tmpPath := strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + ".compress.mp4"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if ctrl != nil {
		go func() {
			select {
			case <-ctrl.stopped:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	// 视频按目标码率重编码（保留兼容性），音频统一 128k AAC；faststart
	args := []string{
		"-y", "-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", inputPath,
		"-c:v", "libx264", "-preset", "medium",
		"-b:v", fmt.Sprintf("%dk", target),
		"-maxrate", fmt.Sprintf("%dk", int(float64(target)*1.2)),
		"-bufsize", fmt.Sprintf("%dk", target*2),
		"-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "+faststart",
		tmpPath,
	}

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmpPath)
		if ctx.Err() != nil {
			return "", false, true
		}
		if ctrl != nil {
			select {
			case <-ctrl.stopped:
				return "", false, true
			default:
			}
		}
		ds.sendLog(taskID, "error", fmt.Sprintf("压缩失败: %v; %s", err, strings.TrimSpace(stderr.String())))
		return inputPath, false, false
	}

	// 校验压缩产物：存在、非空、码率确实下降，否则放弃替换保留原片
	fi, statErr := os.Stat(tmpPath)
	newKbps, newProbeOK := probeBitrateKbps(ffmpegPath, tmpPath)
	if statErr != nil || fi.Size() == 0 || !newProbeOK || newKbps >= curKbps {
		ds.sendLog(taskID, "warn", "压缩产物无效或码率未下降，保留原文件")
		_ = os.Remove(tmpPath)
		return inputPath, true, false
	}

	// 原子替换：先以临时名覆盖到目标旁，再 rename，避免半成品暴露
	if err := os.Rename(tmpPath, inputPath); err != nil {
		ds.sendLog(taskID, "error", fmt.Sprintf("替换原文件失败: %v", err))
		_ = os.Remove(tmpPath)
		return inputPath, false, false
	}

	completeMsg := fmt.Sprintf("压缩完成：码率 %dkbps -> %dkbps，文件大小 %s -> %s",
		curKbps, newKbps, humanBytes(origSize), humanBytes(fi.Size()))
	if origSize > 0 {
		completeMsg += fmt.Sprintf("（减小约 %.0f%%）",
			(1-float64(fi.Size())/float64(origSize))*100)
	}
	ds.sendLog(taskID, "info", completeMsg)
	return inputPath, true, false
}

// mergeWithFFmpeg 先将 TS 分片预拼接为单个连续流，再用 FFmpeg 处理
// mode: copy(源流式复制) / h264 / h265(转码)
// 返回：success 是否成功；stopped 是否因用户停止而退出（停止时调用方不回退）
func (ds *DownloaderService) mergeWithFFmpeg(taskID, ffmpegPath, mode string, tsFiles []string,
	mvName string, displayTotal int, ctrl *taskControl) (success, stopped bool) {

	mode = model.NormalizeMuxMode(mode)
	speedLabel := muxModeSpeedLabel(mode)
	ds.sendStatus(taskID, model.StatusMerging,
		fmt.Sprintf("正在使用 FFmpeg 封装 MP4（%s）...", speedLabel))

	// 1. 预拼接：TS 顺序拼成单个连续流，使 SPS/PPS 只出现一次即可被解码器缓存，
	//    避免 concat demuxer 逐文件打开分片时，后续分片缺失参数集而无法解码
	rawPath := strings.TrimSuffix(mvName, filepath.Ext(mvName)) + ".raw.ts"
	if err := concatenateTSFiles(tsFiles, rawPath); err != nil {
		ds.sendLog(taskID, "error", fmt.Sprintf("预拼接 TS 失败: %v", err))
		return false, false
	}
	defer os.Remove(rawPath)

	// 2. context：用户停止时终止 ffmpeg 进程
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if ctrl != nil {
		go func() {
			select {
			case <-ctrl.stopped:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	// 健康看门狗：持续相同异常且无进展 60 秒自动终止
	wd := newFFmpegWatchdog(cancel, ffmpegStallLimit, ffmpegAggWindow)

	// 3. 用 ffprobe 预先获取总时长，用于解析 ffmpeg 的真实转码进度
	totalSec, durationOK := probeMediaDuration(ffmpegPath, rawPath)
	if durationOK {
		ds.sendLog(taskID, "info", fmt.Sprintf("源总时长 %.1f 秒，启用真实进度", totalSec))
	}

	// 4. 构造命令：单文件输入 + 按模式选择编码参数 + faststart；
	//    -progress pipe:1 将 key=value 进度写入 stdout
	args := []string{
		"-y", "-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", rawPath,
	}
	args = append(args, ffmpegMuxCodecArgs(mode)...)
	args = append(args, "-movflags", "+faststart",
		"-progress", "pipe:1", "-nostats", mvName)

	// 记录实际命令，失败后可直接复制复现排查
	ds.logFFmpegCommand(taskID, ffmpegPath, args)

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		ds.sendLog(taskID, "error", fmt.Sprintf("创建进度管道失败: %v", err))
		return false, false
	}

	// stderr：按行实时推送，2 秒窗口聚合折叠重复消息，并保留完整历史用于失败定位
	stderrSink := newFFmpegStderrSink(taskID, ds, wd, ffmpegAggWindow)
	defer stderrSink.close()
	cmd.Stderr = stderrSink

	// 5. 进度反馈：时长已知则解析真实进度，否则退化为粗粒度计时
	ds.sendProgress(taskID, 2, speedLabel, 0, displayTotal)
	progressDone := make(chan struct{})
	if durationOK {
		go runRealProgress(stdout, totalSec, displayTotal, speedLabel, ds, taskID, wd)
	} else {
		go runCoarseProgress(displayTotal, speedLabel, ds, taskID, progressDone)
	}

	if err := cmd.Start(); err != nil {
		close(progressDone)
		ds.sendLog(taskID, "error", fmt.Sprintf("启动 FFmpeg 失败: %v", err))
		return false, false
	}

	waitErr := cmd.Wait()
	close(progressDone)
	stderrSink.close() // flush 聚合残余

	if waitErr != nil {
		if isControlStopped(ctrl) {
			os.Remove(mvName)
			ds.sendLog(taskID, "warn", "用户终止了 FFmpeg 封装，已清理临时文件")
			return false, true
		}
		os.Remove(mvName)
		if wd.isTripped() {
			ds.sendLog(taskID, "error", fmt.Sprintf(
				"检测到 FFmpeg 持续输出相同异常消息且 %.0f 秒无处理进展，已自动终止",
				ffmpegStallLimit.Seconds()))
			return false, false
		}
		ds.sendLog(taskID, "warn", fmt.Sprintf("FFmpeg 执行失败: %v | %s",
			waitErr, tailString(stderrSink.history(), 500)))
		return false, false
	}

	// 6. 校验输出文件真实存在且非空
	info, statErr := os.Stat(mvName)
	if statErr != nil || info.Size() == 0 {
		os.Remove(mvName)
		ds.sendLog(taskID, "warn", "FFmpeg 输出文件异常（不存在或为空）")
		return false, false
	}

	ds.sendProgress(taskID, 100, "合并完成", displayTotal, displayTotal)
	return true, false
}

// ffprobeBinaryName 当前平台 ffprobe 文件名
func ffprobeBinaryName() string {
	if runtime.GOOS == "windows" {
		return "ffprobe.exe"
	}
	return "ffprobe"
}

// probeMediaDuration 通过 ffprobe 获取媒体文件的总时长（秒）
func probeMediaDuration(ffmpegPath, inputPath string) (float64, bool) {
	// 优先使用 ffmpeg 同目录下的 ffprobe，缺失时回退 PATH 查找
	ffprobePath := filepath.Join(filepath.Dir(ffmpegPath), ffprobeBinaryName())
	if _, err := os.Stat(ffprobePath); err != nil {
		path, lookupErr := exec.LookPath("ffprobe")
		if lookupErr != nil {
			return 0, false
		}
		ffprobePath = path
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, ffprobePath,
		"-v", "error", "-i", inputPath,
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1").Output()
	if err != nil {
		return 0, false
	}

	duration, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || duration <= 0 {
		return 0, false
	}
	return duration, true
}

// probeBitrateKbps 用 ffprobe 读取成片的总码率（format.bit_rate），返回 kbps；
// ffprobe 不可用或解析失败时 ok=false
func probeBitrateKbps(ffmpegPath, inputPath string) (int, bool) {
	ffprobePath := filepath.Join(filepath.Dir(ffmpegPath), ffprobeBinaryName())
	if _, err := os.Stat(ffprobePath); err != nil {
		path, lookupErr := exec.LookPath("ffprobe")
		if lookupErr != nil {
			return 0, false
		}
		ffprobePath = path
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, ffprobePath,
		"-v", "error", "-i", inputPath,
		"-show_entries", "format=bit_rate",
		"-of", "default=noprint_wrappers=1:nokey=1").Output()
	if err != nil {
		return 0, false
	}

	bps, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || bps <= 0 {
		return 0, false
	}
	return int((bps + 999) / 1000), true
}

// ffmpegMuxCodecArgs 按封装模式构建 ffmpeg 编码参数
func ffmpegMuxCodecArgs(mode string) []string {
	switch mode {
	case model.MuxModeH264:
		// libx264 转码：固定 CRF 23 与 yuv420p，保证最大兼容性；音频统一转 AAC
		return []string{
			"-c:v", "libx264", "-preset", "medium", "-crf", "23",
			"-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "128k",
		}
	case model.MuxModeH265:
		// libx265 转码：CRF 28；-tag:v hvc1 保证 Apple 生态可识别
		return []string{
			"-c:v", "libx265", "-preset", "medium", "-crf", "28",
			"-pix_fmt", "yuv420p", "-tag:v", "hvc1",
			"-c:a", "aac", "-b:a", "128k",
		}
	default:
		// 源流式复制：不重新编码，速度最快、零画质损失
		return []string{"-c", "copy"}
	}
}

// muxModeSpeedLabel 返回进度文案中的模式名称
func muxModeSpeedLabel(mode string) string {
	switch mode {
	case model.MuxModeH264:
		return "转码 H.264"
	case model.MuxModeH265:
		return "转码 H.265"
	default:
		return "源流式复制"
	}
}

// runRealProgress 解析 ffmpeg -progress 输出的 out_time_us，按总时长计算真实百分比；
// 随 stdout EOF（进程退出/管道关闭）自然结束，无 goroutine 泄漏
func runRealProgress(stdout io.Reader, totalSec float64, displayTotal int, speedLabel string,
	ds *DownloaderService, taskID string, wd *ffmpegWatchdog) {

	lastSent := time.Now()
	lastLog := time.Now()

	scanProgressBlocks(stdout, func(block map[string]string) {
		rawUS, hasTime := block["out_time_us"]
		if !hasTime {
			return
		}
		us, err := strconv.ParseInt(rawUS, 10, 64)
		if err != nil || us <= 0 {
			return // 起始阶段可能为 N/A
		}

		// 喂看门狗：处理时间真实推进时解除异常循环判定
		wd.noteProgress(us)

		pct := float64(us) / 1e6 / totalSec * 100
		if pct < 0 {
			pct = 0
		}
		if pct > 99 {
			pct = 99 // 100 留给校验通过后
		}

		// 节流：最多 500ms 推送一次进度
		if time.Since(lastSent) >= 500*time.Millisecond || pct >= 99 {
			ds.sendProgress(taskID, pct, speedLabel, 0, displayTotal)
			lastSent = time.Now()
		}

		// 实时处理日志：帧/速度/码率/倍率，最多 2 秒推送一条（落盘同样在 AddLog 内节流）
		logProgressBlock(block, &lastLog, ds, taskID)
	})
}

// runCoarseProgress 无法获取总时长时的粗粒度进度（平滑增长到 90%）
func runCoarseProgress(displayTotal int, speedLabel string,
	ds *DownloaderService, taskID string, done chan struct{}) {

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	pct := 5

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			pct += 5
			if pct > 90 {
				pct = 90
			}
			ds.sendProgress(taskID, float64(pct), speedLabel, 0, displayTotal)
		}
	}
}

// logProgressBlock 将一个 ffmpeg -progress 统计块节流（2 秒）输出为 debug 处理日志
func logProgressBlock(block map[string]string, lastLog *time.Time,
	ds *DownloaderService, taskID string) {
	if time.Since(*lastLog) < 2*time.Second {
		return
	}
	ds.sendLog(taskID, "debug", fmt.Sprintf(
		"frame=%s fps=%s bitrate=%s speed=%s time=%s",
		block["frame"], block["fps"], block["bitrate"],
		block["speed"], block["out_time"]))
	*lastLog = time.Now()
}

// scanProgressBlocks 逐行解析 ffmpeg -progress 的 key=value 流，
// 每遇到 progress=continue/end 回调一个统计块；随 EOF 结束。
func scanProgressBlocks(stdout io.Reader, onBlock func(map[string]string)) {
	scanner := bufio.NewScanner(stdout)
	// ffmpeg 个别输出行可能较长（如转码统计），放大缓冲避免扫描失败
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	block := make(map[string]string)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if key == "progress" {
			// 一个统计块结束（continue/end）
			onBlock(block)
			block = make(map[string]string)
			continue
		}
		block[key] = value
	}
	// 扫描异常（如行超过 1MB）不应静默，记录以便排查进度丢失
	if err := scanner.Err(); err != nil {
		log.Printf("[FFmpeg] -progress 输出扫描异常: %v\n", err)
	}
}

// runProgressLogs 仅消费 ffmpeg -progress 输出并推送实时处理日志（不驱动进度条），
// 同时将处理时间推进喂给看门狗。随 stdout EOF 自然结束，无 goroutine 泄漏。
func runProgressLogs(stdout io.Reader, ds *DownloaderService, taskID string, wd *ffmpegWatchdog) {
	lastLog := time.Now()
	scanProgressBlocks(stdout, func(block map[string]string) {
		if rawUS := block["out_time_us"]; rawUS != "" {
			if us, err := strconv.ParseInt(rawUS, 10, 64); err == nil && us > 0 {
				wd.noteProgress(us)
			}
		}
		logProgressBlock(block, &lastLog, ds, taskID)
	})
}

const (
	// ffmpegStallLimit 持续相同异常且无处理进展多久后自动终止
	ffmpegStallLimit = 60 * time.Second
	// ffmpegAggWindow stderr 重复消息聚合折叠窗口
	ffmpegAggWindow = 2 * time.Second
	// hlsKeyFileName HLS 加密密钥固定文件名（m3u8 的 EXT-X-KEY URI 同此名）
	hlsKeyFileName = "enc.key"
)

// ffmpeg 上下文地址格式为 "[h264 @ 000001f5c2491d40]"，每次运行都不同，
// 归一化后相同消息才能被识别为同一种异常
var ffmpegAddrRegexp = regexp.MustCompile(`@ [0-9a-fA-F]{6,}`)

// normalizeFFmpegMsg 将 FFmpeg 日志归一化为稳定的消息标识：
// 去除每次运行都变化的内存地址；压缩连续空白。
func normalizeFFmpegMsg(line string) string {
	s := ffmpegAddrRegexp.ReplaceAllString(line, "@ 0xADDR")
	s = strings.Join(strings.Fields(s), " ")
	return s
}

// ffmpegWatchdog 健康看门狗：同一种异常消息持续 stallLimit 且处理时间无推进时，
// 自动 cancel FFmpeg 进程，避免错误刷屏式死循环。并发安全。
type ffmpegWatchdog struct {
	cancel     context.CancelFunc
	stallLimit time.Duration
	aggWindow  time.Duration

	mu            sync.Mutex
	key           string // 当前异常消息标识
	firstSeen     time.Time
	repeatWindows int // 连续命中该 key 的聚合窗口数
	lastProgress  time.Time
	lastOutUS     int64
	tripped       bool
}

func newFFmpegWatchdog(cancel context.CancelFunc, stallLimit, aggWindow time.Duration) *ffmpegWatchdog {
	now := time.Now()
	return &ffmpegWatchdog{
		cancel:       cancel,
		stallLimit:   stallLimit,
		aggWindow:    aggWindow,
		lastProgress: now,
	}
}

// noteProgress 处理时间（微秒）发生真实推进时，解除异常循环判定
func (w *ffmpegWatchdog) noteProgress(outUS int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if outUS != w.lastOutUS {
		w.lastOutUS = outUS
		w.lastProgress = time.Now()
		w.key = ""
		w.repeatWindows = 0
	}
}

// observe 观察一个聚合窗口的异常消息；持续重复且无进展则终止进程
func (w *ffmpegWatchdog) observe(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.tripped || key == "" {
		return
	}

	now := time.Now()
	if key != w.key {
		w.key = key
		w.firstSeen = now
		w.repeatWindows = 1
		return
	}
	w.repeatWindows++

	// 同一异常持续 stallLimit、处理时间同样 stallLimit 无推进，
	// 且窗口数达到下限（防止偶发重复误杀）→ 终止
	minWindows := int(w.stallLimit / w.aggWindow)
	if now.Sub(w.firstSeen) >= w.stallLimit &&
		now.Sub(w.lastProgress) >= w.stallLimit &&
		w.repeatWindows >= minWindows {
		w.tripped = true
		w.cancel()
	}
}

func (w *ffmpegWatchdog) isTripped() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.tripped
}

// ffmpegStderrSink FFmpeg stderr 接收器：
// 按行读取，aggWindow 窗口内相同消息折叠为一条（同时治刷屏与持久化通道拥塞），
// 保留完整历史用于失败定位。并发安全（FFmpeg 单 goroutine 写入）。
type ffmpegStderrSink struct {
	taskID    string
	ds        *DownloaderService
	wd        *ffmpegWatchdog
	aggWindow time.Duration

	// emit 聚合块输出回调（喂看门狗 + 推日志），测试时可替换
	emit func(key, msg string)

	mu      sync.Mutex
	lineBuf []byte // 跨行残余
	key     string
	text    string
	count   int
	timer   *time.Timer
	hist    strings.Builder
	closed  bool
}

func newFFmpegStderrSink(taskID string, ds *DownloaderService,
	wd *ffmpegWatchdog, aggWindow time.Duration) *ffmpegStderrSink {
	s := &ffmpegStderrSink{
		taskID:    taskID,
		ds:        ds,
		wd:        wd,
		aggWindow: aggWindow,
	}
	s.emit = func(key, msg string) {
		wd.observe(key)
		ds.sendLog(taskID, "warn", msg)
	}
	return s
}

func (s *ffmpegStderrSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lineBuf = append(s.lineBuf, p...)
	for {
		idx := bytes.IndexByte(s.lineBuf, '\n')
		if idx < 0 {
			break
		}
		line := strings.TrimSpace(string(s.lineBuf[:idx]))
		s.lineBuf = s.lineBuf[idx+1:]
		if line != "" {
			s.ingest(line)
		}
	}
	return len(p), nil
}

// ingest 聚合一行消息（调用时持锁）
func (s *ffmpegStderrSink) ingest(line string) {
	key := normalizeFFmpegMsg(line)

	if key != s.key {
		s.flushLocked()
		s.key = key
		s.text = line
		s.count = 1
	} else {
		s.count++
	}

	// 重置聚合定时器
	if s.timer == nil {
		s.timer = time.AfterFunc(s.aggWindow, s.timedFlush)
	}
}

func (s *ffmpegStderrSink) timedFlush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushLocked()
}

// flushLocked 输出当前聚合块（调用时持锁）
func (s *ffmpegStderrSink) flushLocked() {
	if s.count == 0 {
		return
	}

	msg := s.text
	if s.count > 1 {
		msg = fmt.Sprintf("%s（%.0f秒内重复%d次）", s.text, s.aggWindow.Seconds(), s.count)
	}

	s.emit(s.key, "FFmpeg: "+msg)
	s.hist.WriteString(msg)
	s.hist.WriteByte('\n')

	s.key = ""
	s.text = ""
	s.count = 0
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

// close 输出残余（含无换行结尾的最后一行）并停止定时器；幂等
func (s *ffmpegStderrSink) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true

	// 行缓冲中未以换行结尾的残余也要输出，不能丢
	if tail := strings.TrimSpace(string(s.lineBuf)); tail != "" {
		s.lineBuf = nil
		s.ingest(tail)
	}
	s.flushLocked()
}

// history 返回截至目前的完整（折叠后）日志
func (s *ffmpegStderrSink) history() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.TrimSpace(s.hist.String())
}

// logFFmpegCommand 记录实际执行的命令参数（排障入口），超长截断
func (ds *DownloaderService) logFFmpegCommand(taskID, binary string, args []string) {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, quoteCommandArg(binary))
	for _, arg := range args {
		parts = append(parts, quoteCommandArg(arg))
	}

	cmd := strings.Join(parts, " ")
	if len(cmd) > 1000 {
		cmd = cmd[:1000] + fmt.Sprintf("...(共%d字符)", len(cmd))
	}
	ds.sendLog(taskID, "info", "执行命令: "+cmd)
}

// safeArgChars 无需引号即可出现在命令行中的安全字符
const safeArgChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.\\/:=+@%"

// quoteCommandArg 生成可直接复制到终端的命令参数表示：
// 仅含安全字符（无空格、括号、& 等特殊字符）时原样返回；
// 否则用双引号包裹，参数内换行转为空格、双引号转义为 \"。
// 注意：仅用于日志展示；exec.Command 以 argv 数组传参，由系统自行转义，不可套用此函数。
func quoteCommandArg(arg string) string {
	arg = strings.ReplaceAll(arg, "\n", " ")
	if arg == "" {
		return `""`
	}

	needsQuote := false
	for _, r := range arg {
		if !strings.ContainsRune(safeArgChars, r) {
			needsQuote = true
			break
		}
	}
	if !needsQuote {
		return arg
	}

	escaped := strings.ReplaceAll(arg, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// packageAsHLS 将合并产物再次切片为 HLS（依赖 FFmpeg hls muxer，-c copy 不重编码）。
// encryptEnabled 时按 encMode 准备 AES-128 密钥并写出标准 EXT-X-KEY。
// 返回 hlsDir；失败（含用户停止）ok=false。
func (ds *DownloaderService) packageAsHLS(taskID, inputPath, downloadDir, outputName string,
	encryptEnabled bool, encMode, keyURL, packForm, nodeOrigin string,
	ctrl *taskControl) (hlsDir string, ok bool) {

	if ds.ffmpegService == nil {
		ds.sendStatus(taskID, model.StatusFailed, "二次 HLS 分片依赖 FFmpeg，请先安装")
		return "", false
	}
	ffmpegStatus := ds.ffmpegService.Status(false)
	if !ffmpegStatus.Available {
		ds.sendStatus(taskID, model.StatusFailed, "二次 HLS 分片需要 FFmpeg 环境，请先检测或私有安装")
		return "", false
	}

	// 进入分片队列（CPU/IO 密集，默认串行，避免与其他转码/分片并发抢占资源与磁盘空间）
	ds.sendStatus(taskID, model.StatusPacking, "正在等待分片队列...")
	packSem := ds.packSem.Load()
	if !packSem.acquire(ctrl.stopped) {
		return "", false
	}
	defer packSem.release()

	hlsDir = filepath.Join(downloadDir, outputName+"_hls")
	// 重试场景：清理旧产物后重建
	if err := os.RemoveAll(hlsDir); err != nil {
		log.Printf("[Downloader] 清理旧 HLS 目录失败: %v\n", err)
	}
	if err := os.MkdirAll(hlsDir, 0o755); err != nil {
		ds.sendStatus(taskID, model.StatusFailed, fmt.Sprintf("创建 HLS 输出目录失败: %v", err))
		return "", false
	}

	stageMsg := "正在二次分片 HLS..."
	if encryptEnabled {
		stageMsg = "正在加密分片 HLS（AES-128）..."
	}
	ds.sendStatus(taskID, model.StatusPacking, stageMsg)
	ds.sendLog(taskID, "info", stageMsg)

	// 准备密钥并生成 ffmpeg hls_key_info_file（两行：m3u8 中的密钥URI / 本地密钥路径）。
	// 密钥实体独立存于 keys/{taskID}.key（以 ID 命名 + index.json 索引），hlsDir 内不再保留密钥文件。
	keyInfoPath := ""
	taskKeyPath := ""
	if encryptEnabled {
		keyPath, err := prepareHLSKey(taskID, encMode, keyURL)
		if err != nil {
			os.RemoveAll(hlsDir)
			ds.sendStatus(taskID, model.StatusFailed, fmt.Sprintf("准备加密密钥失败: %v", err))
			return "", false
		}
		taskKeyPath = keyPath
		keyInfoPath = filepath.Join(hlsDir, "keyinfo.txt")
		// 随机 IV：作为 hls_key_info_file 第三行，FFmpeg 据此 IV 加密并在 EXT-X-KEY 输出，
		// 避免默认使用媒体序列号（第 0 片为全零）导致的可预测 IV。
		ivHex, err := randomHLSIV()
		if err != nil {
			os.RemoveAll(hlsDir)
			ds.sendStatus(taskID, model.StatusFailed, err.Error())
			return "", false
		}
		content := fmt.Sprintf("%s\n%s\n%s\n", hlsKeyFileName, filepath.ToSlash(keyPath), ivHex)
		if err := os.WriteFile(keyInfoPath, []byte(content), 0o644); err != nil {
			os.RemoveAll(hlsDir)
			ds.sendStatus(taskID, model.StatusFailed, fmt.Sprintf("写入密钥信息失败: %v", err))
			return "", false
		}
		ds.sendLog(taskID, "info", fmt.Sprintf("已生成随机加密 IV: 0x%s", ivHex))
	}

	indexPath := filepath.Join(hlsDir, "index.m3u8")
	segPattern := filepath.Join(hlsDir, "seg_%05d.ts")

	args := []string{
		"-y", "-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", inputPath,
		"-c", "copy",
		"-f", "hls",
		"-hls_time", "10", // 每片约 10 秒
		"-hls_list_size", "0", // 保留全部分片（非直播滑窗）
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", segPattern,
	}
	if encryptEnabled {
		args = append(args, "-hls_key_info_file", keyInfoPath)
	}
	// -progress pipe:1 将实时处理统计写入 stdout（不影响产物）
	args = append(args, "-progress", "pipe:1", "-nostats", indexPath)

	// context：用户停止时终止进程
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if ctrl != nil {
		go func() {
			select {
			case <-ctrl.stopped:
				cancel()
			case <-ctx.Done():
			}
		}()
	}

	// 健康看门狗：持续相同异常且无进展 60 秒自动终止
	wd := newFFmpegWatchdog(cancel, ffmpegStallLimit, ffmpegAggWindow)

	// 记录实际命令，失败后可直接复制复现排查
	ds.logFFmpegCommand(taskID, ffmpegStatus.Path, args)

	cmd := exec.CommandContext(ctx, ffmpegStatus.Path, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		os.RemoveAll(hlsDir)
		ds.sendStatus(taskID, model.StatusFailed, fmt.Sprintf("创建进度管道失败: %v", err))
		return "", false
	}

	// stderr：按行实时推送，2 秒窗口聚合折叠，保留完整历史用于失败定位
	stderrSink := newFFmpegStderrSink(taskID, ds, wd, ffmpegAggWindow)
	defer stderrSink.close()
	cmd.Stderr = stderrSink

	// 粗粒度进度（无法按分片数预估，-c copy 速度较快）；
	// stdout 并行输出实时处理日志
	label := "二次HLS分片"
	if encryptEnabled {
		label = "HLS加密分片"
	}
	ds.sendProgress(taskID, 2, label, 0, 0)
	progressDone := make(chan struct{})
	go runCoarseProgress(0, label, ds, taskID, progressDone)
	go runProgressLogs(stdout, ds, taskID, wd)

	if err := cmd.Start(); err != nil {
		close(progressDone)
		os.RemoveAll(hlsDir)
		ds.sendStatus(taskID, model.StatusFailed, fmt.Sprintf("启动 FFmpeg 失败: %v", err))
		return "", false
	}

	waitErr := cmd.Wait()
	close(progressDone)
	stderrSink.close() // flush 聚合残余

	if waitErr != nil {
		if isControlStopped(ctrl) {
			os.RemoveAll(hlsDir)
			ds.sendLog(taskID, "warn", "用户终止了二次 HLS 分片，已清理")
			return "", false
		}
		os.RemoveAll(hlsDir)
		if wd.isTripped() {
			ds.sendStatus(taskID, model.StatusFailed, fmt.Sprintf(
				"检测到 FFmpeg 持续输出相同异常消息且 %.0f 秒无处理进展，已自动终止",
				ffmpegStallLimit.Seconds()))
			return "", false
		}
		ds.sendStatus(taskID, model.StatusFailed, fmt.Sprintf("二次 HLS 分片失败: %v | %s",
			waitErr, tailString(stderrSink.history(), 500)))
		return "", false
	}

	// 校验 m3u8 与至少一个分片
	if _, err := os.Stat(indexPath); err != nil {
		os.RemoveAll(hlsDir)
		ds.sendStatus(taskID, model.StatusFailed, "HLS 输出缺少 m3u8 清单")
		return "", false
	}
	segFiles, _ := collectByExt(hlsDir, ".ts")
	if len(segFiles) == 0 {
		os.RemoveAll(hlsDir)
		ds.sendStatus(taskID, model.StatusFailed, "HLS 输出缺少 TS 分片")
		return "", false
	}

	// single 可选形态：收敛为 tsbin + BYTERANGE m3u8 + meta.json；
	// multi（默认）形态不进入此分支，产物与上传逻辑完全保持原样
	if packForm == model.HLSPackFormSingle {
		if err := collapseToSingleFile(hlsDir, segFiles, indexPath, outputName,
			taskID, encryptEnabled, nodeOrigin); err != nil {
			os.RemoveAll(hlsDir)
			ds.sendStatus(taskID, model.StatusFailed, fmt.Sprintf("收敛单文件失败: %v", err))
			return "", false
		}
		ds.sendLog(taskID, "info", "已收敛为单文件 tsbin（BYTERANGE 索引 + meta.json）")
	}

	// keyinfo 仅含本地路径，播放器不需要，移除
	os.Remove(keyInfoPath)

	// 密钥路径（keys/{taskID}.key）挂到任务并持久化（任务列表的下载按钮据此提供）
	if encryptEnabled && taskKeyPath != "" {
		var taskForPersist *model.Task
		ds.taskManager.mu.Lock()
		if t, exists := ds.taskManager.tasks[taskID]; exists {
			t.KeyPath = taskKeyPath
			taskForPersist = t
		}
		ds.taskManager.mu.Unlock()
		if taskForPersist != nil {
			ds.taskManager.UpdateTask(taskForPersist)
		}
	}

	ds.sendLog(taskID, "info", fmt.Sprintf("二次 HLS 分片完成（%d 片）", len(segFiles)))
	return hlsDir, true
}

// hlsMeta 单文件产物元信息（与 tsbin、m3u8 同目录）
type hlsMeta struct {
	Version   int    `json:"version"`
	TaskID    string `json:"taskId"`
	NodeURL   string `json:"nodeUrl"`
	Encrypted bool   `json:"encrypted"`
	CreatedAt string `json:"createdAt"`
}

// collapseToSingleFile 将多分片 HLS 目录收敛为单文件形态：
//  1. 各分片（加密时即各分片密文）顺序拼接为 {outputName}.tsbin
//  2. index.m3u8 重写为 EXT-X-BYTERANGE 索引，统一指向 tsbin
//  3. 删除 seg 分片文件
//  4. 写 meta.json（taskId/节点地址/是否加密）
//
// 密钥不在 hlsDir 内（独立存于 keys/{taskID}.key），本函数不涉及；m3u8 中 EXT-X-KEY
// 的 URI 仍为 enc.key，由播放器侧改写后经鉴权接口取密钥。
func collapseToSingleFile(hlsDir string, segFiles []string, indexPath, outputName,
	taskID string, encrypted bool, nodeURL string) error {

	// 分片实际大小（顺序即播放顺序，segFiles 已排序）
	sizes := make([]int64, len(segFiles))
	for i, f := range segFiles {
		info, err := os.Stat(f)
		if err != nil {
			return fmt.Errorf("读取分片 %s 信息失败: %w", filepath.Base(f), err)
		}
		sizes[i] = info.Size()
	}

	binName := outputName + ".tsbin"
	binPath := filepath.Join(hlsDir, binName)

	// 1. 二进制拼接（TS 密文/明文均可连接；range 边界恰好是分片边界）
	if err := concatenateTSFiles(segFiles, binPath); err != nil {
		return err
	}

	// 2. 重写 m3u8 为 BYTERANGE
	orig, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("读取原始 m3u8 失败: %w", err)
	}
	playlist, err := buildByteRangePlaylist(orig, sizes, binName)
	if err != nil {
		return err
	}
	if err := os.WriteFile(indexPath, playlist, 0o644); err != nil {
		return fmt.Errorf("写入 BYTERANGE m3u8 失败: %w", err)
	}

	// 3. 删除分片（hlsDir 内只剩 tsbin/index.m3u8/meta.json，密钥独立于 keys/）
	for _, f := range segFiles {
		if err := os.Remove(f); err != nil {
			return fmt.Errorf("删除已收敛分片失败: %w", err)
		}
	}

	// 4. meta.json
	meta := hlsMeta{
		Version:   1,
		TaskID:    taskID,
		NodeURL:   strings.TrimSpace(nodeURL),
		Encrypted: encrypted,
		CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
	}
	metaBytes, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("构造 meta.json 失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(hlsDir, "meta.json"), metaBytes, 0o644); err != nil {
		return fmt.Errorf("写入 meta.json 失败: %w", err)
	}

	return nil
}

// buildByteRangePlaylist 将标准分片 m3u8 重写为 BYTERANGE 形态（RFC 8216）：
//   - 版本强制 ≥4（BYTERANGE 要求）
//   - 头部标签原样保留（TARGETDURATION/MEDIA-SEQUENCE/INDEPENDENT-SEGMENTS/EXT-X-KEY 等）
//   - 每个分片 URI 替换为「#EXT-X-BYTERANGE:<长度>@<偏移>」+ 统一 binName
//   - ENDLIST 等非 URI 行保留
//
// 清单分片数必须与 sizes 数量一致，否则返回错误（防止 range 错位）。
func buildByteRangePlaylist(orig []byte, sizes []int64, binName string) ([]byte, error) {
	lines := strings.Split(strings.TrimSpace(string(orig)), "\n")

	var out strings.Builder
	out.WriteString("#EXTM3U\n")

	segIdx := 0
	var offset int64
	pendingExtInf := false

	for _, raw := range lines {
		line := strings.TrimSpace(raw)

		switch {
		case line == "#EXTM3U":
			// 已写
		case strings.HasPrefix(line, "#EXT-X-VERSION"):
			out.WriteString("#EXT-X-VERSION:4\n")
		case strings.HasPrefix(line, "#EXTINF"):
			out.WriteString(line)
			out.WriteByte('\n')
			pendingExtInf = true
		case strings.HasPrefix(line, "#") || line == "":
			// 其它标签（含 EXT-X-KEY/ENDLIST）原样保留
			if line != "" {
				out.WriteString(line)
				out.WriteByte('\n')
			}
		default:
			// 分片 URI 行：必须由 EXTINF 引出，且数量不超 sizes
			if !pendingExtInf {
				return nil, fmt.Errorf("m3u8 中存在未由 EXTINF 引出的 URI 行: %s", line)
			}
			if segIdx >= len(sizes) {
				return nil, fmt.Errorf("m3u8 分片数多于实际分片（%d）", len(sizes))
			}

			out.WriteString(fmt.Sprintf("#EXT-X-BYTERANGE:%d@%d\n", sizes[segIdx], offset))
			out.WriteString(binName)
			out.WriteByte('\n')

			offset += sizes[segIdx]
			segIdx++
			pendingExtInf = false
		}
	}

	if segIdx != len(sizes) {
		return nil, fmt.Errorf("m3u8 分片数 %d 与实际分片 %d 不一致", segIdx, len(sizes))
	}

	return []byte(out.String()), nil
}

// prepareHLSKey 按密钥来源准备任务密钥，统一存入独立密钥库 keys/{taskID}.key：
// specified → 从 URL 下载（复用 hlskeys 缓存，同 URL 不重复请求）；
// generated → crypto/rand 生成 16 字节随机密钥。
// 返回密钥文件绝对路径（供 ffmpeg hls_key_info_file 读取、任务 KeyPath 持久化），
// 密钥不再落于 download_* 缓存目录，定时清理不会删除。
func prepareHLSKey(taskID, mode, keyURL string) (string, error) {
	var (
		data     []byte
		fileName = hlsKeyFileName
	)

	if mode == model.HLSEncryptSpecified {
		cached, err := resolveSpecifiedKey(keyURL)
		if err != nil {
			return "", err
		}
		data, err = os.ReadFile(cached)
		if err != nil {
			return "", fmt.Errorf("读取缓存密钥失败: %w", err)
		}
		if len(data) != 16 {
			return "", fmt.Errorf("缓存密钥必须为 16 字节，实际 %d 字节", len(data))
		}
		// 记录用户 URL 中的原始文件名（无法提取则 enc.key）
		fileName = originalKeyFileName(keyURL)
	} else {
		data = make([]byte, 16)
		if _, err := rand.Read(data); err != nil {
			return "", fmt.Errorf("生成随机密钥失败: %w", err)
		}
	}

	return defaultKeyStore.put(taskID, data, fileName)
}

// randomHLSIV 用 crypto/rand 生成 16 字节随机 IV，返回小写十六进制字符串（32 个字符，
// 不带 0x 前缀），用于写入 FFmpeg hls_key_info_file 的第三行。
func randomHLSIV() (string, error) {
	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		return "", fmt.Errorf("生成随机 IV 失败: %w", err)
	}
	return hex.EncodeToString(iv), nil
}

// resolveSpecifiedKey 下载指定 URL 的 HLS 密钥（必须为 16 字节），
// 缓存于 {工作目录}/hlskeys/{URL哈希}-{时间戳}.key；相同 URL 已存在缓存则直接复用。
func resolveSpecifiedKey(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("指定密钥模式下密钥 URL 为空")
	}
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		return "", fmt.Errorf("密钥 URL 非法: %w", err)
	}

	pwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("获取工作目录失败: %w", err)
	}
	cacheDir := filepath.Join(pwd, "hlskeys")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("创建密钥缓存目录失败: %w", err)
	}

	sum := sha256.Sum256([]byte(rawURL))
	hash := hex.EncodeToString(sum[:])[:16]

	// 同 URL 复用：查找 {hash}-*.key，取文件名（含时间戳）最新的有效副本
	entries, err := os.ReadDir(cacheDir)
	if err == nil {
		candidates := make([]string, 0)
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), hash+"-") && strings.HasSuffix(e.Name(), ".key") {
				candidates = append(candidates, e.Name())
			}
		}
		if len(candidates) > 0 {
			sort.Strings(candidates)
			latest := filepath.Join(cacheDir, candidates[len(candidates)-1])
			if info, statErr := os.Stat(latest); statErr == nil && info.Size() == 16 {
				log.Printf("[HLSKey] 复用缓存密钥: %s\n", latest)
				return latest, nil
			}
		}
	}

	// 下载（密钥极小：响应头 30 秒，整体 120 秒）
	client := &http.Client{
		Timeout: 120 * time.Second,
		Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
	resp, err := client.Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("下载密钥失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载密钥返回状态: %s", resp.Status)
	}

	// 最多读 64 字节并严格校验为 16 字节
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", fmt.Errorf("读取密钥内容失败: %w", err)
	}
	if len(data) != 16 {
		return "", fmt.Errorf("密钥文件必须为 16 字节（HLS AES-128），实际 %d 字节", len(data))
	}

	// 带时间戳落盘：临时文件写完再 rename，避免半截文件被当作有效缓存
	dest := filepath.Join(cacheDir,
		fmt.Sprintf("%s-%s.key", hash, time.Now().Format("20060102-150405.000000")))
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fmt.Errorf("写入缓存密钥失败: %w", err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return "", fmt.Errorf("密钥落盘失败: %w", err)
	}

	log.Printf("[HLSKey] 已下载并缓存密钥: %s\n", dest)
	return dest, nil
}

// collectByExt 收集 dir 下指定扩展名（小写匹配）的常规文件完整路径并排序
func collectByExt(dir, ext string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	result := make([]string, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ext) {
			result = append(result, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(result)
	return result, nil
}

// countValidSegments 统计 cacheDir 内非空 .ts 分片数量（目录不存在返回 0）。
// 用于失败时判断是否值得保留缓存，以及合并前的有效性判定。
func countValidSegments(cacheDir string) int {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ".ts") {
			continue
		}
		if info, err := e.Info(); err == nil && info.Size() > 0 {
			count++
		}
	}
	return count
}

// uploadHLSDirectory 将本地 HLS 目录上传到 WebDAV：远程 {remoteBase}/{folderName}/。
// 分片先传，m3u8 最后传；聚合所有文件字节计算整体进度。
func (ds *DownloaderService) uploadHLSDirectory(taskID string, svc *WebDAVService,
	localDir, folderName, remoteBase string, ctrl *taskControl) error {

	base := remoteBase
	if base == "" {
		base = "/"
	}
	if !strings.HasPrefix(base, "/") {
		base = "/" + base
	}
	remoteRoot := path.Join(base, folderName)
	if err := svc.MkdirAll(remoteRoot); err != nil {
		return fmt.Errorf("创建远程 HLS 目录失败: %w", err)
	}

	files, totalSize, keySkipped, err := collectHLSUploadFiles(localDir)
	if err != nil {
		return err
	}

	// 安全策略：密钥不随 WebDAV 上传，仅允许通过任务列表鉴权下载
	if keySkipped {
		ds.sendLog(taskID, "info", fmt.Sprintf(
			"已按安全策略跳过加密密钥 %s（不随 WebDAV 上传，仅可从任务列表下载）",
			hlsKeyFileName))
	}

	var uploaded int64
	for _, f := range files {
		select {
		case <-ctrl.stopped:
			return fmt.Errorf("用户停止上传")
		default:
		}

		remoteName := path.Join(folderName, f.name)
		fileBase := uploaded
		err := svc.UploadFile(filepath.Join(localDir, f.name), remoteName, ctrl.stopped,
			func(done int64, total int64, speed string) {
				cur := fileBase + done
				pct := 0.0
				if totalSize > 0 {
					pct = float64(cur) / float64(totalSize) * 100
				}
				ds.sendProgress(taskID, pct, speed, int(cur/1024), int(totalSize/1024))
			})
		if err != nil {
			return fmt.Errorf("上传 %s 失败: %w", f.name, err)
		}
		uploaded += f.size
	}

	ds.sendProgress(taskID, 100, "上传完成", int(totalSize/1024), int(totalSize/1024))
	return nil
}

// hlsUploadFile HLS 待上传文件
type hlsUploadFile struct {
	name string
	size int64
}

// collectHLSUploadFiles 收集 HLS 目录待上传文件（m3u8 排最后）：
// 剔除 enc.key —— 密钥不允许随 WebDAV 上传，只能通过前端鉴权接口下载。
// 返回文件列表、待上传总字节、是否发现并跳过了密钥。
func collectHLSUploadFiles(localDir string) ([]hlsUploadFile, int64, bool, error) {
	entries, err := os.ReadDir(localDir)
	if err != nil {
		return nil, 0, false, fmt.Errorf("读取本地 HLS 目录失败: %w", err)
	}

	files := make([]hlsUploadFile, 0)
	var totalSize int64
	keySkipped := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// 剔除加密密钥
		if e.Name() == hlsKeyFileName {
			keySkipped = true
			continue
		}
		info, statErr := e.Info()
		if statErr != nil {
			continue
		}
		files = append(files, hlsUploadFile{e.Name(), info.Size()})
		totalSize += info.Size()
	}

	// m3u8 放最后（播放清单最后就位，避免播放端拿到不完整清单）
	sort.SliceStable(files, func(i, j int) bool {
		return !strings.HasSuffix(files[i].name, ".m3u8")
	})

	return files, totalSize, keySkipped, nil
}

// isControlStopped 非阻塞判断任务是否已被用户停止
func isControlStopped(ctrl *taskControl) bool {
	if ctrl == nil {
		return false
	}
	select {
	case <-ctrl.stopped:
		return true
	default:
		return false
	}
}

// waitIfPaused 在各阶段切换处提供暂停关卡：若任务已暂停则在此阻塞，直到被恢复或停止。
// 这样可在下载/合并/压缩/分片/上传阶段之间严格停顿，避免暂停后任务仍冲入下一阶段。
// 注意：不打断正在运行的 FFmpeg 子进程（避免浪费已耗 CPU），转码结束后到达本关卡再挂起。
// 返回 true 表示可以继续，false 表示任务已被停止（应直接退出，由 defer 负责清理与释放）。
func waitIfPaused(ctrl *taskControl) bool {
	if ctrl == nil {
		return true
	}
	select {
	case <-ctrl.stopped:
		return false
	case <-ctrl.paused:
		select {
		case <-ctrl.resumed:
			return true
		case <-ctrl.stopped:
			return false
		}
	default:
		return true
	}
}

// humanBytes 将字节数格式化为人类可读的 B/KB/MB/GB 字符串（保留两位小数）
func humanBytes(n int64) string {
	if n < 0 {
		return "-"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// tailString 返回字符串末尾最多 n 个字节，用于日志截断
func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// mergeWithGomedia 使用纯 Go gomedia 进行 TS 转 MP4 封装（ffmpeg 不可用或失败时的兜底路径）
func (ds *DownloaderService) mergeWithGomedia(taskID string, tsFiles []string,
	mvName string, displayTotal int, ctrl *taskControl) string {

	outMv, err := os.Create(mvName)
	if err != nil {
		log.Printf("[Error] 无法创建输出文件: %v\n", err)
		return ""
	}
	defer outMv.Close()

	muxer, err := mp4.CreateMp4Muxer(outMv)
	if err != nil {
		log.Printf("[Error] 无法创建 MP4 Muxer: %v\n", err)
		return mvName
	}

	vtid := uint32(0)
	atid := uint32(0)
	var firstDts uint64
	hasFirstDts := false

	demuxer := mpeg2.NewTSDemuxer()
	demuxer.OnFrame = func(cid mpeg2.TS_STREAM_TYPE, frame []byte, pts uint64, dts uint64) {
		if !hasFirstDts {
			firstDts = dts
			hasFirstDts = true
		}

		var adjPts, adjDts uint64
		if pts >= firstDts {
			adjPts = pts - firstDts
		}
		if dts >= firstDts {
			adjDts = dts - firstDts
		}

		if cid == mpeg2.TS_STREAM_H264 {
			if vtid == 0 {
				vtid = muxer.AddVideoTrack(mp4.MP4_CODEC_H264)
			}
			muxer.Write(vtid, frame, adjPts, adjDts)
		} else if cid == mpeg2.TS_STREAM_AAC {
			if atid == 0 {
				atid = muxer.AddAudioTrack(mp4.MP4_CODEC_AAC)
			}
			muxer.Write(atid, frame, adjPts, adjDts)
		} else if cid == mpeg2.TS_STREAM_H265 {
			if vtid == 0 {
				vtid = muxer.AddVideoTrack(mp4.MP4_CODEC_H265)
			}
			muxer.Write(vtid, frame, adjPts, adjDts)
		}
	}

	totalFiles := len(tsFiles)
	for i, path := range tsFiles {
		// 检查暂停 / 停止
		if ctrl != nil {
			select {
			case <-ctrl.stopped:
				muxer.WriteTrailer()
				outMv.Close()
				os.Remove(mvName) // 清理不完整的合并文件
				ds.sendLog(taskID, "warn", "用户终止了合并过程，已清理临时文件")
				return ""
			case <-ctrl.paused:
				ds.sendStatus(taskID, model.StatusPaused, "合并已暂停")
				select {
				case <-ctrl.resumed:
					ds.sendStatus(taskID, model.StatusMerging, "合并已恢复")
				case <-ctrl.stopped:
					muxer.WriteTrailer()
					outMv.Close()
					os.Remove(mvName)
					ds.sendLog(taskID, "warn", "用户终止了合并过程，已清理临时文件")
					return ""
				}
			default:
			}
		}

		// 发送合并/转码进度
		progress := float64(i+1) / float64(totalFiles) * 100
		ds.sendProgress(taskID, progress, "封装中", i+1, displayTotal)

		f, err := os.Open(path)
		if err != nil {
			continue
		}
		demuxer.Input(f)
		f.Close()
	}

	// 无任何可识别音视频流（分片损坏/非 TS/强制合并的残片）时，产物为空 MP4，
	// 不返回成功，删除无效文件并明确报错，避免"已完成但无法播放"
	if vtid == 0 && atid == 0 {
		muxer.WriteTrailer()
		outMv.Close()
		if rmErr := os.Remove(mvName); rmErr != nil {
			log.Printf("[Error] 删除无效合并产物失败: %v\n", rmErr)
		}
		ds.sendStatus(taskID, model.StatusFailed, "分片未包含可识别的音视频流（H264/H265/AAC），合并产物无效")
		return ""
	}

	muxer.WriteTrailer()
	return mvName
}

// 辅助方法：过滤文件名非法字符
func sanitizeFileName(name string) string {
	if name == "" {
		return "movie"
	}
	// 移除可能导致路径穿越或系统问题的字符
	badChars := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	for _, char := range badChars {
		name = strings.ReplaceAll(name, char, "")
	}
	return name
}

// 辅助方法：过滤 Header 非法字符（防御 CRLF 注入）
func sanitizeHeader(val string) string {
	val = strings.ReplaceAll(val, "\r", "")
	val = strings.ReplaceAll(val, "\n", "")
	return strings.TrimSpace(val)
}

// 辅助方法：标准化 Referrer 格式
func normalizeReferrer(referrer string) string {
	referrer = sanitizeHeader(referrer)
	if referrer == "" {
		return ""
	}

	if !strings.HasPrefix(referrer, "http://") && !strings.HasPrefix(referrer, "https://") {
		referrer = "https://" + referrer
	}

	if !strings.HasSuffix(referrer, "/") {
		referrer = referrer + "/"
	}

	return referrer
}

func (ds *DownloaderService) AesDecrypt(crypted, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	blockSize := block.BlockSize()
	blockMode := cipher.NewCBCDecrypter(block, key[:blockSize])
	origData := make([]byte, len(crypted))
	blockMode.CryptBlocks(origData, crypted)
	return ds.pkcs7UnPadding(origData), nil
}

func (ds *DownloaderService) pkcs7UnPadding(origData []byte) []byte {
	length := len(origData)
	if length == 0 {
		return origData
	}
	unpadding := int(origData[length-1])
	if unpadding > length {
		return origData
	}
	return origData[:length-unpadding]
}

func (ds *DownloaderService) PauseDownload(taskID string) {
	if ctrl, ok := ds.getControl(taskID); ok {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		if !ctrl.isPaused {
			ctrl.isPaused = true
			close(ctrl.paused)
			// Re-create resumed channel for next resume
			ctrl.resumed = make(chan struct{})
			ds.sendStatus(taskID, model.StatusPaused, "已暂停")
		}
	}
}

func (ds *DownloaderService) ResumeDownload(taskID string) {
	if ctrl, ok := ds.getControl(taskID); ok {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		if ctrl.isPaused {
			ctrl.isPaused = false
			close(ctrl.resumed)
			// Re-create paused channel for next pause
			ctrl.paused = make(chan struct{})
			ds.sendStatus(taskID, model.StatusDownloading, "已恢复")
		}
	}
}

func (ds *DownloaderService) RetryDownload(taskID string, mode string) error {
	task, exists := ds.taskManager.GetTask(taskID)
	if !exists {
		return fmt.Errorf("任务不存在")
	}

	if task.Status != model.StatusFailed && task.Status != model.StatusCompleted {
		return fmt.Errorf("只有失败或已完成的任务可以重试，当前状态无法重试")
	}

	// 模式校验：空串回退缺失重试；未知模式明确报错，不静默走错误路径
	retryMode := model.NormalizeRetryMode(model.RetryMode(mode))
	if mode != "" && model.RetryMode(mode) != retryMode {
		return fmt.Errorf("无效的重试模式: %s", mode)
	}

	// 上一轮 goroutine 未完全退出（控制块仍在）时拒绝重试，避免双 goroutine 并发写同一目录
	if _, alive := ds.getControl(taskID); alive {
		return fmt.Errorf("上一轮任务尚未完全停止，请稍后再试")
	}

	// 解析任务目录：优先使用持久化的 WorkDir（新下载与重试目录同一位置）；
	// 老任务无记录时回退 {SavePath}/{Name}
	taskDir := task.WorkDir
	if taskDir == "" {
		taskDir = filepath.Join(task.SavePath, task.Name)
	}
	cacheDir := filepath.Join(taskDir, "cache")

	// 如果是强制合并模式，直接执行合并
	if retryMode == model.RetryModeForceMerge {
		validCount := countValidSegments(cacheDir)
		if validCount == 0 {
			return fmt.Errorf("本地没有任何已下载分片，无法强制合并（可能缓存已被清理）")
		}

		ds.sendStatus(taskID, model.StatusMerging, fmt.Sprintf("正在强制合并 %d 个分片...", validCount))
		ds.sendLog(taskID, "info", fmt.Sprintf("用户选择强制合并，将基于现有 %d 个分片跳过缺失部分", validCount))

		ctrl := ds.createControl(taskID)
		// 强制合并为同步执行（本函数处于 HTTP 处理协程），结束后必须移除控制块，
		// 否则控制块泄漏，下次重试会被"上一轮未停止"守卫永久拦截
		defer ds.removeControl(taskID, ctrl)

		mvName := ds.mergeTs(taskID, cacheDir, taskDir, task.Name, ctrl)
		if mvName != "" {
			ds.sendStatus(taskID, model.StatusCompleted, "强制合并完成")
			ds.sendLog(taskID, "info", fmt.Sprintf("视频已保存到: %s", mvName))
			ds.taskManager.mu.Lock()
			if t, exists := ds.taskManager.tasks[taskID]; exists {
				t.Status = model.StatusCompleted
				t.OutputPath = mvName
				t.Progress = 100
				t.Error = ""
			}
			ds.taskManager.mu.Unlock()
			ds.taskManager.UpdateTask(task)
		} else {
			ds.sendStatus(taskID, model.StatusFailed, "强制合并失败")
			return fmt.Errorf("强制合并失败（分片无效或不包含可识别音视频流）")
		}
		return nil
	}

	// 准备重试请求
	if task.ThreadCount < 1 {
		task.ThreadCount = 24 // 与 HTTP 入口默认一致
	}
	req := model.DownloadRequest{
		URL:               task.URL,
		ThreadCount:       task.ThreadCount,
		OutputName:        task.Name,
		HostType:          task.HostType,
		Cookie:            task.Cookie,
		Referer:           task.Referer,
		AutoClear:         task.AutoClear,
		SavePath:          task.SavePath,
		EnableWebDAV:      task.EnableWebDAV,
		WebDAVURL:         task.WebDAVURL,
		WebDAVUsername:    task.WebDAVUsername,
		WebDAVPassword:    task.WebDAVPassword,
		WebDAVRemoteDir:   task.WebDAVRemoteDir,
		DeleteAfterUpload: task.DeleteAfterUpload,
		RetryMode:         retryMode,
	}

	// 更新任务状态并广播
	if retryMode == model.RetryModeMissing {
		ds.sendStatus(taskID, model.StatusDownloading, "正在重试下载缺失分片...")
		ds.sendLog(taskID, "info", "重试模式：保留已下载分片，仅下载缺失分片")
	} else {
		ds.sendStatus(taskID, model.StatusDownloading, "正在完全重新下载...")
		ds.sendLog(taskID, "info", "重试模式：完全重新下载所有分片")

		// 删除之前的缓存（按 WorkDir 精确定位，含 0 字节残片）
		if exists, _ := pathExists(cacheDir); exists {
			if err := os.RemoveAll(cacheDir); err != nil {
				log.Printf("[Retry] 删除旧缓存目录失败: %v\n", err)
				ds.sendLog(taskID, "warn", fmt.Sprintf("删除旧缓存目录失败: %v", err))
			} else {
				ds.sendLog(taskID, "info", "已删除之前的缓存目录")
			}
		}
	}

	ds.taskManager.mu.Lock()
	task.Progress = 0
	task.DownloadedSegments = 0
	task.Error = ""
	ds.taskManager.mu.Unlock()
	ds.taskManager.UpdateTask(task)

	// 创建新一代控制信号
	ds.createControl(taskID)

	// 启动下载
	go ds.download(taskID, req)

	return nil
}

func (ds *DownloaderService) UploadTaskToWebDAV(taskID string, config *WebDAVConfig) error {
	task, exists := ds.taskManager.GetTask(taskID)
	if !exists {
		return fmt.Errorf("任务不存在")
	}

	if task.Status != model.StatusCompleted && task.Status != model.StatusFailed {
		return fmt.Errorf("只有完成或失败的任务可以尝试上传")
	}

	if task.OutputPath == "" {
		return fmt.Errorf("找不到本地输出文件路径，可能任务未完成或文件路径丢失")
	}

	if _, err := os.Stat(task.OutputPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("本地输出文件已不存在，可能已被自动清理或手动删除: %s", task.OutputPath)
		}
		return fmt.Errorf("无法访问本地输出文件: %w", err)
	}

	// 上一轮下载/合并/上传尚未结束时拒绝，防止重复点击产生两个上传 goroutine
	// （旧 ctrl 被替换后无法停止、同一文件被重复上传）
	if _, alive := ds.getControl(taskID); alive {
		return fmt.Errorf("上一轮任务尚未完全停止，请稍后再试")
	}

	// 优先使用传入的配置，如果没有则使用任务保存的配置
	var finalConfig WebDAVConfig
	if config != nil && config.URL != "" {
		finalConfig = *config
	} else {
		if task.WebDAVURL == "" {
			return fmt.Errorf("任务未配置 WebDAV 地址，请在设置中配置或手动选择路径")
		}
		finalConfig = WebDAVConfig{
			Enabled:   true,
			URL:       task.WebDAVURL,
			Username:  task.WebDAVUsername,
			Password:  task.WebDAVPassword,
			RemoteDir: task.WebDAVRemoteDir,
		}
	}

	// 在启动 goroutine 前拷贝需要的值，避免 goroutine 中直接读共享 task 指针
	localOutputPath := task.OutputPath
	remoteName := filepath.Base(localOutputPath)
	deleteAfterUpload := task.DeleteAfterUpload

	go func() {
		ds.taskManager.mu.Lock()
		if t, exists := ds.taskManager.tasks[taskID]; exists {
			t.Error = ""
		}
		ds.taskManager.mu.Unlock()

		ds.sendStatus(taskID, model.StatusUploading, "正在上传到 WebDAV...")
		ds.sendLog(taskID, "info", fmt.Sprintf("开始上传到 WebDAV: %s (目录: %s)", finalConfig.URL, finalConfig.RemoteDir))

		ctrl := ds.createControl(taskID)
		webdavService := NewWebDAVService(finalConfig)
		err := webdavService.UploadFile(localOutputPath, remoteName, ctrl.stopped, func(downloaded, total int64, speed string) {
			progress := 0.0
			if total > 0 {
				progress = float64(downloaded) / float64(total) * 100
			}
			curKB := int(downloaded / 1024)
			totalKB := int(total / 1024)
			ds.sendProgress(taskID, progress, speed, curKB, totalKB)
		})

		ds.removeControl(taskID, ctrl)
		if err != nil {
			// 检查是否是用户主动停止
			isStopped := false
			select {
			case <-ctrl.stopped:
				isStopped = true
			default:
			}

			if isStopped {
				ds.sendLog(taskID, "warn", "WebDAV 手动上传已被用户停止")
				ds.sendStatus(taskID, model.StatusFailed, "上传已停止")
				return
			}

			ds.sendLog(taskID, "error", fmt.Sprintf("WebDAV 上传失败: %v", err))
			ds.sendStatus(taskID, model.StatusFailed, fmt.Sprintf("WebDAV 上传失败: %v", err))
			return
		}

		ds.sendLog(taskID, "info", "WebDAV 上传成功")
		ds.sendStatus(taskID, model.StatusCompleted, "WebDAV 上传完成")

		// 如果是通过手动上传且勾选了删除，清理整个下载目录
		if deleteAfterUpload {
			downloadDir := filepath.Dir(localOutputPath)
			if strings.Contains(filepath.Base(downloadDir), "download_") {
				if err := os.RemoveAll(downloadDir); err != nil {
					log.Printf("[Downloader] 删除本地下载目录失败: %v\n", err)
					ds.sendLog(taskID, "warn", fmt.Sprintf("删除本地下载目录失败: %v", err))
				} else {
					ds.sendLog(taskID, "info", "已清理本地下载目录")
				}
			} else {
				if err := os.Remove(localOutputPath); err != nil {
					log.Printf("[Downloader] 删除本地文件失败: %v\n", err)
					ds.sendLog(taskID, "warn", fmt.Sprintf("删除本地文件失败: %v", err))
				} else {
					ds.sendLog(taskID, "info", "已删除本地文件")
				}
			}
		}
	}()

	return nil
}

func (ds *DownloaderService) StopDownload(taskID string) {
	if ctrl, ok := ds.getControl(taskID); ok {
		ctrl.mu.Lock()
		defer ctrl.mu.Unlock()
		select {
		case <-ctrl.stopped:
			// already stopped
		default:
			close(ctrl.stopped)
			ds.sendStatus(taskID, model.StatusFailed, "用户终止")
		}
	}
}

func (ds *DownloaderService) AnalyzeURL(urlStr string, referer string, cookie string) (map[string]interface{}, error) {
	// 1. 基础校验：检查是否是 M3U8
	isM3U8 := strings.Contains(strings.ToLower(urlStr), ".m3u8")

	if isM3U8 {
		m3u8Body := ds.getM3u8Body(urlStr, referer, cookie)
		if m3u8Body == "" {
			return nil, fmt.Errorf("无法获取 m3u8 内容，请检查链接有效性或 Referer 设置")
		}

		host := ds.getHost(urlStr, "v1")
		tsList := ds.getTsList(host, m3u8Body)
		key := ds.getM3u8Key(host, m3u8Body, referer, cookie)

		return map[string]interface{}{
			"type":     "m3u8",
			"segments": len(tsList),
			"hasKey":   key != "",
		}, nil
	}

	// 2. 视频格式校验：检查是否是支持的视频格式
	if !ds.checkIsVideoURL(urlStr, referer, cookie) {
		return nil, fmt.Errorf("不支持的下载类型：仅支持 M3U8 及主流视频格式 (MP4, MKV, AVI 等)")
	}

	// 否则视为支持的通用视频文件
	return map[string]interface{}{
		"type": "file",
	}, nil
}

// 辅助方法：检查链接是否为视频类资源
func (ds *DownloaderService) checkIsVideoURL(urlStr, referer, cookie string) bool {
	referer = normalizeReferrer(referer)
	lowerURL := strings.ToLower(urlStr)
	videoExts := []string{".mp4", ".mkv", ".avi", ".flv", ".mov", ".wmv", ".webm", ".m4v", ".ts", ".3gp", ".rmvb"}

	// 1. 优先通过后缀名判断
	for _, ext := range videoExts {
		if strings.Contains(lowerURL, ext) {
			return true
		}
	}

	// 2. 尝试发送 HEAD 请求检查 Content-Type
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest("HEAD", urlStr, nil)
	if err == nil {
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

		resp, err := client.Do(req)
		if err == nil {
			defer resp.Body.Close()
			contentType := strings.ToLower(resp.Header.Get("Content-Type"))
			if strings.HasPrefix(contentType, "video/") ||
				strings.Contains(contentType, "application/vnd.apple.mpegurl") ||
				strings.Contains(contentType, "application/x-mpegurl") {
				return true
			}
		}
	}

	// 3. 如果 HEAD 请求失败或被禁止，尝试发送 GET 请求并读取前 512 字节进行嗅探
	req, err = http.NewRequest("GET", urlStr, nil)
	if err != nil {
		return false
	}
	// 设置 Range 只读取开头，节省流量
	req.Header.Set("Range", "bytes=0-511")
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	// 再次检查 Content-Type
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.HasPrefix(contentType, "video/") {
		return true
	}

	// 使用 http.DetectContentType 进行嗅探
	buffer := make([]byte, 512)
	n, _ := io.ReadFull(resp.Body, buffer)
	if n > 0 {
		detectedType := http.DetectContentType(buffer[:n])
		return strings.HasPrefix(detectedType, "video/")
	}

	return false
}

func (ds *DownloaderService) downloadSingleFile(taskID string, urlStr string, savePath string, referer string, cookie string, ctrl *taskControl) bool {
	referer = normalizeReferrer(referer)
	cookie = sanitizeHeader(cookie)

	client := &http.Client{
		Timeout: 0, // 下载大文件不设置总超时，由 Read 时的 ctx 控制
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("too many redirects")
			}
			if referer != "" {
				req.Header.Set("Referer", referer)
				if u, err := url.Parse(referer); err == nil {
					req.Header.Set("Origin", u.Scheme+"://"+u.Host)
				}
			}
			if cookie != "" {
				req.Header.Set("Cookie", cookie)
			}
			req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
			return nil
		},
	}

	maxRetries := 3
	var resp *http.Response
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		httpRequest, err := http.NewRequest("GET", urlStr, nil)
		if err != nil {
			ds.sendLog(taskID, "error", fmt.Sprintf("创建请求失败: %v", err))
			return false
		}

		httpRequest.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		if referer != "" {
			httpRequest.Header.Set("Referer", referer)
			if u, err := url.Parse(referer); err == nil {
				httpRequest.Header.Set("Origin", u.Scheme+"://"+u.Host)
			}
		}
		if cookie != "" {
			httpRequest.Header.Set("Cookie", cookie)
		}

		resp, err = client.Do(httpRequest)
		if err == nil && resp.StatusCode == http.StatusOK {
			break
		}

		if resp != nil {
			resp.Body.Close()
		}

		if attempt < maxRetries-1 {
			ds.sendLog(taskID, "warn", fmt.Sprintf("下载请求失败，正在进行第 %d 次重试...", attempt+1))
			time.Sleep(2 * time.Second)
		}
	}

	if err != nil || resp == nil || resp.StatusCode != http.StatusOK {
		ds.sendLog(taskID, "error", "下载请求多次尝试后依然失败")
		return false
	}
	defer resp.Body.Close()

	totalSize := resp.ContentLength
	ds.taskManager.mu.Lock()
	if t, exists := ds.taskManager.tasks[taskID]; exists {
		t.TotalBytes = totalSize // 通用视频用 TotalBytes 存真实字节数，不再复用 TotalSegments
	}
	ds.taskManager.mu.Unlock()

	out, err := os.Create(savePath)
	if err != nil {
		ds.sendLog(taskID, "error", fmt.Sprintf("创建本地文件失败: %v", err))
		return false
	}
	defer out.Close()

	buffer := make([]byte, 32*1024)
	var downloaded int64
	startTime := time.Now()
	lastUpdate := time.Now()

	for {
		select {
		case <-ctrl.stopped:
			out.Close()
			os.Remove(savePath)
			return false
		case <-ctrl.paused:
			select {
			case <-ctrl.resumed:
			case <-ctrl.stopped:
				out.Close()
				os.Remove(savePath)
				return false
			}
		default:
		}

		n, err := resp.Body.Read(buffer)
		if n > 0 {
			_, werr := out.Write(buffer[:n])
			if werr != nil {
				ds.sendLog(taskID, "error", fmt.Sprintf("写入文件失败: %v", werr))
				return false
			}
			downloaded += int64(n)

			now := time.Now()
			if now.Sub(lastUpdate) >= 500*time.Millisecond || downloaded == totalSize {
				duration := now.Sub(startTime).Seconds()
				speedStr := "0 KB/s"
				if duration > 0 {
					speed := float64(downloaded) / duration
					if speed > 1024*1024 {
						speedStr = fmt.Sprintf("%.2f MB/s", speed/1024/1024)
					} else {
						speedStr = fmt.Sprintf("%.2f KB/s", speed/1024)
					}
				}

				progress := 0.0
				if totalSize > 0 {
					progress = float64(downloaded) / float64(totalSize) * 100
				}
				ds.sendProgress(taskID, progress, speedStr, int(downloaded/1024), int(totalSize/1024))

				// 同步更新通用视频的真实字节数字段
				ds.taskManager.mu.Lock()
				if t, exists := ds.taskManager.tasks[taskID]; exists {
					t.DownloadedBytes = downloaded
				}
				ds.taskManager.mu.Unlock()

				lastUpdate = now
			}
		}

		if err != nil {
			if err == io.EOF {
				break
			}
			ds.sendLog(taskID, "error", fmt.Sprintf("读取网络数据失败: %v", err))
			return false
		}
	}

	return true
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
