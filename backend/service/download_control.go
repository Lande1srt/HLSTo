package service

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m3u8-downloader-web/model"
)

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

func (ds *DownloaderService) downloadSingleFile(taskID string, urlStr string, savePath string, referer string, cookie string, ctrl *taskControl) bool {
	referer = normalizeReferrer(referer)
	cookie = sanitizeHeader(cookie)

	// 绑定可取消 ctx：用户停止时取消请求，确保阻塞在建立连接或读取响应体上的 goroutine
	// 能立即退出，从而可靠释放下载信号量与任务控制块，避免必须重启应用才能再次下载。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopWatcherDone := make(chan struct{})
	go func() {
		defer close(stopWatcherDone)
		if ctrl != nil {
			select {
			case <-ctrl.stopped:
				cancel()
			case <-ctx.Done():
			}
		}
	}()

	client := &http.Client{
		Timeout: 0, // 下载大文件不设置总超时，由请求 ctx（停止/暂停）控制
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
		// 进入请求前若已被停止，立即退出（不释放残留半成品文件由调用方清理流程处理）
		if ctrl != nil {
			select {
			case <-ctrl.stopped:
				return false
			default:
			}
		}

		httpRequest, reqErr := http.NewRequestWithContext(ctx, "GET", urlStr, nil)
		if reqErr != nil {
			ds.sendLog(taskID, "error", fmt.Sprintf("创建请求失败: %v", reqErr))
			return false
		}

		httpRequest.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
		if referer != "" {
			httpRequest.Header.Set("Referer", referer)
			if u, parseErr := url.Parse(referer); parseErr == nil {
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
			// 重试等待期间也要能被停止立即打断
			if ctrl != nil {
				select {
				case <-ctrl.stopped:
					return false
				case <-time.After(2 * time.Second):
				}
			} else {
				time.Sleep(2 * time.Second)
			}
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
			// 用户停止触发 ctx 取消：静默退出并清理半成品，不记为网络错误
			if ctx.Err() != nil || (ctrl != nil && func() bool {
				select {
				case <-ctrl.stopped:
					return true
				default:
					return false
				}
			}()) {
				out.Close()
				os.Remove(savePath)
				return false
			}
			ds.sendLog(taskID, "error", fmt.Sprintf("读取网络数据失败: %v", err))
			return false
		}
	}

	return true
}

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
