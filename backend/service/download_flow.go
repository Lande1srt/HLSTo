package service

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"m3u8-downloader-web/model"
)

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
		// 重试：优先复用原工作目录（失败时保留下来的分片在此），修复新旧目录错位；
		// 老任务无 WorkDir 记录时回退 {pwd}/{OutputName}。
		downloadDir = ""
		if t, ok := ds.taskManager.GetTask(taskID); ok && t.WorkDir != "" {
			downloadDir = t.WorkDir
		}
		// 历史损坏自愈：若该工作目录同时被其它任务引用（旧版本目录名仅精确到分钟造成的
		// 多任务共用目录），其中分片互相覆盖、无法安全续传。放弃复用，改分配全新唯一目录，
		// 让损坏任务无需重启应用即可干净重试。
		if downloadDir != "" && ds.taskManager.IsWorkDirShared(downloadDir, taskID) {
			ds.sendLog(taskID, "warn", "检测到工作目录与其它任务冲突（旧版本遗留），已切换到全新目录重新下载")
			downloadDir = ""
		}
		if downloadDir == "" && req.OutputName == "" {
			downloadDir = ds.uniqueWorkDir(pwd, taskID)
		}
		if downloadDir == "" {
			downloadDir = filepath.Join(pwd, req.OutputName)
		}
	} else {
		// 新下载：时间戳（精确到秒）+ 任务 ID 短后缀，保证并发/同秒启动的任务
		// 工作目录全局唯一，避免多个任务共用同一 cache 目录导致分片互相覆盖、
		// 已存在残片被误判为"已下载"而跳过、进度错乱等问题。
		downloadDir = ds.uniqueWorkDir(pwd, taskID)
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

		ok, holding := ds.downloader(taskID, req.ThreadCount, key, cacheDir, tsList, ctrl, req.Referer, req.Cookie, downloadSem)
		if !ok {
			// 下载失败或被停止。若暂停期间已让出 downloadSem 且未重新持有，
			// 必须告知收尾 defer 不要再释放，否则会错误释放其它任务正在使用的许可。
			downloadFinished = !holding
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

// uniqueWorkDir 为任务生成全局唯一的工作目录：时间戳精确到秒 + 任务 ID 短后缀；
// 若极端情况下目录已存在（同秒碰撞或历史残留），追加序号直到拿到不存在的路径，
// 从机制上杜绝多个任务共用同一工作目录。

func (ds *DownloaderService) uniqueWorkDir(pwd, taskID string) string {
	timestamp := time.Now().Format("060102030405")
	shortID := taskID
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}
	base := fmt.Sprintf("download_%s_%s", timestamp, shortID)
	dir := filepath.Join(pwd, base)
	for i := 1; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return dir
		}
		dir = filepath.Join(pwd, fmt.Sprintf("%s_%d", base, i))
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
