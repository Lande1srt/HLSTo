package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 构造一个标准 3 分片非加密 m3u8
func plainPlaylist3() []byte {
	return []byte(strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:10",
		"#EXT-X-MEDIA-SEQUENCE:0",
		"#EXTINF:10.0,",
		"seg-0.ts",
		"#EXTINF:10.0,",
		"seg-1.ts",
		"#EXTINF:10.0,",
		"seg-2.ts",
		"#EXT-X-ENDLIST",
		"",
	}, "\n"))
}

// 构造一个标准 3 分片加密 m3u8
func encryptedPlaylist3() []byte {
	return []byte(strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-VERSION:3",
		"#EXT-X-TARGETDURATION:10",
		"#EXT-X-MEDIA-SEQUENCE:0",
		`#EXT-X-KEY:METHOD=AES-128,URI="enc.key"`,
		"#EXTINF:10.0,",
		"seg-0.ts",
		"#EXTINF:10.0,",
		"seg-1.ts",
		"#EXTINF:10.0,",
		"seg-2.ts",
		"#EXT-X-ENDLIST",
		"",
	}, "\n"))
}

func TestBuildByteRangePlaylist_Plain(t *testing.T) {
	sizes := []int64{100, 200, 150}
	got, err := buildByteRangePlaylist(plainPlaylist3(), sizes, "movie.tsbin")
	if err != nil {
		t.Fatalf("重写失败: %v", err)
	}
	text := string(got)

	wantSubstrings := []string{
		"#EXTM3U\n",
		"#EXT-X-VERSION:4\n",
		"#EXT-X-TARGETDURATION:10\n",
		"#EXT-X-MEDIA-SEQUENCE:0\n",
		"#EXT-X-BYTERANGE:100@0\nmovie.tsbin\n",
		"#EXT-X-BYTERANGE:200@100\nmovie.tsbin\n",
		"#EXT-X-BYTERANGE:150@300\nmovie.tsbin\n",
		"#EXT-X-ENDLIST\n",
	}
	for _, w := range wantSubstrings {
		if !strings.Contains(text, w) {
			t.Errorf("结果缺少 %q\n实际:\n%s", w, text)
		}
	}

	// 旧分片 URI 不应残留
	if strings.Contains(text, "seg-0.ts") || strings.Contains(text, "seg-1.ts") {
		t.Errorf("旧分片 URI 未清除:\n%s", text)
	}
}

func TestBuildByteRangePlaylist_Encrypted(t *testing.T) {
	sizes := []int64{100, 200, 150}
	got, err := buildByteRangePlaylist(encryptedPlaylist3(), sizes, "movie.tsbin")
	if err != nil {
		t.Fatalf("加密 m3u8 重写失败: %v", err)
	}
	text := string(got)

	// EXT-X-KEY 原样保留（仍是 m3u8 头标签，位置在 EXTINF 之前）
	if !strings.Contains(text, `#EXT-X-KEY:METHOD=AES-128,URI="enc.key"`) {
		t.Errorf("EXT-X-KEY 标签丢失或被改写:\n%s", text)
	}
	if !strings.Contains(text, "#EXT-X-BYTERANGE:100@0\n") {
		t.Errorf("首个 BYTERANGE 异常:\n%s", text)
	}
}

func TestBuildByteRangePlaylist_CountMismatch(t *testing.T) {
	// sizes 少于分片数：处理到第 3 片时报错
	if _, err := buildByteRangePlaylist(plainPlaylist3(), []int64{100, 200}, "movie.tsbin"); err == nil {
		t.Errorf("sizes 少于分片数应报错")
	}

	// sizes 多于分片数：最终数量不一致报错
	if _, err := buildByteRangePlaylist(plainPlaylist3(), []int64{100, 200, 150, 999}, "movie.tsbin"); err == nil {
		t.Errorf("sizes 多于分片数应报错")
	}
}

func TestBuildByteRangePlaylist_UriWithoutExtInf(t *testing.T) {
	// EXT-X-MEDIA-SEQUENCE 之后直接跟 URI，无 EXTINF 引出
	bad := []byte(strings.Join([]string{
		"#EXTM3U",
		"#EXT-X-TARGETDURATION:10",
		"orphan.ts",
		"#EXT-X-ENDLIST",
		"",
	}, "\n"))

	if _, err := buildByteRangePlaylist(bad, []int64{10}, "movie.tsbin"); err == nil {
		t.Errorf("未由 EXTINF 引出的 URI 行应报错")
	}
}

