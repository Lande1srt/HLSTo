package service

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"m3u8-downloader-web/model"
)

// withTempWd 将工作目录切换到临时目录（resolveSpecifiedKey 基于工作目录建缓存）
func withTempWd(t *testing.T) {
	t.Helper()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败: %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("切换工作目录失败: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
}

func keyHash(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return hex.EncodeToString(sum[:])[:16]
}

func TestResolveSpecifiedKey_DownloadCacheReuse(t *testing.T) {
	goodKey := []byte("0123456789abcdef") // 恰好 16 字节
	var goodHits int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/good.key":
			atomic.AddInt32(&goodHits, 1)
			_, _ = w.Write(goodKey)
		case "/short.key":
			_, _ = w.Write([]byte("short")) // 5 字节
		case "/long.key":
			_, _ = w.Write(make([]byte, 17))
		case "/bad.key":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	withTempWd(t)

	goodURL := srv.URL + "/good.key"

	// 首次调用：下载并带时间戳落盘
	p1, err := resolveSpecifiedKey(goodURL)
	if err != nil {
		t.Fatalf("首次下载密钥失败: %v", err)
	}
	info, err := os.Stat(p1)
	if err != nil {
		t.Fatalf("缓存密钥不存在: %v", err)
	}
	if info.Size() != 16 {
		t.Fatalf("缓存密钥大小 = %d, want 16", info.Size())
	}
	data, err := os.ReadFile(p1)
	if err != nil {
		t.Fatalf("读取缓存密钥失败: %v", err)
	}
	if string(data) != string(goodKey) {
		t.Fatalf("缓存密钥内容不匹配")
	}

	// 文件名规则：{sha256(URL)[:16]}-{20060102-150405.000000}.key
	base := filepath.Base(p1)
	wantPrefix := keyHash(goodURL) + "-"
	if !strings.HasPrefix(base, wantPrefix) || !strings.HasSuffix(base, ".key") {
		t.Fatalf("缓存文件名 %q 不符合 {hash}-{timestamp}.key 规则", base)
	}
	stamp := strings.TrimSuffix(strings.TrimPrefix(base, wantPrefix), ".key")
	if _, err := time.Parse("20060102-150405.000000", stamp); err != nil {
		t.Fatalf("缓存文件名时间戳非法: %v", err)
	}
	if atomic.LoadInt32(&goodHits) != 1 {
		t.Fatalf("首次调用应只请求 1 次，实际 %d 次", goodHits)
	}

	// 二次调用：必须复用缓存、不再发起请求
	p2, err := resolveSpecifiedKey(goodURL)
	if err != nil {
		t.Fatalf("复用缓存密钥失败: %v", err)
	}
	if p2 != p1 {
		t.Fatalf("二次调用未复用同一缓存文件: %q != %q", p2, p1)
	}
	if atomic.LoadInt32(&goodHits) != 1 {
		t.Fatalf("复用缓存不应发起请求，请求次数 = %d", goodHits)
	}

	// 非法入参
	for _, bad := range []string{"", "://broken-url"} {
		if _, err := resolveSpecifiedKey(bad); err == nil {
			t.Errorf("入参 %q 应返回错误", bad)
		}
	}

	// 长度不符 / 状态异常 / 不存在
	for _, path := range []string{"/short.key", "/long.key", "/bad.key", "/missing.key"} {
		if _, err := resolveSpecifiedKey(srv.URL + path); err == nil {
			t.Errorf("路径 %q 应返回错误", path)
		}
	}
}

func TestResolveSpecifiedKey_InvalidCacheIgnored(t *testing.T) {
	goodKey := []byte("fedcba9876543210")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(goodKey)
	}))
	defer srv.Close()

	withTempWd(t)

	rawURL := srv.URL + "/good.key"
	cacheDir := filepath.Join(mustGetwd(t), "hlskeys")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatalf("创建缓存目录失败: %v", err)
	}

	// 预置一个“早期时间戳”的无效缓存（大小非 16），应被忽略并重新下载
	staleName := keyHash(rawURL) + "-19990101-000000.000000.key"
	if err := os.WriteFile(filepath.Join(cacheDir, staleName), []byte("bad"), 0o644); err != nil {
		t.Fatalf("预置无效缓存失败: %v", err)
	}

	path, err := resolveSpecifiedKey(rawURL)
	if err != nil {
		t.Fatalf("忽略无效缓存后下载失败: %v", err)
	}
	if filepath.Base(path) == staleName {
		t.Fatalf("命中了无效缓存文件")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != string(goodKey) {
		t.Fatalf("新缓存密钥内容异常: %v", err)
	}
}

func TestPrepareHLSKey(t *testing.T) {
	withTempWd(t)

	// 生成模式：密钥独立存于 keys/{taskID}.key，两次生成应互不相同
	k1, err := prepareHLSKey("task-gen-1", model.HLSEncryptGenerated, "")
	if err != nil {
		t.Fatalf("生成密钥失败: %v", err)
	}
	if filepath.Base(k1) != "task-gen-1.key" {
		t.Fatalf("生成模式密钥应以 ID 命名，实际 %q", filepath.Base(k1))
	}
	check16(t, k1)
	b1, _ := os.ReadFile(k1)

	k2, err := prepareHLSKey("task-gen-2", model.HLSEncryptGenerated, "")
	if err != nil {
		t.Fatalf("二次生成密钥失败: %v", err)
	}
	check16(t, k2)
	b2, _ := os.ReadFile(k2)

	if string(b1) == string(b2) {
		t.Fatalf("两次生成的随机密钥相同")
	}

	// 密钥不在 download_* 缓存目录内（定时清理不会删除）
	if strings.Contains(filepath.ToSlash(k1), "/download_") {
		t.Fatalf("密钥仍位于 download_* 缓存目录: %s", k1)
	}

	// 指定模式：从测试服务器取密钥，存为 keys/{taskID}.key，索引记录原始文件名
	goodKey := []byte("aaaa1111bbbb2222")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(goodKey)
	}))
	defer srv.Close()

	target, err := prepareHLSKey("task-spec-1", model.HLSEncryptSpecified, srv.URL+"/enc.key")
	if err != nil {
		t.Fatalf("指定模式准备密钥失败: %v", err)
	}
	if filepath.Base(target) != "task-spec-1.key" {
		t.Fatalf("指定模式密钥也应以 ID 命名，实际 %q", filepath.Base(target))
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != string(goodKey) {
		t.Fatalf("任务密钥内容不匹配: %v", err)
	}
	// JSON 索引：ID → 原始文件名
	if got := LookupHLSKeyFileName("task-spec-1"); got != "enc.key" {
		t.Fatalf("索引原始文件名 = %q, want enc.key", got)
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败: %v", err)
	}
	return wd
}

func check16(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("密钥文件不存在: %v", err)
	}
	if info.Size() != 16 {
		t.Fatalf("密钥大小 = %d, want 16", info.Size())
	}
}
