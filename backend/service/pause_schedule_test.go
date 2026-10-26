package service

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m3u8-downloader-web/model"
	"m3u8-downloader-web/websocket"
)

// 构造一个可通过 gate 通道控制分片放行节奏的慢 m3u8 源，
// 用于制造"任务 A 暂停时任务 B 仍能下载"的并发调度场景。
func newGatedM3U8Server(t *testing.T, segCount int) (*httptest.Server, string, chan struct{}) {
	t.Helper()
	gate := make(chan struct{})
	var inflight int64
	mux := http.NewServeMux()

	mux.HandleFunc("/playlist.m3u8", func(w http.ResponseWriter, r *http.Request) {
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:0\n")
		for i := 0; i < segCount; i++ {
			b.WriteString("#EXTINF:6.0,\n")
			b.WriteString(fmt.Sprintf("/seg.ts?i=%d\n", i))
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(b.String()))
	})

	mux.HandleFunc("/seg.ts", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&inflight, 1)
		defer atomic.AddInt64(&inflight, -1)
		// 第一个分片请求后阻塞在 gate，直到测试放行，便于精确制造暂停时刻
		<-gate
		_, _ = w.Write(make([]byte, 1024))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, srv.URL + "/playlist.m3u8", gate
}

// TestPausedTaskDoesNotBlockOthers 验证：下载并发=1 时，暂停任务 A 必须让出下载槽位，
// 任务 B 仍能完成；恢复 A 后 A 也能完成。修复前 A 会永久独占唯一许可导致 B 卡死。
func TestPausedTaskDoesNotBlockOthers(t *testing.T) {
	const segCount = 6
	srv, playlistURL, gate := newGatedM3U8Server(t, segCount)
	defer srv.Close()

	tm := NewTaskManager(nil)
	defer tm.Close()
	wm := websocket.NewWebSocketManager()
	ds := NewDownloaderService(tm, wm)
	// 下载并发=1（默认即 1，这里显式设置），关闭合并/压缩/分片/上传
	ds.downloadConcurrency.Store(1)
	ds.downloadSem.Load().resize(1)
	ds.UpdatePostDownloadConfig(false, model.MergeMethodAuto, model.MuxModeCopy)
	saveDir := t.TempDir()

	// 任务 A 启动；其分片请求会阻塞在 gate
	taskA, err := ds.StartDownload(model.DownloadRequest{
		URL: playlistURL, OutputName: "taskA", ThreadCount: 1, SavePath: saveDir,
	})
	if err != nil {
		t.Fatalf("start A: %v", err)
	}

	// 等待 A 进入 downloading 且持有唯一下载槽位
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tt, ok := tm.GetTask(taskA.ID); ok && tt.Status == model.StatusDownloading {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 暂停 A：此时 A 的 goroutine 停在分片请求（gate），暂停信号在下一个关卡生效。
	// 先放行 gate 让在途请求返回，A 到达暂停关卡后应让出槽位。
	ds.PauseDownload(taskA.ID)
	close(gate)

	// 等待 A 进入 paused
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if tt, ok := tm.GetTask(taskA.ID); ok && tt.Status == model.StatusPaused {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if tt, _ := tm.GetTask(taskA.ID); tt.Status != model.StatusPaused {
		t.Fatalf("任务 A 未进入 paused，当前=%s", tt.Status)
	}

	// 任务 B 启动：并发=1，若 A 不让出槽位，B 将永远卡在 pending。给 B 充分时间完成。
	taskB, err := ds.StartDownload(model.DownloadRequest{
		URL: playlistURL, OutputName: "taskB", ThreadCount: 1, SavePath: saveDir,
	})
	if err != nil {
		t.Fatalf("start B: %v", err)
	}

	deadline = time.Now().Add(10 * time.Second)
	bDone := false
	for time.Now().Before(deadline) {
		if tt, ok := tm.GetTask(taskB.ID); ok && tt.Status == model.StatusCompleted {
			bDone = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !bDone {
		tt, _ := tm.GetTask(taskB.ID)
		t.Fatalf("任务 A 暂停期间，任务 B 被阻塞无法完成，B 状态=%s（调度槽位被暂停任务独占）", tt.Status)
	}

	// 恢复 A，A 应重新获取槽位并完成
	ds.ResumeDownload(taskA.ID)
	deadline = time.Now().Add(10 * time.Second)
	aDone := false
	for time.Now().Before(deadline) {
		if tt, ok := tm.GetTask(taskA.ID); ok && tt.Status == model.StatusCompleted {
			aDone = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !aDone {
		tt, _ := tm.GetTask(taskA.ID)
		t.Fatalf("恢复后任务 A 未能完成，状态=%s 错误=%s", tt.Status, tt.Error)
	}
}
