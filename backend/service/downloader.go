package service

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
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
	paused  chan struct{}
	resumed chan struct{}
	stopped chan struct{}
	mu      sync.Mutex
	isPaused bool
}

type semaphore struct {
	sem chan struct{}
}

type DownloaderService struct {
	taskManager *TaskManager
	wsManager   *websocket.WebSocketManager
	controls    map[string]*taskControl
	mu          sync.RWMutex

	downloadSem atomic.Pointer[semaphore]
	mergeSem    atomic.Pointer[semaphore]
	uploadSem   atomic.Pointer[semaphore]
	
	// 配置
	downloadConcurrency atomic.Int32
	mergeConcurrency    atomic.Int32
	uploadConcurrency   atomic.Int32
	singleMode          atomic.Bool
	singleSem           chan struct{} // 单状态模式下的全局信号量
}

func NewDownloaderService(taskManager *TaskManager, wsManager *websocket.WebSocketManager) *DownloaderService {
	ds := &DownloaderService{
		taskManager: taskManager,
		wsManager:   wsManager,
		controls:    make(map[string]*taskControl),
		singleSem:   make(chan struct{}, 1),
	}
	
	// 初始化原子指针
	ds.downloadSem.Store(&semaphore{sem: make(chan struct{}, 1)})
	ds.mergeSem.Store(&semaphore{sem: make(chan struct{}, 1)})
	ds.uploadSem.Store(&semaphore{sem: make(chan struct{}, 1)})
	
	// 初始化配置
	ds.downloadConcurrency.Store(1)
	ds.mergeConcurrency.Store(1)
	ds.uploadConcurrency.Store(1)
	ds.singleMode.Store(false)
	
	return ds
}

