package service

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yapingcat/gomedia/go-mp4"
	"github.com/yapingcat/gomedia/go-mpeg2"
	"m3u8-downloader-web/model"
)

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

		switch cid {
		case mpeg2.TS_STREAM_H264:
			if vtid == 0 {
				vtid = muxer.AddVideoTrack(mp4.MP4_CODEC_H264)
			}
			muxer.Write(vtid, frame, adjPts, adjDts)
		case mpeg2.TS_STREAM_AAC:
			if atid == 0 {
				atid = muxer.AddAudioTrack(mp4.MP4_CODEC_AAC)
			}
			muxer.Write(atid, frame, adjPts, adjDts)
		case mpeg2.TS_STREAM_H265:
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
