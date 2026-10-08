package service

import (
	"log"
	"os"
	"sort"
	"sync"
	"time"

	"m3u8-downloader-web/model"
	"m3u8-downloader-web/storage"
)

// storageOp 持久化任务，统一由 runStorageLoop 单 goroutine 顺序消费
type storageOp struct {
	kind    string      // "add" / "update" / "delete" / "log"
	task    *model.Task // add / update 时使用
	id      string      // delete / log 时使用（log 时为 taskID）
	level   string      // log 时使用
	message string      // log 时使用
}

const storageChSize = 200

type TaskManager struct {
	tasks      map[string]*model.Task
	lastUpdate map[string]time.Time
	lastLog    map[string]time.Time // debug 级高频日志节流：taskID -> 最近落盘时间
	mu         sync.RWMutex

	storage   *storage.SQLiteStorage
	storageCh chan storageOp
	wg        sync.WaitGroup
	closed    bool
	closeMu   sync.Mutex
}

func NewTaskManager(storage *storage.SQLiteStorage) *TaskManager {
	tm := &TaskManager{
		tasks:      make(map[string]*model.Task),
		lastUpdate: make(map[string]time.Time),
		lastLog:    make(map[string]time.Time),
		storage:    storage,
		storageCh:  make(chan storageOp, storageChSize),
	}

	tm.loadTasksFromDB()

	// 启动后台持久化循环
	tm.wg.Add(1)
	go tm.runStorageLoop()

	return tm
}

// runStorageLoop 单 goroutine 顺序消费持久化任务，避免 SQLite 并发冲突
func (tm *TaskManager) runStorageLoop() {
	defer tm.wg.Done()
	for op := range tm.storageCh {
		if tm.storage == nil {
			continue
		}
		switch op.kind {
		case "add":
			if err := tm.storage.AddTask(op.task); err != nil {
				log.Printf("[TaskManager] AddTask 持久化失败: %v", err)
			}
		case "update":
			if err := tm.storage.UpdateTask(op.task); err != nil {
				log.Printf("[TaskManager] UpdateTask 持久化失败: %v", err)
			}
		case "delete":
			if err := tm.storage.DeleteTask(op.id); err != nil {
				log.Printf("[TaskManager] DeleteTask 持久化失败: %v", err)
			}
		case "log":
			if err := tm.storage.AddLog(op.id, op.level, op.message); err != nil {
				log.Printf("[TaskManager] AddLog 持久化失败: %v", err)
			}
		}
	}
}

// Close 关闭持久化通道并等待已入队任务处理完成
func (tm *TaskManager) Close() {
	tm.closeMu.Lock()
	if tm.closed {
		tm.closeMu.Unlock()
		return
	}
	tm.closed = true
	tm.closeMu.Unlock()

	close(tm.storageCh)
	tm.wg.Wait()
	log.Println("[TaskManager] 持久化循环已优雅退出")
}

// submitStorage 提交持久化任务到通道（通道满时 drop 并打日志，避免业务阻塞）
func (tm *TaskManager) submitStorage(op storageOp) {
	if tm.storage == nil {
		return
	}
	tm.closeMu.Lock()
	if tm.closed {
		tm.closeMu.Unlock()
		return
	}
	tm.closeMu.Unlock()

	select {
	case tm.storageCh <- op:
	default:
		// 通道满了，放弃本次持久化并记录日志
		log.Printf("[TaskManager] 持久化通道已满，丢弃 op=%s taskID=%s", op.kind, op.id)
	}
}

func (tm *TaskManager) loadTasksFromDB() {
	tasks, err := tm.storage.GetAllTasks()
	if err != nil {
		return
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	for _, task := range tasks {
		tm.tasks[task.ID] = task
	}
}

// cloneTask 复制任务对象，返回持久化专用私有副本；调用方须已持有 tm.mu。
// 持久化 goroutine 在锁外读取字段，必须传入副本，否则与业务写并发构成 data race。
func cloneTask(t *model.Task) *model.Task {
	cp := *t
	return &cp
}

func (tm *TaskManager) AddTask(task *model.Task) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.tasks[task.ID] = task

	tm.submitStorage(storageOp{kind: "add", task: cloneTask(task)})
}

