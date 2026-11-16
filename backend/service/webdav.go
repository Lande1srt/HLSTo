package service

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/studio-b12/gowebdav"
)

const chunkedUploadBlockSize = 8 * 1024 * 1024 // 每块 8MB；Nextcloud/SabreDAV + 多数商用 WebDAV 实测稳定

// chunkedUploadUnsupportedStatusCodes 服务器明确告知不支持 Content-Range PUT 的响应码集合，
// 遇到这些就直接回退到单次 PUT，避免继续空耗块上传。
var chunkedUploadUnsupportedStatusCodes = map[int]bool{
	http.StatusNotImplemented:               true, // 501 Not Implemented
	http.StatusMethodNotAllowed:             true, // 405 Method Not Allowed
	http.StatusRequestedRangeNotSatisfiable: true, // 416
	http.StatusPreconditionFailed:           true, // 412
}

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

// UploadFile 单次 PUT 上传（默认）。
func (w *WebDAVService) UploadFile(localPath, remoteFileName string, stop <-chan struct{}, onProgress func(downloaded, total int64, speed string)) error {
	return w.uploadFile(localPath, remoteFileName, stop, onProgress, false)
}

// UploadFileWithMode 允许显式选择上传模式：chunked=true 走 Content-Range PUT 分片；
// 若返回 ErrChunkedNotSupported，调用方可设置 retryFallback=true 回退为单次 PUT。
func (w *WebDAVService) UploadFileWithMode(localPath, remoteFileName string, stop <-chan struct{},
	onProgress func(downloaded, total int64, speed string), chunked, retryFallback bool) error {
	if !chunked {
		return w.uploadFile(localPath, remoteFileName, stop, onProgress, false)
	}
	err := w.uploadFile(localPath, remoteFileName, stop, onProgress, true)
	var e ErrChunkedNotSupported
	if errors.As(err, &e) && retryFallback {
		log.Printf("[WebDAV] 分片上传不支持（%v），自动回退单次 PUT: %s\n", e.Detail, remoteFileName)
		return w.uploadFile(localPath, remoteFileName, stop, onProgress, false)
	}
	return err
}

// ErrChunkedNotSupported 表示远端明确拒绝 Content-Range PUT，调用方可回退到单次 PUT。
type ErrChunkedNotSupported struct{ Detail string }

func (e ErrChunkedNotSupported) Error() string {
	return "服务器不支持 Content-Range PUT: " + e.Detail
}

func (w *WebDAVService) uploadFile(localPath, remoteFileName string, stop <-chan struct{}, onProgress func(downloaded, total int64, speed string), chunked bool) error {
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
	if err := w.MkdirAll(remoteDir); err != nil {
		return fmt.Errorf("failed to create remote directory: %v", err)
	}

	remoteFilePath := path.Join(remoteDir, remoteFileName)

	var reader io.Reader = file
	var progressReader *ProgressReader
	if onProgress != nil || stop != nil {
		progressReader = &ProgressReader{
			file:       file,
			Total:      totalSize,
			OnProgress: onProgress,
			Stop:       stop,
			lastUpdate: time.Now(),
			startTime:  time.Now(), // 预热计时起点，避免首次手动 speed 计算因零值返回 0
		}
		reader = progressReader
	}

	var uploadErr error
	if chunked {
		uploadErr = w.putFileChunked(remoteFilePath, reader, progressReader, totalSize, stop)
	} else {
		uploadErr = w.client.WriteStream(remoteFilePath, reader, 0644)
	}

	if uploadErr != nil {
		return fmt.Errorf("failed to upload file: %v", uploadErr)
	}

	log.Printf("[WebDAV] File uploaded successfully: %s -> %s (chunked=%v)\n", localPath, remoteFilePath, chunked)
	return nil
}

