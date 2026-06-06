package service

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"log"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"m3u8-downloader-web/model"
	"m3u8-downloader-web/websocket"

	"github.com/google/uuid"
	"github.com/levigross/grequests"
	"github.com/yapingcat/gomedia/go-mp4"
	"github.com/yapingcat/gomedia/go-mpeg2"
)

const (
	HEAD_TIMEOUT     = 5 * time.Second
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

type DownloaderService struct {
	taskManager *TaskManager
	wsManager   *websocket.WebSocketManager
	controls    map[string]*taskControl
	mu          sync.RWMutex

	// 队列控制信号量
	downloadSem chan struct{}
	mergeSem    chan struct{}
	uploadSem   chan struct{}
}

func NewDownloaderService(taskManager *TaskManager, wsManager *websocket.WebSocketManager) *DownloaderService {
	return &DownloaderService{
		taskManager: taskManager,
		wsManager:   wsManager,
		controls:    make(map[string]*taskControl),
		downloadSem: make(chan struct{}, 1),
		mergeSem:    make(chan struct{}, 1),
		uploadSem:   make(chan struct{}, 1),
	}
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

	// --- 阶段 1: 下载 ---
	ds.sendStatus(taskID, model.StatusPending, "正在等待下载队列...")
	select {
	case ds.downloadSem <- struct{}{}:
		// 获得下载权
	case <-ctrl.stopped:
		return
	}

	// 确保下载权释放
	downloadFinished := false
	defer func() {
		if !downloadFinished {
			<-ds.downloadSem
		}
	}()

	ds.sendLog(taskID, "info", fmt.Sprintf("开始下载: %s (WebDAV上传: %v)", req.URL, req.EnableWebDAV))

	pwd, err := os.Getwd()
	if err != nil {
		return
	}
	if req.SavePath != "" {
		pwd = req.SavePath
	}

	timestamp := time.Now().Format("0601020304")
	downloadDir := filepath.Join(pwd, fmt.Sprintf("download_%s", timestamp))
	cacheDir := filepath.Join(downloadDir, "cache")

	if exists, _ := pathExists(cacheDir); !exists {
		os.MkdirAll(cacheDir, os.ModePerm)
	}

	ds.sendStatus(taskID, model.StatusDownloading, "正在解析 m3u8...")

	m3u8Host := ds.getHost(req.URL, req.HostType)
	m3u8Body := ds.getM3u8Body(req.URL)
	if m3u8Body == "" {
		ds.sendStatus(taskID, model.StatusFailed, "无法获取 m3u8 内容，请检查 URL 是否有效")
		return
	}

	key := ds.getM3u8Key(m3u8Host, m3u8Body)
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

	if !ds.downloader(taskID, req.ThreadCount, key, cacheDir, tsList, ctrl) {
		// Task was stopped or failed
		return
	}

	if ok := ds.checkTsDownDir(taskID, cacheDir, tsList); !ok {
		ds.sendStatus(taskID, model.StatusFailed, "合并前检查失败: 文件不完整")
		return
	}

	downloadFinished = true
	<-ds.downloadSem // 下载阶段结束，释放信号量

	// --- 阶段 2: 合并 ---
	ds.sendStatus(taskID, model.StatusMerging, "正在等待合并队列...")
	ds.sendProgress(taskID, 0, "等待队列", 0, 0)
	select {
	case ds.mergeSem <- struct{}{}:
		// 获得合并权
	case <-ctrl.stopped:
		return
	}

	// 确保合并权释放
	mergeFinished := false
	defer func() {
		if !mergeFinished {
			<-ds.mergeSem
		}
	}()

	ds.sendStatus(taskID, model.StatusMerging, "正在合并文件...")
	ds.sendProgress(taskID, 0, "开始合并", 0, 0)
	mv := ds.mergeTs(taskID, cacheDir, downloadDir, req.OutputName, ctrl)

	if mv == "" {
		// 说明合并被停止或出错
		return
	}

	// 合并完成后立即保存路径，防止后续上传失败导致路径丢失
	ds.taskManager.mu.Lock()
	if task, exists := ds.taskManager.tasks[taskID]; exists {
		task.OutputPath = mv
	}
	ds.taskManager.mu.Unlock()

	if req.AutoClear {
		os.RemoveAll(cacheDir)
	}

	mergeFinished = true
	<-ds.mergeSem // 合并阶段结束，释放信号量

	// --- 阶段 3: 上传 ---
	if req.EnableWebDAV && req.WebDAVURL != "" {
		ds.sendStatus(taskID, model.StatusUploading, "正在等待上传队列...")
		select {
		case ds.uploadSem <- struct{}{}:
			// 获得上传权
		case <-ctrl.stopped:
			return
		}
		defer func() { <-ds.uploadSem }()

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

		err := webdavService.UploadFile(mv, req.OutputName+".mp4", ctrl.stopped, func(downloaded, total int64) {
			progress := 0.0
			if total > 0 {
				progress = float64(downloaded) / float64(total) * 100
			}
			// 将字节转换为 KB 以便在前端显示，KB 比较稳妥且能显示更多细节
			curKB := int(downloaded / 1024)
			totalKB := int(total / 1024)
			ds.sendProgress(taskID, progress, "上传中", curKB, totalKB)
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
				os.RemoveAll(downloadDir)
				ds.sendLog(taskID, "info", "已清理本地下载目录")
			}
		}
	} else {
		// 未开启 WebDAV，直接标记为完成
		ds.markTaskCompleted(taskID, mv, "下载完成")
	}

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

func (ds *DownloaderService) getM3u8Body(Url string) string {
	ro := &grequests.RequestOptions{
		UserAgent:      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_13_6) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/79.0.3945.88 Safari/537.36",
		RequestTimeout: HEAD_TIMEOUT,
		Headers: map[string]string{
			"Connection":      "keep-alive",
			"Accept":          "*/*",
			"Accept-Encoding": "*",
			"Accept-Language": "zh-CN,zh;q=0.9,en;q=0.8",
		},
	}

	r, err := grequests.Get(Url, ro)
	if err != nil {
		return ""
	}
	return r.String()
}

func (ds *DownloaderService) getM3u8Key(host, html string) string {
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
			res, err := grequests.Get(keyUrl, nil)
			if err != nil || res.StatusCode != 200 {
				continue
			}
			return res.String()
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

func (ds *DownloaderService) downloadTsFile(ts TsInfo, downloadDir, key string, retries int) bool {
	currPathFile := filepath.Join(downloadDir, ts.Name)
	if exists, _ := pathExists(currPathFile); exists {
		return true
	}

	for attempt := 0; attempt < retries; attempt++ {
		success := func() bool {
			ro := &grequests.RequestOptions{
				UserAgent:      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36",
				RequestTimeout: HEAD_TIMEOUT,
			}

			res, err := grequests.Get(ts.Url, ro)
			if err != nil || !res.Ok {
				return false
			}

			origData := res.Bytes()
			if len(origData) == 0 {
				return false
			}

			// 验证 Content-Length 确保下载完整
			contentLenStr := res.Header.Get("Content-Length")
			if contentLenStr != "" {
				expectedLen, _ := strconv.Atoi(contentLenStr)
				if expectedLen > 0 && len(origData) != expectedLen {
					return false
				}
			}

			// 如果有加密，先解密
			if key != "" {
				origData, err = ds.AesDecrypt(origData, []byte(key))
				if err != nil {
					return false
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
				return false
			}

			// 重命名为正式文件
			err = os.Rename(tempPath, currPathFile)
			if err != nil {
				os.Remove(tempPath)
				return false
			}

			return true
		}()

		if success {
			return true
		}
		
		// 失败重试前稍微等待一下，避免瞬时网络问题
		if attempt < retries-1 {
			time.Sleep(500 * time.Millisecond)
		}
	}

	return false
}

func (ds *DownloaderService) downloader(taskID string, maxGoroutines int, key string, cacheDir string, tsList []TsInfo, ctrl *taskControl) bool {
	retry := 3
	limiter := make(chan struct{}, maxGoroutines)
	tsLen := len(tsList)
	var countMu sync.Mutex

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

				success := ds.downloadTsFile(ts, cacheDir, key, retry)

				if success {
					countMu.Lock()
					downloadCount++
					progress := float64(downloadCount) / float64(tsLen) * 100
					if progress > 100 {
						progress = 100
					}
					ds.sendProgress(taskID, progress, "N/A", downloadCount, tsLen)
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

func (ds *DownloaderService) checkTsDownDir(taskID string, dir string, tsList []TsInfo) bool {
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
				log.Printf("[Info] 任务 %s 在合并阶段停止\n", taskID)
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

func (ds *DownloaderService) RetryDownload(taskID string) error {
	task, exists := ds.taskManager.GetTask(taskID)
	if !exists {
		return fmt.Errorf("任务不存在")
	}

	if task.Status != model.StatusFailed && task.Status != model.StatusCompleted {
		return fmt.Errorf("只有已停止或已完成的任务可以重试")
	}

	// 准备重试请求
	req := model.DownloadRequest{
		URL:               task.URL,
		ThreadCount:       task.ThreadCount,
		OutputName:        task.Name,
		HostType:          task.HostType,
		Cookie:            task.Cookie,
		AutoClear:         task.AutoClear,
		SavePath:          task.SavePath,
		EnableWebDAV:      task.EnableWebDAV,
		WebDAVURL:         task.WebDAVURL,
		WebDAVUsername:    task.WebDAVUsername,
		WebDAVPassword:    task.WebDAVPassword,
		WebDAVRemoteDir:   task.WebDAVRemoteDir,
		DeleteAfterUpload: task.DeleteAfterUpload,
	}

	// 更新任务状态并广播
	ds.sendStatus(taskID, model.StatusDownloading, "正在重试下载...")
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
		err := webdavService.UploadFile(task.OutputPath, task.Name+".mp4", ctrl.stopped, func(downloaded, total int64) {
			progress := 0.0
			if total > 0 {
				progress = float64(downloaded) / float64(total) * 100
			}
			curKB := int(downloaded / 1024)
			totalKB := int(total / 1024)
			ds.sendProgress(taskID, progress, "手动上传中", curKB, totalKB)
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
				os.RemoveAll(downloadDir)
				ds.sendLog(taskID, "info", "已清理本地下载目录")
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

func (ds *DownloaderService) AnalyzeM3U8(urlStr string) (map[string]interface{}, error) {
	m3u8Body := ds.getM3u8Body(urlStr)
	if m3u8Body == "" {
		return nil, fmt.Errorf("无法获取 m3u8 内容")
	}

	host := ds.getHost(urlStr, "v1")
	tsList := ds.getTsList(host, m3u8Body)
	key := ds.getM3u8Key(host, m3u8Body)

	info := map[string]interface{}{
		"segments": len(tsList),
		"hasKey":   key != "",
	}

	return info, nil
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