func (ds *DownloaderService) UpdateConcurrencyConfig(download, merge, upload int, singleMode bool) {
	// 使用原子操作更新配置
	ds.downloadConcurrency.Store(int32(download))
	ds.mergeConcurrency.Store(int32(merge))
	ds.uploadConcurrency.Store(int32(upload))
	ds.singleMode.Store(singleMode)
	
	ds.downloadSem.Store(&semaphore{sem: make(chan struct{}, download)})
	ds.mergeSem.Store(&semaphore{sem: make(chan struct{}, merge)})
	ds.uploadSem.Store(&semaphore{sem: make(chan struct{}, upload)})
	
	log.Printf("[Downloader] 并发配置已更新: 下载=%d, 合并=%d, 上传=%d, 单模式=%v\n", 
		download, merge, upload, singleMode)
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

func (ds *DownloaderService) removeControl(taskID string) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	delete(ds.controls, taskID)
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
	defer ds.removeControl(taskID)
	runtime.GOMAXPROCS(runtime.NumCPU())

	ctrl, _ := ds.getControl(taskID)
	var downloadDir string
	
	// 记录任务是否成功完成
	taskCompleted := false
	defer func() {
		if !taskCompleted && downloadDir != "" {
			// 下载失败，清理缓存目录
			if err := os.RemoveAll(downloadDir); err != nil {
				log.Printf("[Cleanup] 清理失败目录时出错: %v\n", err)
			} else {
				log.Printf("[Cleanup] 已清理失败任务的缓存目录: %s\n", downloadDir)
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
	select {
	case downloadSem.sem <- struct{}{}:
		// 获得下载权
	case <-ctrl.stopped:
		return
	}

	// 确保下载权释放
	downloadFinished := false
	defer func() {
		if !downloadFinished {
			<-downloadSem.sem
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
		downloadDir = filepath.Join(pwd, req.OutputName)
	} else {
		// 新下载：使用时间戳作为目录名
		timestamp := time.Now().Format("0601020304")
		downloadDir = filepath.Join(pwd, fmt.Sprintf("download_%s", timestamp))
	}
	cacheDir := filepath.Join(downloadDir, "cache")

	if exists, _ := pathExists(cacheDir); !exists {
		os.MkdirAll(cacheDir, os.ModePerm)
	}

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
		<-downloadSem.sem

		// --- 阶段 2: 合并 ---
		ds.sendStatus(taskID, model.StatusMerging, "正在等待合并队列...")
		ds.sendProgress(taskID, 0, "等待队列", 0, 0)
		
		// 获取当前的合并信号量（使用原子指针）
		mergeSem := ds.mergeSem.Load()
		select {
		case mergeSem.sem <- struct{}{}:
			// 获得合并权
		case <-ctrl.stopped:
			return
		}

		// 确保合并权释放
		mergeFinished := false
		defer func() {
			if !mergeFinished {
				<-mergeSem.sem
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
		<-mergeSem.sem // 合并阶段结束，释放信号量
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
		<-downloadSem.sem // 下载阶段结束，释放信号量
		
		// 通用文件跳过合并阶段
		ds.sendLog(taskID, "info", "通用文件下载完成，跳过合并阶段")
	}

	// 更新输出路径到任务对象
	ds.taskManager.mu.Lock()
	if task, exists := ds.taskManager.tasks[taskID]; exists {
		task.OutputPath = mv
	}
	ds.taskManager.mu.Unlock()

	// --- 阶段 3: 上传 ---
	if req.EnableWebDAV && req.WebDAVURL != "" {
		ds.sendStatus(taskID, model.StatusUploading, "正在等待上传队列...")
		
		// 获取当前的上传信号量（使用原子指针）
		uploadSem := ds.uploadSem.Load()
		select {
		case uploadSem.sem <- struct{}{}:
			// 获得上传权
		case <-ctrl.stopped:
			return
		}
		defer func() { <-uploadSem.sem }()

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

		remoteFileName := req.OutputName + ".mp4"
		if !isM3U8 {
			remoteFileName = filepath.Base(mv)
		}

		err := webdavService.UploadFile(mv, remoteFileName, ctrl.stopped, func(downloaded, total int64, speed string) {
			progress := 0.0
			if total > 0 {
				progress = float64(downloaded) / float64(total) * 100
			}
			// 将字节转换为 KB 以便在前端显示，KB 比较稳妥且能显示更多细节
			curKB := int(downloaded / 1024)
			totalKB := int(total / 1024)
			ds.sendProgress(taskID, progress, speed, curKB, totalKB)
		})

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
	}
	ds.taskManager.mu.Unlock()

	ds.sendStatus(taskID, model.StatusCompleted, message)
	ds.sendProgress(taskID, 100, "完成", total, total)
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
		if stat, err := os.Stat(currPathFile); err == nil {
			return stat.Size(), true
		}
		return 0, true
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
	limiter := make(chan struct{}, maxGoroutines)
	tsLen := len(tsList)
	var countMu sync.Mutex

	// 速度统计
	var totalBytes int64
	startTime := time.Now()
	lastUpdateTime := time.Now()

	// 更新状态为正在下载分片
	ds.sendStatus(taskID, model.StatusDownloading, "正在下载分片...")

	getMissing := func() []TsInfo {
		var missing []TsInfo
		for _, ts := range tsList {
			currPathFile := filepath.Join(cacheDir, ts.Name)
			if exists, _ := pathExists(currPathFile); !exists {
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
		if exists, _ := pathExists(filepath.Join(dir, ts.Name)); !exists {
			missingCount++
		}
	}
	
	if missingCount > 0 {
		ds.sendLog(taskID, "error", fmt.Sprintf("完整性检查失败：缺失 %d 个分片", missingCount))
		return false
	}
	
	ds.sendLog(taskID, "info", "文件完整性检查通过，开始合并...")
	return true
}

func (ds *DownloaderService) mergeTs(taskID string, cacheDir, downloadDir, outputName string, ctrl *taskControl) string {
	mvName := filepath.Join(downloadDir, outputName+".mp4")
	outMv, err := os.Create(mvName)
	if err != nil {
		log.Printf("[Error] 无法创建输出文件: %v\n", err)
		return ""
	}
	defer outMv.Close()

	ds.sendStatus(taskID, model.StatusMerging, "正在转码封装 MP4...")

	// 使用 gomedia 进行纯 Go 的 TS 转 MP4 封装
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

	files, err := os.ReadDir(cacheDir)
	if err != nil {
		return mvName
	}

	var tsFiles []string
	for _, f := range files {
		if !f.IsDir() && filepath.Ext(f.Name()) == ".ts" {
			tsFiles = append(tsFiles, filepath.Join(cacheDir, f.Name()))
		}
	}
	sort.Strings(tsFiles)

	totalFiles := len(tsFiles)
	if totalFiles == 0 {
		return mvName
	}

	// 获取原始总片段数以保持进度条显示的一致性
	displayTotal := totalFiles
	ds.taskManager.mu.RLock()
	if t, exists := ds.taskManager.tasks[taskID]; exists && t.TotalSegments > 0 {
		displayTotal = t.TotalSegments
	}
	ds.taskManager.mu.RUnlock()

	for i, path := range tsFiles {
		// 检查是否已停止
		if ctrl != nil {
			select {
			case <-ctrl.stopped:
				muxer.WriteTrailer()
				outMv.Close()
				os.Remove(mvName) // 清理不完整的合并文件
				ds.sendLog(taskID, "warn", "用户终止了合并过程，已清理临时文件")
				return ""
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
		return fmt.Errorf("只有已停止或已完成的任务可以重试")
	}

	// 解析重试模式
	retryMode := model.RetryMode(mode)
	
	// 如果是强制合并模式，直接执行合并
	if retryMode == model.RetryModeForceMerge {
		ds.sendStatus(taskID, model.StatusMerging, "正在强制合并...")
		ds.sendLog(taskID, "info", "用户选择强制合并，将跳过缺失分片")
		
		// 获取任务路径信息
		taskDir := filepath.Join(task.SavePath, task.Name)
		cacheDir := filepath.Join(taskDir, "cache")
		outputName := task.Name
		
		ctrl := ds.createControl(taskID)
		mvName := ds.mergeTs(taskID, cacheDir, taskDir, outputName, ctrl)
		if mvName != "" {
			ds.sendStatus(taskID, model.StatusCompleted, "强制合并完成")
			ds.sendLog(taskID, "info", fmt.Sprintf("视频已保存到: %s", mvName))
			ds.taskManager.mu.Lock()
			if t, exists := ds.taskManager.tasks[taskID]; exists {
				t.Status = model.StatusCompleted
				t.OutputPath = mvName
				t.Progress = 100
			}
			ds.taskManager.mu.Unlock()
			ds.taskManager.storage.UpdateTask(task)
		} else {
			ds.sendStatus(taskID, model.StatusFailed, "强制合并失败")
		}
		return nil
	}

	// 准备重试请求
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
		ds.sendLog(taskID, "info", "重试模式：仅下载缺失的分片")
	} else {
		ds.sendStatus(taskID, model.StatusDownloading, "正在完全重新下载...")
		ds.sendLog(taskID, "info", "重试模式：完全重新下载所有分片")
		
		// 删除之前的缓存文件
		taskDir := filepath.Join(task.SavePath, task.Name)
		cacheDir := filepath.Join(taskDir, "cache")
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
	ds.taskManager.storage.UpdateTask(task)

	// 创建控制信号
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

	if _, err := os.Stat(task.OutputPath); os.IsNotExist(err) {
		return fmt.Errorf("本地输出文件已不存在，可能已被自动清理或手动删除: %s", task.OutputPath)
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
		err := webdavService.UploadFile(task.OutputPath, task.Name+".mp4", ctrl.stopped, func(downloaded, total int64, speed string) {
			progress := 0.0
			if total > 0 {
				progress = float64(downloaded) / float64(total) * 100
			}
			curKB := int(downloaded / 1024)
			totalKB := int(total / 1024)
			ds.sendProgress(taskID, progress, speed, curKB, totalKB)
		})
		
		ds.removeControl(taskID)
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
		if task.DeleteAfterUpload {
			downloadDir := filepath.Dir(task.OutputPath)
			if strings.Contains(filepath.Base(downloadDir), "download_") {
				if err := os.RemoveAll(downloadDir); err != nil {
					log.Printf("[Downloader] 删除本地下载目录失败: %v\n", err)
					ds.sendLog(taskID, "warn", fmt.Sprintf("删除本地下载目录失败: %v", err))
				} else {
					ds.sendLog(taskID, "info", "已清理本地下载目录")
				}
			} else {
				os.Remove(task.OutputPath)
				ds.sendLog(taskID, "info", "已删除本地文件")
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
		t.TotalSegments = int(totalSize / 1024) // 这里借用 TotalSegments 存储总 KB
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
		Type:      "status",
		TaskID:    taskID,
		Status:    string(status),
		Message:   message,
		Timestamp: time.Now().Format(time.RFC3339),
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