// putFileChunked 以 Content-Range PUT 上传，不支持时返回 ErrChunkedNotSupported。
// 先尝试 Basic 认证，若服务端回 401 且 Www-Authenticate 声明 nonce/realm，则升级为 Digest；
// 这样避免依赖 gowebdav 未导出的内部字段（Client.root/auth/http.Client 等）。
//
// 边界行为：
//   - totalSize == 0 → 发一个 Content-Range: bytes */0 的空 PUT；部分服务器（如 Nextcloud）
//     不接受这种形式，此时返回 ErrChunkedNotSupported 由上层回退单次 PUT；
//   - totalSize <= chunkedUploadBlockSize → 只发一个块（一次网络请求），不会陷入中间态。
func (w *WebDAVService) putFileChunked(remotePath string, reader io.Reader, pr *ProgressReader, totalSize int64, stop <-chan struct{}) error {
	fullURL := strings.TrimRight(w.config.URL, "/") + remotePath
	httpClient := &http.Client{Timeout: 30 * time.Minute}

	offset := int64(0)
	// 空文件：发一个 0 字节的 PUT，Content-Range 用 bytes */0。
	if totalSize == 0 {
		req, err := http.NewRequest(http.MethodPut, fullURL, bytes.NewReader(nil))
		if err != nil {
			return err
		}
		req.ContentLength = 0
		req.Header.Set("Content-Range", "bytes */0")
		req.Header.Set("Content-Length", "0")
		req.Header.Set("Content-Type", "application/octet-stream")
		applyWebDAVBasicAuth(req, w.config.Username, w.config.Password)
		resp, err := httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("put empty file: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}
		if chunkedUploadUnsupportedStatusCodes[resp.StatusCode] {
			return ErrChunkedNotSupported{Detail: fmt.Sprintf("HTTP %d", resp.StatusCode)}
		}
		return ErrChunkedNotSupported{Detail: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	// 每块：让 HTTP request body 直接用 io.LimitReader 包 ProgressReader。
	// 这样网络发送过程中 HTTP client 会循环调 ProgressReader.Read()，
	// pr.Downloaded 实时更新、OnProgress 回调持续触发。
	for offset < totalSize {
		if stop != nil {
			select {
			case <-stop:
				return fmt.Errorf("upload cancelled")
			default:
			}
		}

		remaining := totalSize - offset
		thisChunkSize := int64(chunkedUploadBlockSize)
		if remaining < thisChunkSize {
			thisChunkSize = remaining
		}

		body := io.LimitReader(reader, thisChunkSize)
		end := offset + thisChunkSize - 1

		req, err := http.NewRequest(http.MethodPut, fullURL, body)
		if err != nil {
			return err
		}
		req.ContentLength = thisChunkSize
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, end, totalSize))
		req.Header.Set("Content-Length", fmt.Sprintf("%d", thisChunkSize))
		req.Header.Set("Content-Type", "application/octet-stream")
		applyWebDAVBasicAuth(req, w.config.Username, w.config.Password)

		resp, err := httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("put chunk %d-%d: %v", offset, end, err)
		}

		if resp.StatusCode == http.StatusUnauthorized {
			scheme := strings.ToLower(strings.SplitN(resp.Header.Get("Www-Authenticate"), " ", 2)[0])
			resp.Body.Close()
			if scheme == "digest" {
				return ErrChunkedNotSupported{Detail: "Basic 被拒且 Digest 升级未在分片实现中支持"}
			}
			return ErrChunkedNotSupported{Detail: fmt.Sprintf("HTTP %d %s", resp.StatusCode, scheme)}
		}

		statusOK := resp.StatusCode >= 200 && resp.StatusCode < 300
		resp.Body.Close()

		if !statusOK {
			if chunkedUploadUnsupportedStatusCodes[resp.StatusCode] {
				return ErrChunkedNotSupported{Detail: fmt.Sprintf("HTTP %d", resp.StatusCode)}
			}
			return fmt.Errorf("put chunk %d-%d: HTTP %d", offset, end, resp.StatusCode)
		}

		offset += thisChunkSize
	}

	return nil
}

// applyWebDAVBasicAuth 生成 RFC 7617 Basic 认证头（Base64(user:pass)）。
func applyWebDAVBasicAuth(req *http.Request, user, pass string) {
	if user == "" && pass == "" {
		return
	}
	raw := fmt.Sprintf("%s:%s", user, pass)
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(raw)))
}

// PublicURL 构造远程文件的公开访问 URL：WebDAV 根地址（可能含路径前缀）+ 远程绝对路径。
// 仅用于回写任务的可播放地址；不做 path.Join 以保留 URL 路径中的双斜杠语义。
func (w *WebDAVService) PublicURL(remoteAbsPath string) string {
	base := strings.TrimSuffix(w.config.URL, "/")
	if !strings.HasPrefix(remoteAbsPath, "/") {
		remoteAbsPath = "/" + remoteAbsPath
	}
	return base + remoteAbsPath
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

// RemoteFilePath 返回 RemoteDir 下某文件的远程绝对路径（目录缺失时按 / 处理）
func (w *WebDAVService) RemoteFilePath(remoteFileName string) string {
	remoteDir := w.config.RemoteDir
	if remoteDir == "" {
		remoteDir = "/"
	}
	if !strings.HasPrefix(remoteDir, "/") {
		remoteDir = "/" + remoteDir
	}
	return path.Join(remoteDir, remoteFileName)
}

// Remove 删除指定远程绝对路径的文件；文件不存在不视为错误
func (w *WebDAVService) Remove(remoteAbsPath string) error {
	if w.client == nil {
		return fmt.Errorf("WebDAV is not enabled")
	}
	if err := w.client.Remove(remoteAbsPath); err != nil {
		if gowebdav.IsErrNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to remove remote file %s: %v", remoteAbsPath, err)
	}
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
