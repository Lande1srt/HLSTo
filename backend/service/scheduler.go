package service

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m3u8-downloader-web/model"
	"m3u8-downloader-web/storage"
)

type CleanupConfig struct {
	Enabled  bool      `json:"enabled"`
	Interval int       `json:"interval"` // 间隔数值
	Unit     string    `json:"unit"`     // 间隔单位: "minute", "hour", "day"
	LastRun  time.Time `json:"lastRun"`  // 上次运行时间
	NextRun  time.Time `json:"nextRun"`  // 下次运行时间
}

type SchedulerService struct {
	storage     *storage.SQLiteStorage
	taskManager *TaskManager
	configPath  string
	config      CleanupConfig
	stopChan    chan struct{}
	timerChan   chan struct{} // 用于通知重新计算计时器
}

// activeTaskStatuses 清理时需要避让的活跃任务状态
var activeTaskStatuses = map[model.TaskStatus]bool{
	model.StatusPending:     true,
	model.StatusDownloading: true,
	model.StatusMerging:     true,
	model.StatusPaused:      true,
	model.StatusUploading:   true,
}

func NewSchedulerService(storage *storage.SQLiteStorage, taskManager *TaskManager) *SchedulerService {
	pwd, _ := os.Getwd()
	configPath := filepath.Join(pwd, "cleanup_config.json")

	s := &SchedulerService{
		storage:     storage,
		taskManager: taskManager,
		configPath:  configPath,
		stopChan:    make(chan struct{}),
		timerChan:   make(chan struct{}, 1), // 带缓冲，避免发送阻塞
	}

	s.loadConfig()
	return s
}

func (s *SchedulerService) loadConfig() {
	// 默认配置
	s.config = CleanupConfig{
		Enabled:  false,
		Interval: 1,
		Unit:     "day",
	}

	data, err := os.ReadFile(s.configPath)
	if err == nil {
		json.Unmarshal(data, &s.config)
	} else {
		s.saveConfig() // 如果不存在则创建默认配置
	}
}

func (s *SchedulerService) saveConfig() {
	data, _ := json.MarshalIndent(s.config, "", "  ")
	os.WriteFile(s.configPath, data, 0644)
}

func (s *SchedulerService) Start() {
	go func() {
		log.Printf("[Scheduler] 自动清理计划任务已启动，规则: 每 %d %s 清理一次 (启用: %v)\n",
			s.config.Interval, s.config.Unit, s.config.Enabled)

		for {
			duration := s.calculateDuration()
			s.config.NextRun = time.Now().Add(duration)

			timer := time.NewTimer(duration)
			select {
			case <-timer.C:
				if s.config.Enabled {
					if s.performCleanup() {
						// 只有实际执行了清理才更新 LastRun，否则下次定时继续尝试
						s.config.LastRun = time.Now()
						s.saveConfig()
					}
				}
			case <-s.timerChan:
				// 配置更新，重新计算计时器
				timer.Stop()
				log.Printf("[Scheduler] 配置已更新，重新计算计时器: 每 %d %s\n",
					s.config.Interval, s.config.Unit)
				continue // 重新开始循环，使用新配置
			case <-s.stopChan:
				timer.Stop()
				return
			}
		}
	}()
}

func (s *SchedulerService) calculateDuration() time.Duration {
	var duration time.Duration
	switch s.config.Unit {
	case "minute":
		duration = time.Duration(s.config.Interval) * time.Minute
	case "hour":
		duration = time.Duration(s.config.Interval) * time.Hour
	case "day":
		duration = time.Duration(s.config.Interval) * 24 * time.Hour
	default:
		duration = 24 * time.Hour
	}
	return duration
}

func (s *SchedulerService) Stop() {
	close(s.stopChan)
}

// hasActiveTasks 检查是否有正在下载/合并/上传/暂停的活跃任务
func (s *SchedulerService) hasActiveTasks() bool {
	if s.taskManager == nil {
		return false
	}
	for _, t := range s.taskManager.ListTasks() {
		if activeTaskStatuses[t.Status] {
			return true
		}
	}
	return false
}

