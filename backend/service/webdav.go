package service

import (
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"
	"time"

	"github.com/studio-b12/gowebdav"
)

type WebDAVConfig struct {
	Enabled   bool   `json:"enabled"`
	URL       string `json:"url"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	RemoteDir string `json:"remoteDir"`
}

type WebDAVService struct {
	client *gowebdav.Client
	config WebDAVConfig
}

type ProgressReader struct {
	file       *os.File
	Total      int64
	Downloaded int64
	OnProgress func(downloaded, total int64, speed string)
	Stop       <-chan struct{}
	lastUpdate time.Time
	startTime  time.Time
}

func (pr *ProgressReader) Read(p []byte) (int, error) {
	// 检查是否已取消
	if pr.Stop != nil {
		select {
		case <-pr.Stop:
			return 0, fmt.Errorf("upload cancelled")
		default:
		}
	}

	if pr.startTime.IsZero() {
		pr.startTime = time.Now()
	}

	n, err := pr.file.Read(p)
	if n > 0 {
		pr.Downloaded += int64(n)
		if pr.Total > 0 {
			// 限制更新频率，避免 WebSocket 消息过于频繁
			now := time.Now()
			if now.Sub(pr.lastUpdate) > 200*time.Millisecond || pr.Downloaded == pr.Total {
				if pr.OnProgress != nil {
					duration := now.Sub(pr.startTime).Seconds()
					speedStr := "0 KB/s"
					if duration > 0 {
						speed := float64(pr.Downloaded) / duration
						if speed > 1024*1024 {
							speedStr = fmt.Sprintf("%.2f MB/s", speed/1024/1024)
						} else {
							speedStr = fmt.Sprintf("%.2f KB/s", speed/1024)
						}
					}
					pr.OnProgress(pr.Downloaded, pr.Total, speedStr)
				}
				pr.lastUpdate = now
			}
		}
	}
	return n, err
}

// 实现 Seeker 接口，以便 http.NewRequest 能识别出 Content-Length
func (pr *ProgressReader) Seek(offset int64, whence int) (int64, error) {
	n, err := pr.file.Seek(offset, whence)
	if err == nil && offset == 0 && whence == 0 {
		// 如果回滚到开头（通常发生在 Digest 认证重试时），重置进度
		pr.Downloaded = 0
		log.Printf("[WebDAV] Request seek to 0, resetting progress counter")
	}
	return n, err
}

func NewWebDAVService(config WebDAVConfig) *WebDAVService {
	var client *gowebdav.Client
	if config.Enabled && config.URL != "" {
		// 确保 URL 以 / 结尾
		url := config.URL
		if !strings.HasSuffix(url, "/") {
			url += "/"
		}
		client = gowebdav.NewClient(url, config.Username, config.Password)
	}

	return &WebDAVService{
		client: client,
		config: config,
	}
}

func (w *WebDAVService) UploadFile(localPath, remoteFileName string, stop <-chan struct{}, onProgress func(downloaded, total int64, speed string)) error {
	if !w.config.Enabled || w.client == nil {
		return fmt.Errorf("WebDAV is not enabled")
	}

	file, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("failed to open local file: %v", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat local file: %v", err)
	}
	totalSize := stat.Size()

	// 构造远程完整路径
	remoteDir := w.config.RemoteDir
	if remoteDir == "" {
		remoteDir = "/"
	}
	if !strings.HasPrefix(remoteDir, "/") {
		remoteDir = "/" + remoteDir
	}

	// 确保远程目录存在
	err = w.MkdirAll(remoteDir)
	if err != nil {
		return fmt.Errorf("failed to create remote directory: %v", err)
	}

	remoteFilePath := path.Join(remoteDir, remoteFileName)

	// 使用 ProgressReader 包装 file 以监控进度
	// 通过实现 Seeker 接口，可以让 http.NewRequest 识别出 Content-Length，从而实现真正的流式上传
	var reader io.Reader = file
	if onProgress != nil || stop != nil {
		reader = &ProgressReader{
			file:       file,
			Total:      totalSize,
			OnProgress: onProgress,
			Stop:       stop,
			lastUpdate: time.Now(),
		}
	}

	// 使用流式上传，由于 reader 实现了 Seeker 接口，gowebdav 内部的 http.Client 会正确识别 Content-Length
	err = w.client.WriteStream(remoteFilePath, reader, 0644)
	if err != nil {
		return fmt.Errorf("failed to upload file: %v", err)
	}

	log.Printf("[WebDAV] File uploaded successfully: %s -> %s\n", localPath, remoteFilePath)
	return nil
}

// MkdirAll 递归创建远程目录
func (w *WebDAVService) MkdirAll(remoteDir string) error {
	if w.client == nil {
		return fmt.Errorf("WebDAV client is nil")
	}

	// 清理路径
	remoteDir = path.Clean(remoteDir)
	if remoteDir == "/" || remoteDir == "." {
		return nil
	}

	// 检查目录是否存在
	info, err := w.client.Stat(remoteDir)
	if err == nil {
		if info.IsDir() {
			return nil
		}
		return fmt.Errorf("path exists but is not a directory: %s", remoteDir)
	}

	// 递归创建父目录
	parent := path.Dir(remoteDir)
	if parent != remoteDir {
		err = w.MkdirAll(parent)
		if err != nil {
			return err
		}
	}

	// 创建当前目录
	log.Printf("[WebDAV] Creating directory: %s\n", remoteDir)
	return w.client.Mkdir(remoteDir, 0755)
}

func (w *WebDAVService) TestConnection() error {
	if !w.config.Enabled || w.client == nil {
		return fmt.Errorf("WebDAV is not enabled")
	}

	// 尝试读取根目录
	_, err := w.client.ReadDir("/")
	if err != nil {
		return fmt.Errorf("connection test failed: %v", err)
	}

	log.Println("[WebDAV] Connection test successful")
	return nil
}

func (w *WebDAVService) UpdateConfig(config WebDAVConfig) {
	w.config = config
	if config.Enabled && config.URL != "" {
		url := config.URL
		if !strings.HasSuffix(url, "/") {
			url += "/"
		}
		w.client = gowebdav.NewClient(url, config.Username, config.Password)
	} else {
		w.client = nil
	}
}

func (w *WebDAVService) ReadDir(path string) ([]os.FileInfo, error) {
	if w.client == nil {
		return nil, fmt.Errorf("WebDAV client is nil")
	}
	return w.client.ReadDir(path)
}

func (w *WebDAVService) ReadStream(path string) (io.ReadCloser, error) {
	if w.client == nil {
		return nil, fmt.Errorf("WebDAV client is nil")
	}
	return w.client.ReadStream(path)
}

func (w *WebDAVService) Stat(path string) (os.FileInfo, error) {
	if w.client == nil {
		return nil, fmt.Errorf("WebDAV client is nil")
	}
	return w.client.Stat(path)
}
