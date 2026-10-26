package service

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"m3u8-downloader-web/model"
)

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
