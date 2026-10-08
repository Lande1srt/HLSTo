package service

import (
	"sync"
	"testing"
	"time"
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
