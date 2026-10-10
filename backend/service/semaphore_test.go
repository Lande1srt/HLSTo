package service

import (
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"m3u8-downloader-web/storage"
	"m3u8-downloader-web/websocket"
)

func TestSemaphore_BasicAcquireRelease(t *testing.T) {
	s := newSemaphore(2)

	if !s.tryAcquire() {
		t.Fatal("首次获取应成功")
	}
	if !s.tryAcquire() {
		t.Fatal("第二次获取应成功（容量 2）")
	}
	if s.tryAcquire() {
		t.Fatal("超出容量不应获取成功")
	}
	s.release()
	if !s.tryAcquire() {
		t.Fatal("释放后应可再次获取")
	}
}

func TestSemaphore_InvalidSizeClamped(t *testing.T) {
	s := newSemaphore(0)
	if !s.tryAcquire() {
		t.Fatal("非法容量应兜底为 1 且可获取")
	}
	s.resize(-5)
	s.release()
	if !s.tryAcquire() {
		t.Fatal("resize 非法值后容量仍应至少为 1")
	}
}

// 关键回归：占满后排队的任务，在扩容（调高并发数）时必须被立即唤醒，
// 而不是卡在等待中（对应"数量设置还在冗余数值内但任务没触发"的 bug）
func TestSemaphore_ResizeWakesWaiters(t *testing.T) {
	s := newSemaphore(1)
	stop := make(chan struct{})

	if !s.acquire(stop) {
		t.Fatal("首次获取应成功")
	}

	got := make(chan struct{})
	go func() {
		if s.acquire(stop) {
			close(got)
		}
	}()

	// 等待者入队
	time.Sleep(50 * time.Millisecond)
	select {
	case <-got:
		t.Fatal("扩容前等待者不应被唤醒")
	default:
	}

	// 扩容到 2：等待者应立即获得许可
	s.resize(2)
	select {
	case <-got:
	case <-time.After(time.Second):
		t.Fatal("扩容后等待者未被唤醒")
	}
}

// 缩容不影响已占用许可；释放后按新容量限制
func TestSemaphore_ShrinkDoesNotKillHolders(t *testing.T) {
	s := newSemaphore(3)
	stop := make(chan struct{})
	for i := 0; i < 3; i++ {
		if !s.acquire(stop) {
			t.Fatalf("第 %d 个获取应成功", i)
		}
	}
	s.resize(1) // 缩容，但 3 个占用保留
	s.release()
	if s.tryAcquire() {
		t.Fatal("缩容后仍有 2 个占用，不应再获取")
	}
	s.release()
	s.release()
	if !s.tryAcquire() {
		t.Fatal("全部释放后应可按容量 1 获取")
	}
}

// 停止时等待者放弃且不占用许可，后续仍可正常调度
func TestSemaphore_StopWhileWaiting(t *testing.T) {
	s := newSemaphore(1)
	stop := make(chan struct{})
	if !s.acquire(stop) {
		t.Fatal("首次获取应成功")
	}

	var wg sync.WaitGroup
	wg.Add(1)
	ok := true
	go func() {
		defer wg.Done()
		ok = s.acquire(stop)
	}()
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
	if ok {
		t.Fatal("停止后 acquire 应返回 false")
	}

	s.release()
	// 放弃的等待者不应残留占用
	stop2 := make(chan struct{})
	if !s.acquire(stop2) {
		t.Fatal("等待者放弃后许可应可正常获取")
	}
}

// TestRandomHLSIV 验证随机 IV 为 32 字符小写十六进制，且两次生成不同
func TestRandomHLSIV(t *testing.T) {
	iv1, err := randomHLSIV()
	if err != nil {
		t.Fatalf("生成 IV 失败: %v", err)
	}
	if len(iv1) != 32 {
		t.Fatalf("IV 十六进制长度应为 32，实际 %d", len(iv1))
	}
	if _, err := hex.DecodeString(iv1); err != nil {
		t.Fatalf("IV 不是合法十六进制: %v", err)
	}
	iv2, _ := randomHLSIV()
	if iv1 == iv2 {
		t.Fatal("两次生成的随机 IV 不应相同")
	}
}

// TestHumanBytes 验证字节数到人类可读字符串的换算边界
func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{-1, "-"},
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.00 KB"},
		{1536, "1.50 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
	}
	for _, c := range cases {
		if got := humanBytes(c.n); got != c.want {
			t.Errorf("humanBytes(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestUpdateConcurrencyConfig_FiveStages 验证五阶段并发配置同时调整
// download/merge/compress/pack/upload 五个信号量：默认均为 1；resize 不替换对象
// （计数连续）；非法值兜底为 1。
func TestUpdateConcurrencyConfig_FiveStages(t *testing.T) {
	sqliteStorage, err := storage.NewSQLiteStorage()
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStorage.Close() })

	tm := NewTaskManager(sqliteStorage)
	t.Cleanup(tm.Close)

	ds := NewDownloaderService(tm, websocket.NewWebSocketManager())

	// 一次性读取五个阶段当前信号量对象
	loadSems := func() []*semaphore {
		return []*semaphore{
			ds.downloadSem.Load(),
			ds.mergeSem.Load(),
			ds.compressSem.Load(),
			ds.packSem.Load(),
			ds.uploadSem.Load(),
		}
	}
	semSize := func(s *semaphore) int {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.size
	}

	// 默认五个阶段容量均为 1
	sems := loadSems()
	for i, s := range sems {
		if size := semSize(s); size != 1 {
			t.Fatalf("阶段 %d 默认容量应为 1，实际 %d", i, size)
		}
	}

	// 调整为 2/3/4/5/6
	ds.UpdateConcurrencyConfig(2, 3, 4, 5, 6, false)
	want := []int{2, 3, 4, 5, 6}
	for i, s := range sems {
		if size := semSize(s); size != want[i] {
			t.Fatalf("阶段 %d 调整后容量应为 %d，实际 %d", i, want[i], size)
		}
	}
	// CPU 阶段并发计数同步
	if ds.compressConcurrency.Load() != 4 || ds.packConcurrency.Load() != 5 {
		t.Fatalf("压缩/分片并发计数未同步: compress=%d pack=%d",
			ds.compressConcurrency.Load(), ds.packConcurrency.Load())
	}

	// 非法值（0/负数）兜底为 1，且信号量仍是同一对象（避免计数脱节）
	ds.UpdateConcurrencyConfig(0, -1, 0, -2, 0, false)
	current := loadSems()
	for i, s := range current {
		if s != sems[i] {
			t.Fatalf("阶段 %d 信号量对象被替换，计数可能脱节", i)
		}
		if size := semSize(s); size != 1 {
			t.Fatalf("阶段 %d 非法值应兜底为 1，实际 %d", i, size)
		}
	}
}
