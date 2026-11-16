package service

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"m3u8-downloader-web/model"
	"m3u8-downloader-web/storage"
	"m3u8-downloader-web/websocket"
)

// locateFFmpeg 依次从 PATH、FFMPEG_PATH、私有安装目录查找 ffmpeg；均无返回 ""
func locateFFmpeg(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	candidates := []string{
		os.Getenv("FFMPEG_PATH"),
		filepath.Join("..", "ffmpeg", "bin", ffmpegBinaryName()),
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		if _, err := os.Stat(abs); err == nil {
			return abs
		}
	}
	return ""
}

// newTestStack 在临时工作目录构造存储 + 任务管理器 + 下载服务
func newTestStack(t *testing.T) (*DownloaderService, func()) {
	t.Helper()
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	st, err := storage.NewSQLiteStorage()
	if err != nil {
		t.Fatal(err)
	}
	tm := NewTaskManager(st)
	ds := NewDownloaderService(tm, websocket.NewWebSocketManager())

	cleanup := func() {
		tm.Close()
		_ = st.Close()
		_ = os.Chdir(orig)
	}
	return ds, cleanup
}

func TestCountValidSegments(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}

	// 2 个有效分片 + 1 个 0 字节残片 + 1 个非 ts 文件 + 1 个子目录
	files := map[string][]byte{
		"seg_000.ts": []byte("AAAA"),
		"seg_001.ts": []byte("BBBB"),
		"seg_002.ts": []byte(""),
		"notes.txt":  []byte("x"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(cache, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(cache, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	if got := countValidSegments(cache); got != 2 {
		t.Errorf("countValidSegments = %d, want 2（0 字节残片不计）", got)
	}
	if got := countValidSegments(filepath.Join(cache, "missing")); got != 0 {
		t.Errorf("不存在目录应返回 0，实际 %d", got)
	}
}

func TestRetryDownload_Guards(t *testing.T) {
	ds, cleanup := newTestStack(t)
	defer cleanup()

	// 1. 任务不存在
	if err := ds.RetryDownload("nobody", string(model.RetryModeMissing)); err == nil {
		t.Fatal("不存在的任务应报错")
	}

	// 构造一个进行中的任务
	active := &model.Task{
		ID: "active-1", URL: "http://x/a.m3u8", Name: "active",
		Status: model.StatusDownloading, CreatedAt: time.Now(),
	}
	ds.taskManager.AddTask(active)
	if err := ds.RetryDownload("active-1", string(model.RetryModeMissing)); err == nil {
		t.Fatal("进行中的任务不允许重试")
	}

	// 构造一个失败任务（无任何本地分片，模拟缓存已被清理）
	failed := &model.Task{
		ID: "failed-1", URL: "http://x/a.m3u8", Name: "failed",
		Status: model.StatusFailed, CreatedAt: time.Now(),
	}
	ds.taskManager.AddTask(failed)

	// 2. 非法模式明确报错
	if err := ds.RetryDownload("failed-1", "bogus_mode"); err == nil {
		t.Fatal("非法重试模式应报错")
	}

	// 3. 强制合并但无分片：报错而非产生空/损坏产物
	if err := ds.RetryDownload("failed-1", string(model.RetryModeForceMerge)); err == nil {
		t.Fatal("无分片时强制合并应报错")
	}

	// 4. 上一轮 goroutine 未退出（控制块仍在）：拒绝重试
	ds.createControl("failed-1")
	if err := ds.RetryDownload("failed-1", string(model.RetryModeMissing)); err == nil {
		t.Fatal("旧控制块仍存在时应拒绝重试，防止双 goroutine")
	}
}

// 伪造非 TS 数据的"分片"：强制合并必须识别为无效，不得产出空 MP4 并标记完成
func TestRetryDownload_ForceMergeInvalidContent(t *testing.T) {
	ds, cleanup := newTestStack(t)
	defer cleanup()

	pwd, _ := os.Getwd()
	workDir := filepath.Join(pwd, "download_bad")
	cache := filepath.Join(workDir, "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "seg_000.ts"),
		[]byte("AAAA"), 0o644); err != nil {
		t.Fatal(err)
	}

	failed := &model.Task{
		ID: "fm-bad", URL: "http://x/a.m3u8", Name: "fm",
		Status: model.StatusFailed, CreatedAt: time.Now(),
		WorkDir: workDir,
	}
	ds.taskManager.AddTask(failed)

	ds.UpdatePostDownloadConfig(true, model.MergeMethodGomedia, model.MuxModeCopy)

	if err := ds.RetryDownload("fm-bad", string(model.RetryModeForceMerge)); err == nil {
		t.Fatal("非 TS 内容强制合并应失败")
	}

	task, _ := ds.taskManager.GetTask("fm-bad")
	if task.Status != model.StatusFailed {
		t.Errorf("无效内容后状态 = %s, want failed", task.Status)
	}
	// 无效产物不应残留
	if _, err := os.Stat(filepath.Join(workDir, "fm.mp4")); !os.IsNotExist(err) {
		t.Errorf("无效 MP4 未删除")
	}
	// 同步执行结束后控制块必须已移除
	if _, alive := ds.getControl("fm-bad"); alive {
		t.Fatal("强制合并结束后控制块未清理")
	}
}

// 端到端：真实 HLS 分片删除末片后强制合并，应基于剩余分片产出可播放 MP4。
// 无 ffmpeg 时跳过（定位逻辑与 merge_integration_test 一致）。
func TestRetryDownload_ForceMergeRealSegments(t *testing.T) {
	ffmpegPath := locateFFmpeg(t)
	if ffmpegPath == "" {
		t.Skip("未找到 ffmpeg，跳过强制合并集成测试")
	}

	ds, cleanup := newTestStack(t)
	defer cleanup()

	// 用 ffmpeg 生成 3 个真实 HLS 分片
	pwd, _ := os.Getwd()
	workDir := filepath.Join(pwd, "download_keep")
	cache := filepath.Join(workDir, "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	segPattern := filepath.Join(cache, "seg_%03d.ts")
	indexPath := filepath.Join(workDir, "index.m3u8")

	gen := exec.Command(ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=6:size=320x240:rate=25",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-g", "50", "-force_key_frames", "expr:gte(t,n_forced*2)",
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "0",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", segPattern,
		indexPath)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("生成 HLS 分片失败: %v | %s", err, out)
	}

	segs, err := collectByExt(cache, ".ts")
	if err != nil || len(segs) < 2 {
		t.Fatalf("有效分片不足: %d (err=%v)", len(segs), err)
	}
	// 删除最后一个分片，模拟"缺失"
	if err := os.Remove(segs[len(segs)-1]); err != nil {
		t.Fatal(err)
	}

	failed := &model.Task{
		ID: "fm-real", URL: "http://x/a.m3u8", Name: "fm",
		Status: model.StatusFailed, CreatedAt: time.Now(),
		WorkDir: workDir, MergeAfterDownload: true,
		MergeMethod: model.MergeMethodGomedia, FFmpegMuxMode: model.MuxModeCopy,
		HLSPackEnabled: true, HLSPackForm: model.HLSPackFormSingle,
		NodeOrigin: "http://node.example",
	}
	ds.taskManager.AddTask(failed)

	// 全局配置与任务快照相反，验证强制合并及后续分片只使用快照。
	ds.SetFFmpegService(NewFFmpegService())
	ds.UpdatePostDownloadConfig(true, model.MergeMethodFFmpeg, model.MuxModeH265)
	ds.UpdateHLSPackConfig(false, false, model.HLSEncryptGenerated, "", model.HLSPackFormMulti)

	if err := ds.RetryDownload("fm-real", string(model.RetryModeForceMerge)); err != nil {
		t.Fatalf("强制合并失败: %v", err)
	}

	task, _ := ds.taskManager.GetTask("fm-real")
	if task.Status != model.StatusCompleted {
		t.Fatalf("状态 = %s, want completed（err=%s）", task.Status, task.Error)
	}
	info, err := os.Stat(task.OutputPath)
	if err != nil || info.Size() == 0 {
		t.Fatalf("强制合并产物异常: %v", err)
	}
	if _, alive := ds.getControl("fm-real"); alive {
		t.Fatal("强制合并结束后控制块未清理")
	}

	hlsDir := filepath.Join(workDir, "fm_hls")
	indexData, err := os.ReadFile(filepath.Join(hlsDir, "index.m3u8"))
	if err != nil || !strings.Contains(string(indexData), "EXT-X-BYTERANGE") {
		t.Fatalf("强制合并后未生成 BYTERANGE 索引: %v", err)
	}
	if info, err := os.Stat(filepath.Join(hlsDir, "fm.tsbin")); err != nil || info.Size() == 0 {
		t.Fatalf("强制合并后未生成 tsbin: %v", err)
	}
	metaData, err := os.ReadFile(filepath.Join(hlsDir, "meta.json"))
	if err != nil || !strings.Contains(string(metaData), "http://node.example") {
		t.Fatalf("重试后的 meta.json 未保留节点快照: %v", err)
	}
}

func TestStartDownloadUsesConfigSnapshot(t *testing.T) {
	ds, cleanup := newTestStack(t)
	defer cleanup()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-ENDLIST\n"))
	}))
	defer srv.Close()

	ds.UpdatePostDownloadConfig(false, model.MergeMethodGomedia, model.MuxModeH265)
	ds.UpdateCompressConfig(true, 4096, 3000)
	ds.UpdateHLSPackConfig(true, true, model.HLSEncryptSpecified, "http://example.com/enc.key", model.HLSPackFormSingle)
	ds.UpdateUploadModeConfig(true)

	task, err := ds.StartDownload(model.DownloadRequest{
		URL: srv.URL + "/index.m3u8", OutputName: "snapshot", ThreadCount: 1,
		SavePath: t.TempDir(), NodeOrigin: "http://node.example",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 新建后立即改掉所有全局配置，已有任务仍必须保留并执行原快照。
	ds.UpdatePostDownloadConfig(true, model.MergeMethodFFmpeg, model.MuxModeCopy)
	ds.UpdateCompressConfig(false, 2048, 0)
	ds.UpdateHLSPackConfig(false, false, model.HLSEncryptGenerated, "", model.HLSPackFormMulti)
	ds.UpdateUploadModeConfig(false)

	if task.MergeAfterDownload || task.MergeMethod != model.MergeMethodGomedia || task.FFmpegMuxMode != model.MuxModeH265 ||
		!task.CompressAfterMerge || task.CompressBitrateThreshold != 4096 || task.CompressTargetBitrate != 3000 ||
		!task.HLSPackEnabled || !task.HLSEncryptEnabled || task.HLSEncryptMode != model.HLSEncryptSpecified ||
		task.HLSKeyURL != "http://example.com/enc.key" || task.HLSPackForm != model.HLSPackFormSingle ||
		!task.EnableChunkedUpload || task.NodeOrigin != "http://node.example" {
		t.Fatalf("任务未保存创建时的完整配置快照: %+v", task)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if current, _ := ds.taskManager.GetTask(task.ID); current.Status == model.StatusCompleted {
			return
		} else if current.Status == model.StatusFailed {
			t.Fatalf("快照任务执行失败: %s", current.Error)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("任务未按创建时的“不合并”快照完成")
}

func TestInterruptedStatusesBecomeRetryable(t *testing.T) {
	tm := NewTaskManager(nil)
	defer tm.Close()

	interrupted := []model.TaskStatus{
		model.StatusPending, model.StatusDownloading, model.StatusMerging,
		model.StatusCompressing, model.StatusPacking, model.StatusUploading, model.StatusPaused,
	}
	for _, status := range interrupted {
		tm.AddTask(&model.Task{ID: string(status), Status: status, CreatedAt: time.Now()})
	}
	tm.AddTask(&model.Task{ID: "completed", Status: model.StatusCompleted, CreatedAt: time.Now()})

	_ = NewDownloaderService(tm, websocket.NewWebSocketManager())
	for _, status := range interrupted {
		task, _ := tm.GetTask(string(status))
		if task.Status != model.StatusFailed {
			t.Errorf("中断状态 %s 未转为可重试失败，当前=%s", status, task.Status)
		}
	}
	if task, _ := tm.GetTask("completed"); task.Status != model.StatusCompleted {
		t.Errorf("已完成任务不应被修改，当前=%s", task.Status)
	}
}

// TestLegacyTaskBackfillOnRetry 模拟旧版创建的任务（快照字段全为零值）：
// 进入 RetryDownload 时补齐函数必须按当前全局设置回填所有配置并持久化，
// 且后续第二次调用不会再改写。
func TestLegacyTaskBackfillOnRetry(t *testing.T) {
	ds, cleanup := newTestStack(t)
	defer cleanup()

	// 构造旧版任务：所有快照字段全为零值
	task := &model.Task{
		ID:        "legacy-fm",
		Name:      "legacy",
		Status:    model.StatusFailed,
		Error:     "旧任务，快照缺失",
		CreatedAt: time.Now().Add(-24 * time.Hour),
	}
	ds.taskManager.AddTask(task)

	// 当前全局设置
	ds.UpdatePostDownloadConfig(true, model.MergeMethodGomedia, model.MuxModeCopy)
	ds.UpdateHLSPackConfig(true, false, model.HLSEncryptGenerated, "", model.HLSPackFormSingle)
	ds.UpdateCompressConfig(true, 4096, 3000)
	ds.UpdateUploadModeConfig(true)

	// 直接触发补齐（模拟 RetryDownload 入口的行为）
	ds.backfillLegacyTaskSnapshot(task)

	// 补齐后字段应与当前全局设置一致
	if !task.MergeAfterDownload || task.MergeMethod != model.MergeMethodGomedia ||
		task.FFmpegMuxMode != model.MuxModeCopy {
		t.Fatalf("合并配置未正确补齐: %+v", task)
	}
	if !task.HLSPackEnabled || task.HLSPackForm != model.HLSPackFormSingle {
		t.Fatalf("HLS 配置未正确补齐: enabled=%v form=%q", task.HLSPackEnabled, task.HLSPackForm)
	}
	if !task.CompressAfterMerge || task.CompressBitrateThreshold != 4096 ||
		task.CompressTargetBitrate != 3000 {
		t.Fatalf("压缩配置未正确补齐: %+v", task)
	}
	if !task.EnableChunkedUpload {
		t.Fatal("EnableChunkedUpload 未补齐为 true")
	}

	// 持久化验证：从 TaskManager（SQLite）重新加载
	reloaded, _ := ds.taskManager.GetTask("legacy-fm")
	if reloaded.MergeMethod != model.MergeMethodGomedia || reloaded.HLSPackForm != model.HLSPackFormSingle ||
		reloaded.CompressBitrateThreshold != 4096 || !reloaded.EnableChunkedUpload {
		t.Fatalf("补齐后未正确持久化到 SQLite: %+v", reloaded)
	}

	// 后续把全局设置完全改掉，再调补齐函数：已补齐的任务不应被改写
	ds.UpdatePostDownloadConfig(false, model.MergeMethodFFmpeg, model.MuxModeH265)
	ds.UpdateHLSPackConfig(false, false, model.HLSEncryptGenerated, "", model.HLSPackFormMulti)
	ds.UpdateCompressConfig(false, 2048, 0)
	ds.UpdateUploadModeConfig(false)

	ds.backfillLegacyTaskSnapshot(task)
	if task.MergeMethod != model.MergeMethodGomedia || task.HLSPackForm != model.HLSPackFormSingle ||
		task.CompressBitrateThreshold != 4096 || !task.EnableChunkedUpload {
		t.Fatalf("已补齐的任务被全局设置改写！快照优先规则被破坏: %+v", task)
	}
}

// TestSnapshotPriorityOverLegacyBackfill 验证：
// 新版任务（StartDownload 创建时写了快照）即使重试时全局设置已被改得完全相反，
// 也**绝对不能**被补齐函数修改，严格遵循创建时快照。
func TestSnapshotPriorityOverLegacyBackfill(t *testing.T) {
	ds, cleanup := newTestStack(t)
	defer cleanup()

	ds.UpdatePostDownloadConfig(true, model.MergeMethodFFmpeg, model.MuxModeCopy)
	ds.UpdateHLSPackConfig(true, false, model.HLSEncryptGenerated, "", model.HLSPackFormSingle)
	ds.UpdateUploadModeConfig(true)

	task, err := ds.StartDownload(model.DownloadRequest{
		URL:          "http://127.0.0.1:1/index.m3u8", // 会失败，但快照已写入
		OutputName:   "snap-prio",
		ThreadCount:  1,
		SavePath:     t.TempDir(),
		EnableWebDAV: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 把全局设置全部改成相反
	ds.UpdatePostDownloadConfig(false, model.MergeMethodGomedia, model.MuxModeH265)
	ds.UpdateHLSPackConfig(false, false, model.HLSEncryptGenerated, "", model.HLSPackFormMulti)
	ds.UpdateUploadModeConfig(false)

	// 直接调补齐函数，模拟重试入口的行为
	ds.backfillLegacyTaskSnapshot(task)

	if !task.MergeAfterDownload || task.MergeMethod != model.MergeMethodFFmpeg ||
		task.FFmpegMuxMode != model.MuxModeCopy || !task.HLSPackEnabled ||
		task.HLSPackForm != model.HLSPackFormSingle || !task.EnableChunkedUpload {
		t.Fatalf("新版任务快照被补齐函数覆盖！快照优先规则被破坏: %+v", task)
	}
}
