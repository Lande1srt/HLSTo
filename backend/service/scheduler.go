package service

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	storage    *storage.SQLiteStorage
	configPath string
	config     CleanupConfig
	stopChan   chan struct{}
}

func NewSchedulerService(storage *storage.SQLiteStorage) *SchedulerService {
	pwd, _ := os.Getwd()
	configPath := filepath.Join(pwd, "cleanup_config.json")
	
	s := &SchedulerService{
		storage:    storage,
		configPath: configPath,
		stopChan:   make(chan struct{}),
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
			// 不需要立即保存到文件，因为这些是内存中的计时状态

			timer := time.NewTimer(duration)
			select {
			case <-timer.C:
				if s.config.Enabled {
					s.performCleanup()
					s.config.LastRun = time.Now()
				}
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

func (s *SchedulerService) performCleanup() {
	log.Println("[Scheduler] 正在执行定时自动清理任务...")
	
	pwd, err := os.Getwd()
	if err != nil {
		return
	}

	dirsToClear := []string{pwd}
	settings, err := s.storage.GetSettings()
	if err == nil && settings.DefaultSavePath != "" && settings.DefaultSavePath != pwd {
		dirsToClear = append(dirsToClear, settings.DefaultSavePath)
	}

	count := 0
	for _, dir := range dirsToClear {
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() && strings.HasPrefix(f.Name(), "download_") {
				err := os.RemoveAll(filepath.Join(dir, f.Name()))
				if err == nil {
					count++
				}
			}
		}
	}
	log.Printf("[Scheduler] 自动清理完成，已移除 %d 个缓存文件夹\n", count)
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
}

func (s *SchedulerService) GetConfig() CleanupConfig {
	return s.config
}
