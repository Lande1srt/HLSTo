package service

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

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
