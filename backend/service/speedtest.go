package service

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 测速默认参数
const (
	speedTestDefaultSizeMB = 10 // 生成随机流的默认大小
	speedTestRemotePrefix  = "hlsto_speedtest"
	speedTestLogFile       = "speedtest_last.json" // 仅持久化最后一次
)

// SpeedTestStatus 测速状态
const (
	SpeedTestStateRunning = "running"
	SpeedTestStateDone    = "done"    // 上传自然结束（已清理远端文件）
	SpeedTestStateStopped = "stopped" // 用户手动中断（保留远端文件，供排查）
	SpeedTestStateFailed  = "failed"
)

// SpeedTestLog 一次测速的完整记录（仅最后一次被持久化）
type SpeedTestLog struct {
	State       string   `json:"state"`
	SizeMB      int      `json:"sizeMB"`
	WebDAVURL   string   `json:"webdavUrl"`
	StartedAt   string   `json:"startedAt"`
	FinishedAt  string   `json:"finishedAt,omitempty"`
	PerSecond   []string `json:"perSecond"`
	AverageKbps float64  `json:"averageKbps"`
	RemotePath  string   `json:"remotePath,omitempty"`
	Error       string   `json:"error,omitempty"`

	avgAccum int64 // 运行期已上传字节数（不参与序列化展示）
}

// SpeedTestService 管理 WebDAV 上传测速；同一时刻只允许一个测速任务
type SpeedTestService struct {
	mu      sync.Mutex
	running bool
	stop    chan struct{}

	logMu sync.RWMutex
	log   *SpeedTestLog
}

// NewSpeedTestService 创建测速服务，并尝试加载上一次持久化日志
func NewSpeedTestService() *SpeedTestService {
	s := &SpeedTestService{}
	if l, err := loadSpeedTestLog(); err == nil && l != nil {
		s.log = l
	}
	return s
}

// Snapshot 返回当前测速日志的深拷贝（无日志时返回 nil）
func (s *SpeedTestService) Snapshot() *SpeedTestLog {
	s.logMu.RLock()
	defer s.logMu.RUnlock()
	if s.log == nil {
		return nil
	}
	cp := *s.log
	cp.PerSecond = append([]string(nil), s.log.PerSecond...)
	return &cp
}

// IsRunning 是否正在测速
func (s *SpeedTestService) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Start 启动一次测速。sizeMB<=0 时取默认 100MB。
func (s *SpeedTestService) Start(cfg WebDAVConfig, sizeMB int) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return fmt.Errorf("已有测速任务正在运行")
	}
	s.running = true
	s.stop = make(chan struct{})
	stop := s.stop
	s.mu.Unlock()

	if sizeMB < 1 {
		sizeMB = speedTestDefaultSizeMB
	}

	go s.run(cfg, sizeMB, stop)
	return nil
}

// Stop 手动中断测速
func (s *SpeedTestService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running && s.stop != nil {
		close(s.stop)
	}
}

func (s *SpeedTestService) run(cfg WebDAVConfig, sizeMB int, stop chan struct{}) {
	defer func() {
		s.mu.Lock()
		s.running = false
		s.stop = nil
		s.mu.Unlock()
	}()

	logEntry := &SpeedTestLog{
		State:     SpeedTestStateRunning,
		SizeMB:    sizeMB,
		WebDAVURL: cfg.URL,
		StartedAt: time.Now().Format(time.RFC3339),
		PerSecond: []string{},
	}
	s.setLog(logEntry)
	s.appendLine("info", fmt.Sprintf("开始准备 %dMB 随机测试文件...", sizeMB))

	// 1. 生成随机流临时文件
	tmpFile, err := generateRandomFile(sizeMB)
	if err != nil {
		s.finish(SpeedTestStateFailed, fmt.Sprintf("生成测试文件失败: %v", err))
		return
	}
	defer os.Remove(tmpFile)
	s.appendLine("info", "测试文件已生成，开始连接 WebDAV...")

	// 2. 上传（复用正式上传通道，结果有代表性）
	cfg.Enabled = true
	webdav := NewWebDAVService(cfg)
	if err := webdav.TestConnection(); err != nil {
		s.finish(SpeedTestStateFailed, fmt.Sprintf("WebDAV 连接失败: %v", err))
		return
	}

	remoteName := fmt.Sprintf("%s_%d.bin", speedTestRemotePrefix, time.Now().UnixNano())
	remotePath := webdav.RemoteFilePath(remoteName)
	s.logMu.Lock()
	logEntry.RemotePath = remotePath
	s.logMu.Unlock()

	totalBytes := int64(sizeMB) * 1024 * 1024
	start := time.Now()
	var lastBytes int64
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	// 每秒速率采集：基于两次回调间的增量，停止时退出
	var perSecKbps []float64
	progDone := make(chan struct{})
	go func() {
		defer close(progDone)
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				s.logMu.RLock()
				cur := logEntry.avgAccum
				s.logMu.RUnlock()
				delta := cur - lastBytes
				lastBytes = cur
				kbps := float64(delta) / 1024
				perSecKbps = append(perSecKbps, kbps)
				elapsed := int(time.Since(start).Seconds())
				line := fmt.Sprintf("[%2ds] %s", elapsed, humanRate(kbps))
				s.appendPerSecond(line)
			}
		}
	}()

	upErr := webdav.UploadFile(tmpFile, remoteName, stop, func(downloaded, _ int64, _ string) {
		s.logMu.Lock()
		logEntry.avgAccum = downloaded
		s.logMu.Unlock()
	})
	<-progDone

	if upErr != nil {
		if isStopRequested(stop) || strings.Contains(upErr.Error(), "cancelled") {
			// 手动中断：按需求保留远端测试文件，不删除
			s.finish(SpeedTestStateStopped, "用户手动中断，远端测试文件已保留")
			return
		}
		s.finish(SpeedTestStateFailed, fmt.Sprintf("上传失败: %v", upErr))
		return
	}

	// 3. 自然结束：统计平均速率并删除远端测试文件
	elapsed := time.Since(start).Seconds()
	avgKbps := 0.0
	if elapsed > 0 {
		avgKbps = float64(totalBytes) / 1024 / elapsed
	}
	s.logMu.Lock()
	logEntry.AverageKbps = avgKbps
	s.logMu.Unlock()
	s.appendPerSecond(fmt.Sprintf("平均速率 %s", humanRate(avgKbps)))

	if err := webdav.Remove(remotePath); err != nil {
		s.appendLine("warn", fmt.Sprintf("远端测试文件删除失败，请手动清理: %v", err))
	} else {
		s.appendLine("info", "远端测试文件已自动删除")
	}
	s.finish(SpeedTestStateDone, "")
}

