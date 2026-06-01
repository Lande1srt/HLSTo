package service

import (
	"sync"

	"m3u8-downloader-web/model"
	"m3u8-downloader-web/storage"
)

type TaskManager struct {
	tasks   map[string]*model.Task
	mu      sync.RWMutex
	storage *storage.SQLiteStorage
}

func NewTaskManager(storage *storage.SQLiteStorage) *TaskManager {
	tm := &TaskManager{
		tasks:   make(map[string]*model.Task),
		storage: storage,
	}

	tm.loadTasksFromDB()

	return tm
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

func (tm *TaskManager) AddTask(task *model.Task) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.tasks[task.ID] = task

	if tm.storage != nil {
		go tm.storage.AddTask(task)
	}
}

func (tm *TaskManager) GetTask(id string) (*model.Task, bool) {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	task, exists := tm.tasks[id]
	return task, exists
}

func (tm *TaskManager) UpdateTask(task *model.Task) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	tm.tasks[task.ID] = task

	if tm.storage != nil {
		go tm.storage.UpdateTask(task)
	}
}

func (tm *TaskManager) DeleteTask(id string) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	delete(tm.tasks, id)

	if tm.storage != nil {
		go tm.storage.DeleteTask(id)
	}
}

func (tm *TaskManager) ListTasks() []*model.Task {
	tm.mu.RLock()
	defer tm.mu.RUnlock()
	tasks := make([]*model.Task, 0, len(tm.tasks))
	for _, task := range tm.tasks {
		tasks = append(tasks, task)
	}
	return tasks
}

func (tm *TaskManager) UpdateProgress(id string, progress float64, speed string, downloaded int) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if task, exists := tm.tasks[id]; exists {
		task.Progress = progress
		task.Speed = speed
		task.DownloadedSegments = downloaded

		if tm.storage != nil {
			go tm.storage.UpdateTask(task)
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

		if tm.storage != nil {
			go tm.storage.UpdateTask(task)
		}
	}
}

func (tm *TaskManager) GetTaskLogs(taskID string) ([]*model.LogEntry, error) {
	if tm.storage == nil {
		return nil, nil
	}
	return tm.storage.GetLogs(taskID)
}

func (tm *TaskManager) AddLog(taskID, level, message string) {
	// 按照用户要求：主页日志不写入数据库，仅在内存中通过 WebSocket 实时分发
	// 如果未来需要历史记录，可以在此处重新启用
	/*
		if tm.storage != nil {
			go tm.storage.AddLog(taskID, level, message)
		}
	*/
}