// protectedWorkDirs 返回所有任务当前工作目录的绝对路径集合。
// 只要 download_ 目录仍被某个任务引用（含 failed/completed，随时可能被重试/强制合并复用），
// 就不得清理——这从路径归属上杜绝"清理与重试并发误删"，不依赖检查时序。
func (s *SchedulerService) protectedWorkDirs() map[string]bool {
	protected := make(map[string]bool)
	if s.taskManager == nil {
		return protected
	}
	for _, t := range s.taskManager.ListTasks() {
		wd := strings.TrimSpace(t.WorkDir)
		if wd == "" {
			continue
		}
		if abs, err := filepath.Abs(wd); err == nil {
			protected[abs] = true
		}
	}
	return protected
}

// performCleanup 返回 true 表示实际执行了清理；false 表示因活跃任务而跳过
func (s *SchedulerService) performCleanup() bool {
	// 优先避让：有活跃任务则跳过本轮
	if s.hasActiveTasks() {
		log.Println("[Scheduler] 检测到活跃任务（下载/合并/上传/暂停中），跳过本轮清理，下次定时继续")
		return false
	}

	log.Println("[Scheduler] 正在执行定时自动清理任务...")

	pwd, err := os.Getwd()
	if err != nil {
		return false
	}

	dirsToClear := []string{pwd}
	settings, err := s.storage.GetSettings()
	if err == nil && settings.DefaultSavePath != "" && settings.DefaultSavePath != pwd {
		dirsToClear = append(dirsToClear, settings.DefaultSavePath)
	}

	count := 0
	protected := s.protectedWorkDirs()
	for _, dir := range dirsToClear {
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() && strings.HasPrefix(f.Name(), "download_") {
				full := filepath.Join(dir, f.Name())
				if abs, err := filepath.Abs(full); err == nil && protected[abs] {
					log.Printf("[Scheduler] 跳过仍被任务引用的目录: %s\n", full)
					continue
				}
				err := os.RemoveAll(full)
				if err != nil {
					log.Printf("[Scheduler] 删除缓存目录失败: %v\n", err)
				} else {
					count++
				}
			}
		}
	}
	log.Printf("[Scheduler] 自动清理完成，已移除 %d 个缓存文件夹\n", count)
	return true
}

// 提供给外部更新配置的方法
func (s *SchedulerService) UpdateConfig(newConfig CleanupConfig) {
	s.config.Enabled = newConfig.Enabled
	s.config.Interval = newConfig.Interval
	s.config.Unit = newConfig.Unit
	// 重启计时逻辑通过重新计算 NextRun 实现
	s.config.NextRun = time.Now().Add(s.calculateDuration())
	s.saveConfig()
	log.Printf("[Scheduler] 自动清理规则已更新: 每 %d %s (启用: %v)\n",
		s.config.Interval, s.config.Unit, s.config.Enabled)

	// 通知计时器重新计算
	select {
	case s.timerChan <- struct{}{}:
		// 成功发送信号
	default:
		// 如果通道已满（说明之前的信号还没处理），不阻塞
	}
}

func (s *SchedulerService) GetConfig() CleanupConfig {
	return s.config
}

// ClearCache 手动清理按钮 — 同样避让活跃任务
func (s *SchedulerService) ClearCache() int {
	if s.hasActiveTasks() {
		log.Println("[Scheduler] 有活跃任务正在处理，手动清理已跳过")
		return 0
	}

	pwd, err := os.Getwd()
	if err != nil {
		return 0
	}

	dirsToClear := []string{pwd}
	settings, err := s.storage.GetSettings()
	if err == nil && settings.DefaultSavePath != "" && settings.DefaultSavePath != pwd {
		dirsToClear = append(dirsToClear, settings.DefaultSavePath)
	}

	count := 0
	protected := s.protectedWorkDirs()
	for _, dir := range dirsToClear {
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() && strings.HasPrefix(f.Name(), "download_") {
				full := filepath.Join(dir, f.Name())
				if abs, err := filepath.Abs(full); err == nil && protected[abs] {
					log.Printf("[Scheduler] 跳过仍被任务引用的目录: %s\n", full)
					continue
				}
				err := os.RemoveAll(full)
				if err != nil {
					log.Printf("[Scheduler] 删除缓存目录失败: %v\n", err)
				} else {
					count++
				}
			}
		}
	}
	return count
}
