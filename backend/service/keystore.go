package service

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 独立密钥库：二次 HLS 加密密钥不再放在 download_* 缓存目录（定时清理会整体删除），
// 统一存放于工作目录下的 keys/ 目录：
//   - 密钥文件以任务 ID 命名：keys/{taskID}.key（固定 16 字节）
//   - index.json 记录「任务 ID ↔ 原始文件名」及创建时间
//
// keys/ 不以 download_ 开头，定时/手动清理均不会触及；仅在删除任务时联动移除。
const (
	keyStoreDirName   = "keys"
	keyStoreIndexName = "index.json"
	keyStoreVersion   = 1
)

// hlsKeyRecord index.json 中的单条映射
type hlsKeyRecord struct {
	ID        string `json:"id"`
	FileName  string `json:"fileName"` // 密钥的原始/逻辑文件名（如下载响应名 enc.key）
	CreatedAt string `json:"createdAt"`
}

type hlsKeyIndex struct {
	Version int            `json:"version"`
	Keys    []hlsKeyRecord `json:"keys"`
}

// keyStore 进程内唯一密钥库（defaultKeyStore）。
// 目录随工作目录实时解析（与 resolveSpecifiedKey 一致），mu 串行化索引与密钥文件的读写。
type keyStore struct {
	mu sync.Mutex
}

var defaultKeyStore = &keyStore{}

// dir 返回密钥库目录并确保存在
func (s *keyStore) dir() (string, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("获取工作目录失败: %w", err)
	}
	dir := filepath.Join(pwd, keyStoreDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("创建密钥目录失败: %w", err)
	}
	return dir, nil
}

// put 写入（或覆盖）任务密钥并 upsert 索引；返回密钥文件绝对路径。
// 密钥文件与索引均走「临时文件 + rename」，避免半截内容。
func (s *keyStore) put(taskID string, data []byte, fileName string) (string, error) {
	if taskID == "" {
		return "", fmt.Errorf("任务 ID 为空，无法存储密钥")
	}
	if len(data) != 16 {
		return "", fmt.Errorf("密钥必须为 16 字节（HLS AES-128），实际 %d 字节", len(data))
	}
	fileName = sanitizeKeyFileName(fileName)

	s.mu.Lock()
	defer s.mu.Unlock()

	dir, err := s.dir()
	if err != nil {
		return "", err
	}

	keyPath := filepath.Join(dir, taskID+".key")
	tmpKey := keyPath + ".tmp"
	if err := os.WriteFile(tmpKey, data, 0o600); err != nil {
		return "", fmt.Errorf("写入密钥失败: %w", err)
	}
	if err := os.Rename(tmpKey, keyPath); err != nil {
		os.Remove(tmpKey)
		return "", fmt.Errorf("密钥落盘失败: %w", err)
	}

	index := s.loadIndexLocked(dir)
	// upsert：同 ID 替换（重试加密会生成新密钥）
	replaced := false
	for i := range index.Keys {
		if index.Keys[i].ID == taskID {
			index.Keys[i].FileName = fileName
			index.Keys[i].CreatedAt = time.Now().Format("2006-01-02 15:04:05")
			replaced = true
			break
		}
	}
	if !replaced {
		index.Keys = append(index.Keys, hlsKeyRecord{
			ID:        taskID,
			FileName:  fileName,
			CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
		})
	}
	if err := s.saveIndexLocked(dir, index); err != nil {
		return "", err
	}

	log.Printf("[KeyStore] 密钥已存储: %s（原始文件名 %s）\n", keyPath, fileName)
	return keyPath, nil
}

// remove 删除任务密钥文件并移除索引条目；文件/条目不存在视为成功。
func (s *keyStore) remove(taskID string) error {
	if taskID == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	pwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("获取工作目录失败: %w", err)
	}
	dir := filepath.Join(pwd, keyStoreDirName)

	// 密钥库尚未创建，自然没有可删内容
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		return nil
	}

	if err := os.Remove(filepath.Join(dir, taskID+".key")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除密钥文件失败: %w", err)
	}

	index := s.loadIndexLocked(dir)
	kept := index.Keys[:0]
	for _, r := range index.Keys {
		if r.ID != taskID {
			kept = append(kept, r)
		}
	}
	index.Keys = kept

	if len(index.Keys) == 0 {
		// 无任何密钥时移除索引，避免空壳残留
		if err := os.Remove(filepath.Join(dir, keyStoreIndexName)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理空索引失败: %w", err)
		}
		return nil
	}
	return s.saveIndexLocked(dir, index)
}

// fileName 查询任务密钥的原始文件名；无记录返回 ""。
func (s *keyStore) fileName(taskID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	pwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := filepath.Join(pwd, keyStoreDirName)
	for _, r := range s.loadIndexLocked(dir).Keys {
		if r.ID == taskID {
			return r.FileName
		}
	}
	return ""
}

// loadIndexLocked 读取索引；文件不存在或损坏时返回空索引（调用方持锁）
func (s *keyStore) loadIndexLocked(dir string) hlsKeyIndex {
	empty := hlsKeyIndex{Version: keyStoreVersion, Keys: []hlsKeyRecord{}}

	data, err := os.ReadFile(filepath.Join(dir, keyStoreIndexName))
	if err != nil {
		return empty
	}
	var index hlsKeyIndex
	if err := json.Unmarshal(data, &index); err != nil {
		log.Printf("[KeyStore] 索引解析失败，按空索引重建: %v\n", err)
		return empty
	}
	if index.Keys == nil {
		index.Keys = []hlsKeyRecord{}
	}
	sort.Slice(index.Keys, func(i, j int) bool { return index.Keys[i].ID < index.Keys[j].ID })
	return index
}

// saveIndexLocked 原子写入索引（调用方持锁）
func (s *keyStore) saveIndexLocked(dir string, index hlsKeyIndex) error {
	index.Version = keyStoreVersion
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("构造密钥索引失败: %w", err)
	}
	tmp := filepath.Join(dir, keyStoreIndexName+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("写入密钥索引失败: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(dir, keyStoreIndexName)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("密钥索引落盘失败: %w", err)
	}
	return nil
}

// LookupHLSKeyFileName 供 handler 查询任务密钥的原始文件名（下载响应名）；无记录返回 ""。
func LookupHLSKeyFileName(taskID string) string {
	return defaultKeyStore.fileName(taskID)
}

// originalKeyFileName 从指定密钥 URL 提取原始文件名；无法提取时回退 enc.key。
func originalKeyFileName(rawURL string) string {
	name := hlsKeyFileName
	if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil {
		if base := path.Base(u.Path); base != "" && base != "." && base != "/" {
			name = base
		}
	}
	return sanitizeKeyFileName(name)
}

// sanitizeKeyFileName 保证文件名可安全用于路径与 Content-Disposition：
// 拒绝路径分隔符、引号、控制字符，否则回退 enc.key。
func sanitizeKeyFileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return hlsKeyFileName
	}
	if strings.ContainsAny(name, `/\"`) {
		return hlsKeyFileName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return hlsKeyFileName
		}
	}
	return name
}