// finish 写入终态并持久化
func (s *SpeedTestService) finish(state, errMsg string) {
	s.logMu.Lock()
	if s.log != nil {
		s.log.State = state
		s.log.FinishedAt = time.Now().Format(time.RFC3339)
		if errMsg != "" {
			if state == SpeedTestStateFailed {
				s.log.Error = errMsg
			}
		}
		cp := *s.log
		cp.PerSecond = append([]string(nil), s.log.PerSecond...)
	}
	s.logMu.Unlock()

	if errMsg != "" {
		if state == SpeedTestStateFailed {
			s.appendLine("error", errMsg)
		} else {
			s.appendLine("info", errMsg)
		}
	}
	s.persist()
}

func (s *SpeedTestService) setLog(l *SpeedTestLog) {
	s.logMu.Lock()
	s.log = l
	s.logMu.Unlock()
}

func (s *SpeedTestService) appendLine(level, msg string) {
	line := fmt.Sprintf("%s [%s] %s",
		time.Now().Format("15:04:05"), level, msg)
	s.appendPerSecond(line)
}

func (s *SpeedTestService) appendPerSecond(line string) {
	s.logMu.Lock()
	if s.log != nil {
		s.log.PerSecond = append(s.log.PerSecond, line)
	}
	s.logMu.Unlock()
	s.persist()
}

func (s *SpeedTestService) persist() {
	s.logMu.RLock()
	cp := (*SpeedTestLog)(nil)
	if s.log != nil {
		c := *s.log
		c.PerSecond = append([]string(nil), s.log.PerSecond...)
		cp = &c
	}
	s.logMu.RUnlock()
	if cp == nil {
		return
	}
	if err := saveSpeedTestLog(cp); err != nil {
		fmt.Printf("[SpeedTest] 持久化日志失败: %v\n", err)
	}
}

// generateRandomFile 生成指定 MB 的随机数据文件，返回路径
func generateRandomFile(sizeMB int) (string, error) {
	f, err := os.CreateTemp("", "hlsto-speedtest-*.bin")
	if err != nil {
		return "", err
	}

	buf := make([]byte, 1024*1024)
	total := sizeMB
	for i := 0; i < total; i++ {
		if _, err := rand.Read(buf); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", err
		}
		if _, err := f.Write(buf); err != nil {
			f.Close()
			os.Remove(f.Name())
			return "", err
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func isStopRequested(stop chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}

func humanRate(kbps float64) string {
	if kbps >= 1024 {
		return fmt.Sprintf("%.2f MB/s", kbps/1024)
	}
	return fmt.Sprintf("%.0f KB/s", kbps)
}

// loadSpeedTestLog 从工作目录读取最后一次测速日志
func loadSpeedTestLog() (*SpeedTestLog, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	p := filepath.Join(pwd, speedTestLogFile)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var l SpeedTestLog
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

// saveSpeedTestLog 原子写入最后一次测速日志
func saveSpeedTestLog(l *SpeedTestLog) error {
	pwd, err := os.Getwd()
	if err != nil {
		return err
	}
	final := filepath.Join(pwd, speedTestLogFile)
	tmp := final + ".tmp"
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
