package service

import (
	"fmt"
	"io"
	"log"
	"os"
	"path"
	"strings"

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

type ProgressWriter struct {
	Total      int64
	Downloaded int64
	OnProgress func(progress float64)
}

func (pw *ProgressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.Downloaded += int64(n)
	if pw.Total > 0 {
		progress := float64(pw.Downloaded) / float64(pw.Total) * 100
		pw.OnProgress(progress)
	}
	return n, nil
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

func (w *WebDAVService) UploadFile(localPath, remoteFileName string, onProgress func(progress float64)) error {
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

	// 使用 ProgressWriter 包装 file 以监控进度
	var reader io.Reader = file
	if onProgress != nil {
		pw := &ProgressWriter{
			Total:      totalSize,
			OnProgress: onProgress,
		}
		reader = io.TeeReader(file, pw)
	}

	// 使用流式上传，避免大文件占用内存
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
