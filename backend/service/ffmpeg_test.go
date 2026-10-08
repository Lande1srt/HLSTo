package service

import (
	"archive/zip"
	"m3u8-downloader-web/model"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseVersionOutput(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   string
		ok     bool
	}{
		{
			name:   "标准 release 输出",
			output: "ffmpeg version 7.0.2 Copyright (c) 2000-2024 the FFmpeg developers\nbuilt with gcc 13.2",
			want:   "7.0.2",
			ok:     true,
		},
		{
			name:   "Windows essentials 构建",
			output: "ffmpeg version 7.0.2-essentials_build-www.gyan.dev Copyright",
			want:   "7.0.2-essentials_build-www.gyan.dev",
			ok:     true,
		},
		{
			name:   "git 快照构建",
			output: "ffmpeg version N-116031-g28749534e4-20240723",
			want:   "N-116031-g28749534e4-20240723",
			ok:     true,
		},
		{
			name:   "无效输出",
			output: "command not found",
			want:   "",
			ok:     false,
		},
		{
			name:   "空输出",
			output: "",
			want:   "",
			ok:     false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseVersionOutput([]byte(tc.output))
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if got != tc.want {
				t.Fatalf("version = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSniffArchive(t *testing.T) {
	cases := []struct {
		name    string
		magic   []byte
		want    string
		wantErr bool
	}{
		{"zip", []byte{0x50, 0x4b, 0x03, 0x04, 0x00, 0x00}, "zip", false},
		{"xz", []byte{0xfd, '7', 'z', 'X', 'Z', 0x00}, "tarxz", false},
		{"未知格式", []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05}, "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "archive")
			if err := os.WriteFile(path, tc.magic, 0o644); err != nil {
				t.Fatalf("写入临时文件失败: %v", err)
			}

			got, err := sniffArchive(path)
			if tc.wantErr {
				if err == nil {
					t.Fatal("期望返回错误，实际为 nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("sniffArchive 返回错误: %v", err)
			}
			if got != tc.want {
				t.Fatalf("kind = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWithinDir(t *testing.T) {
	base := filepath.Clean("/data/app")
	cases := []struct {
		target string
		want   bool
	}{
		{filepath.Join(base, "ffmpeg", "bin", "ffmpeg"), true},
		{filepath.Join(base, "ffmpeg"), true},
		{filepath.Join(base, "..", "etc", "passwd"), false},
		{filepath.Join(base, ".."), false},
	}

	for _, tc := range cases {
		if got := withinDir(filepath.Clean(tc.target), base); got != tc.want {
			t.Errorf("withinDir(%q) = %v, want %v", tc.target, got, tc.want)
		}
	}
}

func TestBuildSourcesCurrentPlatform(t *testing.T) {
	svc := NewFFmpegService()
	sources, err := svc.buildSources()
	if err != nil {
		t.Fatalf("buildSources 返回错误: %v", err)
	}
	if len(sources) == 0 {
		t.Fatal("当前平台下载源为空")
	}

	for i, src := range sources {
		if src.url == "" {
			t.Fatalf("第 %d 个源 URL 为空", i)
		}
		if src.name == "" {
			t.Fatalf("第 %d 个源名称为空", i)
		}
	}

	// 所有源必须按平台给出对应归档的 URL
	for _, src := range sources {
		lower := strings.ToLower(src.url)
		if !(strings.Contains(lower, ".zip") || strings.Contains(lower, ".tar.xz") ||
			strings.Contains(lower, "github.com")) {
			t.Errorf("源 URL 疑似无效: %s", src.url)
		}
	}

	// 当前为 windows/amd64 时验证具体顺序：镜像 → gyan.dev → 官方直连
	if runtime.GOOS == "windows" && runtime.GOARCH == "amd64" {
		if len(sources) < 4 {
			t.Fatalf("windows/amd64 至少应有 4 个源，实际 %d", len(sources))
		}
		if !strings.HasPrefix(sources[0].url, "https://ghfast.top/") {
			t.Errorf("首个源应为 ghfast 加速，实际 %s", sources[0].url)
		}
		if !strings.Contains(sources[2].url, "gyan.dev") {
			t.Errorf("第三个源应为 gyan.dev，实际 %s", sources[2].url)
		}
		if !strings.HasPrefix(sources[len(sources)-1].url, "https://github.com/") {
			t.Errorf("末位源应为 GitHub 官方直连，实际 %s", sources[len(sources)-1].url)
		}
	}
}

func TestExtractZip(t *testing.T) {
	// 构造包含 ffmpeg 二进制（嵌套目录）的 zip
	archivePath := filepath.Join(t.TempDir(), "ffmpeg.zip")
	zipFile, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("创建 zip 失败: %v", err)
	}

	w := zip.NewWriter(zipFile)
	binaryName := ffmpegBinaryName()
	entry, err := w.Create("ffmpeg-7.0.2/bin/" + binaryName)
	if err != nil {
		t.Fatalf("创建 zip 条目失败: %v", err)
	}
	if _, err := entry.Write([]byte("fake ffmpeg binary")); err != nil {
		t.Fatalf("写入 zip 条目失败: %v", err)
	}
	// 夹带一个无关文件，应被忽略
	other, err := w.Create("ffmpeg-7.0.2/doc/readme.txt")
	if err != nil {
		t.Fatalf("创建 zip 条目失败: %v", err)
	}
	other.Write([]byte("documentation"))
	if err := w.Close(); err != nil {
		t.Fatalf("关闭 zip writer 失败: %v", err)
	}
	if err := zipFile.Close(); err != nil {
		t.Fatalf("关闭 zip 文件失败: %v", err)
	}

	binDir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("创建 bin 目录失败: %v", err)
	}

	if err := extractZip(archivePath, binDir); err != nil {
		t.Fatalf("extractZip 返回错误: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(binDir, binaryName))
	if err != nil {
		t.Fatalf("读取提取结果失败: %v", err)
	}
	if string(content) != "fake ffmpeg binary" {
		t.Fatalf("提取内容不匹配: %q", content)
	}

	// 无关文件不应被提取
	if _, err := os.Stat(filepath.Join(binDir, "readme.txt")); !os.IsNotExist(err) {
		t.Fatal("无关文件不应被提取")
	}
}

// TestFFmpegMuxCodecArgs 验证各封装模式生成的 ffmpeg 编码参数
func TestFFmpegMuxCodecArgs(t *testing.T) {
	cases := []struct {
		name        string
		mode        string
		mustContain []string
		mustNotHave []string
	}{
		{
			name:        "源流式复制",
			mode:        model.MuxModeCopy,
			mustContain: []string{"-c", "copy"},
			mustNotHave: []string{"libx264", "libx265"},
		},
		{
			name:        "转码 H.264",
			mode:        model.MuxModeH264,
			mustContain: []string{"-c:v", "libx264", "-crf", "23", "-pix_fmt", "yuv420p", "-c:a", "aac"},
			mustNotHave: []string{"libx265"},
		},
		{
			name:        "转码 H.265",
			mode:        model.MuxModeH265,
			mustContain: []string{"-c:v", "libx265", "-crf", "28", "-tag:v", "hvc1"},
			mustNotHave: []string{"libx264"},
		},
		{
			name:        "非法模式回退流式复制",
			mode:        "av1",
			mustContain: []string{"-c", "copy"},
			mustNotHave: []string{"libx264", "libx265"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := ffmpegMuxCodecArgs(tc.mode)
			joined := strings.Join(args, " ")
			for _, want := range tc.mustContain {
				found := false
				for _, a := range args {
					if a == want {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("模式 %s 参数中缺少 %q，实际: %v", tc.mode, want, args)
				}
			}
			for _, banned := range tc.mustNotHave {
				if strings.Contains(joined, banned) {
					t.Fatalf("模式 %s 参数中不应出现 %q，实际: %v", tc.mode, banned, args)
				}
			}
		})
	}
}

// TestMuxModeSpeedLabel 验证模式的中文进度文案
func TestMuxModeSpeedLabel(t *testing.T) {
	if got := muxModeSpeedLabel(model.MuxModeCopy); got != "源流式复制" {
		t.Fatalf("copy 标签错误: %q", got)
	}
	if got := muxModeSpeedLabel(model.MuxModeH264); got != "转码 H.264" {
		t.Fatalf("h264 标签错误: %q", got)
	}
	if got := muxModeSpeedLabel(model.MuxModeH265); got != "转码 H.265" {
		t.Fatalf("h265 标签错误: %q", got)
	}
}

// TestNormalizeMuxMode 验证模式合法化
func TestNormalizeMuxMode(t *testing.T) {
	if got := model.NormalizeMuxMode("h264"); got != model.MuxModeH264 {
		t.Fatalf("h264 被错误合法化: %q", got)
	}
	if got := model.NormalizeMuxMode("h265"); got != model.MuxModeH265 {
		t.Fatalf("h265 被错误合法化: %q", got)
	}
	for _, bad := range []string{"", "vp9", "H264", "av1"} {
		if got := model.NormalizeMuxMode(bad); got != model.MuxModeCopy {
			t.Fatalf("非法模式 %q 应回退 copy，得到 %q", bad, got)
		}
	}
}
