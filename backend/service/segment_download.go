package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"m3u8-downloader-web/model"
)

func (ds *DownloaderService) downloadTsFile(taskID string, ts TsInfo, downloadDir, key string, retries int, referer string, cookie string, ctrl *taskControl) (int64, bool) {
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

	// 绑定可取消 ctx：停止任务时立即中断正在进行的分片请求，避免 goroutine
	// 被挂起的连接拖住而无法释放下载并发槽位。
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

	// 预先准备好 Origin
	var origin string
	if referer != "" {
		if u, err := url.Parse(referer); err == nil {
			origin = u.Scheme + "://" + u.Host
		}
	}

	// 复用共享连接池 client，避免每次尝试都重建 TCP/TLS 连接
	client := ds.segHTTPClient
	if client == nil {
		client = &http.Client{Timeout: HEAD_TIMEOUT}
	}

	for attempt := 0; attempt < retries; attempt++ {
		// 重试前检查停止信号
		if ctrl != nil {
			select {
			case <-ctrl.stopped:
				return 0, false
			default:
			}
		}
		success, size := func() (bool, int64) {
			req, err := http.NewRequestWithContext(ctx, "GET", ts.Url, nil)
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
				// 字节已实际从网卡接收但因长度不符被丢弃，将重下——记录以便定位
				// "网卡消耗远高于界面有效速度、目录却无异常文件"的流量去向。
				ds.sendLog(taskID, "warn", fmt.Sprintf(
					"分片 %s 已接收 %d 字节但与 Content-Length(%d) 不符，丢弃并重试",
					ts.Name, len(origData), contentLen))
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

		// 失败重试前指数退避（0.5s/1s/2s），并可被停止立即打断；
		// 退避可降低对故障源的无效冲击，避免短时间内反复拉取同一分片放大带宽消耗。
		if attempt < retries-1 {
			backoff := time.Duration(500<<attempt) * time.Millisecond
			if ctrl != nil {
				select {
				case <-ctrl.stopped:
					return 0, false
				case <-time.After(backoff):
				}
			} else {
				time.Sleep(backoff)
			}
		}
	}

	return 0, false
}

// downloader 执行分片并发下载。返回值：
//   - ok=false：下载失败或被停止；
//   - holding：返回 false 时表示因暂停期间让出且最终未重新持有 downloadSem
//     （仅当被停止），外层收尾 defer 必须跳过对 downloadSem 的释放，避免误释放他人许可。

func (ds *DownloaderService) downloader(taskID string, maxGoroutines int, key string, cacheDir string, tsList []TsInfo, ctrl *taskControl, referer string, cookie string, downloadSem *semaphore) (ok bool, holding bool) {
	holding = true
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
			// 暂停关卡：暂停期间让出下载信号量，避免并发=1 时一个暂停任务冻结其它所有下载
			cont, hold := ds.yieldSemWhilePaused(ctrl, downloadSem)
			if !cont {
				return false, hold
			}
			holding = hold

			wg.Add(1)
			limiter <- struct{}{}

			go func(ts TsInfo) {
				defer func() {
					wg.Done()
					<-limiter
				}()

				size, success := ds.downloadTsFile(taskID, ts, cacheDir, key, retry, referer, cookie, ctrl)

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
		return false, holding
	}

	return true, holding
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
