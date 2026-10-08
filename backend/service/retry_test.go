package service

import (
	"os"
	"os/exec"
	"path/filepath"
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
		WorkDir: workDir,
	}
	ds.taskManager.AddTask(failed)

	// gomedia 路径解析真实 TS
	ds.UpdatePostDownloadConfig(true, model.MergeMethodGomedia, model.MuxModeCopy)

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
}