func (tm *TaskManager) GetTask(id string) (*model.Task, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	task, exists := tm.tasks[id]
	return task, exists
}

// UpdateTask 提交持久化。注意：调用方应先在外部修改 task 指针的字段（map 内与传入的是同一对象），
// 再调用本方法触发 DB 写入。入队时在锁保护下复制快照，持久化 goroutine 只读副本。
func (tm *TaskManager) UpdateTask(task *model.Task) {
	tm.mu.RLock()
	cp := cloneTask(task)
	tm.mu.RUnlock()
	tm.submitStorage(storageOp{kind: "update", task: cp})
}

func (tm *TaskManager) DeleteTask(id string) {
	tm.mu.Lock()
	// 删除任务即删除其加密密钥（密钥独立持久存放于 keys/{id}.key，仅此入口主动删除）
	if task, ok := tm.tasks[id]; ok && task.KeyPath != "" {
		// 兼容历史数据：旧 KeyPath 可能指向 download_* 内的 enc.key
		if err := os.Remove(task.KeyPath); err != nil && !os.IsNotExist(err) {
			log.Printf("[TaskManager] 删除密钥文件失败 taskID=%s: %v\n", id, err)
		}
	}
	delete(tm.tasks, id)
	tm.mu.Unlock()

	// 密钥库：删 keys/{id}.key 并移除 index.json 条目（不存在为无操作）
	if err := defaultKeyStore.remove(id); err != nil {
		log.Printf("[TaskManager] 从密钥库移除失败 taskID=%s: %v\n", id, err)
	}

	tm.submitStorage(storageOp{kind: "delete", id: id})
}

func (tm *TaskManager) ListTasks() []*model.Task {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	tasks := make([]*model.Task, 0, len(tm.tasks))
	for _, task := range tm.tasks {
		tasks = append(tasks, task)
	}

	// 默认按创建时间倒序排列（新任务在前），确保"队列顺序"
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.After(tasks[j].CreatedAt)
	})

	return tasks
}

func (tm *TaskManager) UpdateProgress(id string, progress float64, speed string, downloaded, total int) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if task, exists := tm.tasks[id]; exists {
		task.Progress = progress
		task.Speed = speed
		task.DownloadedSegments = downloaded
		if total > 0 {
			task.TotalSegments = total
		}

		if tm.storage != nil {
			// 节流：每 2 秒最多更新一次数据库进度，或者是进度达到 100%
			now := time.Now()
			last, ok := tm.lastUpdate[id]
			if !ok || now.Sub(last) >= 2*time.Second || progress >= 100 {
				tm.lastUpdate[id] = now
				tm.submitStorage(storageOp{kind: "update", task: cloneTask(task)})
			}
		}
	}
}

func (tm *TaskManager) UpdateStatus(id string, status model.TaskStatus, message string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if task, exists := tm.tasks[id]; exists {
		task.Status = status
		if message != "" {
			task.Error = message
		}

		tm.submitStorage(storageOp{kind: "update", task: cloneTask(task)})
	}
}

func (tm *TaskManager) GetTaskLogs(taskID string) ([]*model.LogEntry, error) {
	if tm.storage == nil {
		return nil, nil
	}
	return tm.storage.GetLogs(taskID)
}

// GetTaskLogsFiltered 按级别与条数上限查询任务日志
func (tm *TaskManager) GetTaskLogsFiltered(taskID, level string, limit int) ([]*model.LogEntry, error) {
	if tm.storage == nil {
		return nil, nil
	}
	return tm.storage.GetLogsFiltered(taskID, level, limit)
}

// AddLog 异步持久化任务日志（单 goroutine 顺序消费，不阻塞下载主流程）。
// info/warn/error 直通；debug 为 FFmpeg 高频处理进度，每任务每 2 秒最多落盘一条。
func (tm *TaskManager) AddLog(taskID, level, message string) {
	if tm.storage == nil {
		return
	}

	now := time.Now()
	tm.mu.Lock()
	if level == "debug" {
		if last, ok := tm.lastLog[taskID]; ok && now.Sub(last) < 2*time.Second {
			tm.mu.Unlock()
			return
		}
	}
	tm.lastLog[taskID] = now
	tm.mu.Unlock()

	tm.submitStorage(storageOp{kind: "log", id: taskID, level: level, message: message})
}
