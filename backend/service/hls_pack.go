package service

import (
	"context"
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
	"path/filepath"
	"sort"
	"strings"
	"time"

	"m3u8-downloader-web/model"
)

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
