package service

import (
	"sync"
)

func (s *semaphore) resize(size int) {
	if size < 1 {
		size = 1
	}
	s.mu.Lock()
	s.size = size
	s.wakeLocked()
	s.mu.Unlock()
}

// wakeLocked 按 FIFO 唤醒所有当前可获得许可的等待者（调用方须持锁）

func (s *semaphore) wakeLocked() {
	for len(s.waiters) > 0 && s.cur < s.size {
		w := s.waiters[0]
		s.waiters = s.waiters[1:]
		s.cur++
		close(w)
	}
}

// tryAcquire 非阻塞尝试获取许可

func (s *semaphore) tryAcquire() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur < s.size {
		s.cur++
		return true
	}
	return false
}

// acquire 阻塞等待许可，stop 被关闭时放弃并返回 false（不占用许可）

func (s *semaphore) acquire(stop <-chan struct{}) bool {
	s.mu.Lock()
	if s.cur < s.size {
		s.cur++
		s.mu.Unlock()
		return true
	}
	wait := make(chan struct{})
	s.waiters = append(s.waiters, wait)
	s.mu.Unlock()

	select {
	case <-wait:
		return true
	case <-stop:
		s.mu.Lock()
		// 若已被唤醒（与 stop 竞争），保留许可视为成功
		select {
		case <-wait:
			s.mu.Unlock()
			return true
		default:
		}
		// 从等待队列移除自己
		for i, w := range s.waiters {
			if w == wait {
				s.waiters = append(s.waiters[:i], s.waiters[i+1:]...)
				break
			}
		}
		s.mu.Unlock()
		return false
	}
}

func (s *semaphore) release() {
	s.mu.Lock()
	if s.cur > 0 {
		s.cur--
	}
	s.wakeLocked()
	s.mu.Unlock()
}

type semaphore struct {
	mu      sync.Mutex
	cur     int // 当前占用数
	size    int // 容量上限
	waiters []chan struct{}
}

func newSemaphore(size int) *semaphore {
	if size < 1 {
		size = 1
	}
	return &semaphore{size: size}
}

// resize 调整容量。扩容时立即唤醒可能满足条件的等待者。

type taskControl struct {
	taskID   string
	paused   chan struct{}
	resumed  chan struct{}
	stopped  chan struct{}
	mu       sync.Mutex
	isPaused bool
}

// semaphore 动态容量信号量。容量可在运行时调整，正在等待/运行的任务
// 始终使用同一个对象——避免"替换 channel"导致新老任务计数脱节。

func isControlStopped(ctrl *taskControl) bool {
	if ctrl == nil {
		return false
	}
	select {
	case <-ctrl.stopped:
		return true
	default:
		return false
	}
}

// waitIfPaused 在各阶段切换处提供暂停关卡：若任务已暂停则在此阻塞，直到被恢复或停止。
// 这样可在下载/合并/压缩/分片/上传阶段之间严格停顿，避免暂停后任务仍冲入下一阶段。
// 注意：不打断正在运行的 FFmpeg 子进程（避免浪费已耗 CPU），转码结束后到达本关卡再挂起。
// 返回 true 表示可以继续，false 表示任务已被停止（应直接退出，由 defer 负责清理与释放）。

func waitIfPaused(ctrl *taskControl) bool {
	if ctrl == nil {
		return true
	}
	select {
	case <-ctrl.stopped:
		return false
	case <-ctrl.paused:
		select {
		case <-ctrl.resumed:
			return true
		case <-ctrl.stopped:
			return false
		}
	default:
		return true
	}
}

// yieldSemWhilePaused 在暂停期间让出当前阶段信号量，避免并发=1 时一个被暂停的任务
// 长期独占唯一槽位、冻结所有其它任务（表现为：存在暂停/等待等不同状态任务时，
// 只有最新任务在跑，其余全部卡在"等待队列"）。
//
// 调用方在调用时必须正持有 sem 的一个许可。返回值含义：
//   - 未暂停：原样返回 (true, true)，许可仍持有，可继续；
//   - 暂停后被恢复：让出期间许可已释放，恢复后重新获取成功，返回 (true, true)；
//   - 暂停等待/重新排队期间被停止：返回 (false, holding)。holding 表示此刻是否仍持有许可：
//     false 表示许可已释放且未重新获取，调用方的收尾 defer 绝不可再释放该信号量
//     （否则会错误释放其它任务的许可），应据此跳过对应 release。

func (ds *DownloaderService) yieldSemWhilePaused(ctrl *taskControl, sem *semaphore) (cont bool, holding bool) {
	holding = true
	if ctrl == nil {
		return true, holding
	}
	// 仅在确实已暂停时才让出
	select {
	case <-ctrl.stopped:
		return false, holding
	case <-ctrl.paused:
		// 暂停：先释放当前阶段许可，让其它任务能进入该阶段
		sem.release()
		holding = false
	default:
		return true, holding
	}

	ds.sendLog(ctrl.taskID, "info", "下载已暂停（已让出调度槽位）")

	// 等待恢复或停止；恢复后需要重新排队获取许可
	select {
	case <-ctrl.stopped:
		return false, holding // holding=false：许可已让出，调用方不得再释放
	case <-ctrl.resumed:
	}
	ds.sendLog(ctrl.taskID, "info", "下载已恢复，正在重新获取调度槽位...")
	if sem.acquire(ctrl.stopped) {
		holding = true
		return true, holding
	}
	// acquire 被停止打断且未拿到许可
	return false, holding
}

// humanBytes 将字节数格式化为人类可读的 B/KB/MB/GB 字符串（保留两位小数）