func TestCollapseToSingleFile(t *testing.T) {
	dir := t.TempDir()

	// 3 个分片，内容长度分别为 4/8/3
	segContents := []string{"AAAA", "BBBBBBBB", "CCC"}
	segFiles := make([]string, 0, len(segContents))
	for i, c := range segContents {
		p := filepath.Join(dir, "seg-"+string(rune('0'+i))+".ts")
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatalf("写分片失败: %v", err)
		}
		segFiles = append(segFiles, p)
	}

	// 密钥文件（模拟加密产物，收敛后必须原样保留）
	keyPath := filepath.Join(dir, "enc.key")
	if err := os.WriteFile(keyPath, []byte("0123456789abcdef"), 0o644); err != nil {
		t.Fatalf("写密钥失败: %v", err)
	}

	indexPath := filepath.Join(dir, "index.m3u8")
	if err := os.WriteFile(indexPath, encryptedPlaylist3(), 0o644); err != nil {
		t.Fatalf("写 m3u8 失败: %v", err)
	}

	if err := collapseToSingleFile(dir, segFiles, indexPath, "movie",
		"task-123", true, "http://node.example:8080"); err != nil {
		t.Fatalf("收敛失败: %v", err)
	}

	// 1. 分片文件已删除
	for _, p := range segFiles {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("分片 %s 应已删除", p)
		}
	}

	// 2. tsbin 存在且内容为分片顺序拼接
	binPath := filepath.Join(dir, "movie.tsbin")
	binData, err := os.ReadFile(binPath)
	if err != nil {
		t.Fatalf("tsbin 不存在: %v", err)
	}
	if want := "AAAABBBBBBBBCCC"; string(binData) != want {
		t.Errorf("tsbin 内容 = %q, want %q", string(binData), want)
	}

	// 3. index.m3u8 已重写为 BYTERANGE，且无旧分片 URI
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatalf("读取重写后 m3u8 失败: %v", err)
	}
	indexText := string(indexData)
	if !strings.Contains(indexText, "#EXT-X-BYTERANGE:4@0\nmovie.tsbin") {
		t.Errorf("首个 BYTERANGE 异常:\n%s", indexText)
	}
	if !strings.Contains(indexText, "#EXT-X-BYTERANGE:3@12\nmovie.tsbin") {
		t.Errorf("第三个 BYTERANGE offset 异常:\n%s", indexText)
	}
	if strings.Contains(indexText, "seg-") {
		t.Errorf("旧分片 URI 残留:\n%s", indexText)
	}
	if !strings.Contains(indexText, "#EXT-X-VERSION:4") {
		t.Errorf("VERSION 未提升到 4:\n%s", indexText)
	}

	// 4. enc.key 原样保留
	if kd, err := os.ReadFile(keyPath); err != nil || string(kd) != "0123456789abcdef" {
		t.Errorf("enc.key 被破坏: %v", err)
	}

	// 5. meta.json 内容正确
	metaData, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("meta.json 不存在: %v", err)
	}
	var meta hlsMeta
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("meta.json 解析失败: %v", err)
	}
	if meta.TaskID != "task-123" {
		t.Errorf("meta.taskId = %q, want task-123", meta.TaskID)
	}
	if !meta.Encrypted {
		t.Errorf("meta.encrypted 应为 true")
	}
	if meta.NodeURL != "http://node.example:8080" {
		t.Errorf("meta.nodeUrl = %q", meta.NodeURL)
	}
}

func TestPublicURL(t *testing.T) {
	svc := NewWebDAVService(WebDAVConfig{URL: "https://dav.example.com"})

	cases := []struct {
		path string
		want string
	}{
		{"hls/movie_hls/index.m3u8", "https://dav.example.com/hls/movie_hls/index.m3u8"},
		{"/hls/movie_hls/index.m3u8", "https://dav.example.com/hls/movie_hls/index.m3u8"},
	}
	for _, c := range cases {
		if got := svc.PublicURL(c.path); got != c.want {
			t.Errorf("PublicURL(%q) = %q, want %q", c.path, got, c.want)
		}
	}

	// base 带尾斜杠时不应出现双斜杠
	svc2 := NewWebDAVService(WebDAVConfig{URL: "https://dav.example.com/"})
	if got := svc2.PublicURL("/a/b.m3u8"); got != "https://dav.example.com/a/b.m3u8" {
		t.Errorf("尾斜杠拼接异常: %q", got)
	}
}
