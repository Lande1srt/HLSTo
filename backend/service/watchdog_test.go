package service

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNormalizeFFmpegMsg(t *testing.T) {
	line := "[h264 @ 000001f5c2491d40] non-existing PPS 0 referenced"
	got := normalizeFFmpegMsg(line)
	want := "[h264 @ 0xADDR] non-existing PPS 0 referenced"
	if got != want {
		t.Fatalf("归一化 = %q，want %q", got, want)
	}

	// 不同运行地址应归一为同一标识
	other := normalizeFFmpegMsg("[h264 @ 000002aabbcc99ee] non-existing PPS 0 referenced")
	if other != got {
		t.Fatal("不同内存地址的相同消息应归一为同一标识")
	}

	// 连续空白压缩
	if normalizeFFmpegMsg("a   b\t c") != "a b c" {
		t.Fatal("连续空白未压缩")
	}
}

func TestFFmpegWatchdogTrip(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stall := 120 * time.Millisecond
	window := 40 * time.Millisecond
	wd := newFFmpegWatchdog(cancel, stall, window)

	cancelled := make(chan struct{})
	go func() {
		<-ctx.Done()
		close(cancelled)
	}()

	// minWindows = stall/window = 3
	wd.observe("same-error")
	time.Sleep(50 * time.Millisecond)
	wd.observe("same-error")
	time.Sleep(50 * time.Millisecond)
	wd.observe("same-error") // windows=3，距首约 100ms，尚不满 120ms

	if wd.isTripped() {
		t.Fatal("持续时间未满 stall，不应触发")
	}

	time.Sleep(50 * time.Millisecond)
	wd.observe("same-error") // 距首约 150ms、无进展、windows≥3 → 触发

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("持续异常无进展，看门狗应自动终止")
	}
}

func TestFFmpegWatchdogProgressReset(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	wd := newFFmpegWatchdog(cancel, 120*time.Millisecond, 40*time.Millisecond)

	wd.observe("err")
	time.Sleep(60 * time.Millisecond)
	wd.observe("err") // windows=2

	// 处理时间真实推进 → 循环判定解除
	wd.noteProgress(1_000_000)

	wd.observe("err") // 重新计 windows=1、firstSeen=now
	time.Sleep(150 * time.Millisecond)
	wd.observe("err") // windows=2 < minWindows(3)，仍不触发

	if wd.isTripped() {
		t.Fatal("有真实处理进展时不应判为异常循环")
	}
}

func TestFFmpegStderrSinkAggregation(t *testing.T) {
	var mu sync.Mutex
	var emitted []string

	sink := &ffmpegStderrSink{aggWindow: 50 * time.Millisecond}
	sink.emit = func(_ string, msg string) {
		mu.Lock()
		emitted = append(emitted, msg)
		mu.Unlock()
	}

	// 3 行相同消息 + 1 行不同消息（不同消息写入时同组立即折叠输出）
	if _, err := io.WriteString(sink, "some error\nsome error\nsome error\nother error\n"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	sink.close()
	sink.close() // 幂等：重复 close 不应产生额外输出

	mu.Lock()
	defer mu.Unlock()

	if len(emitted) != 2 {
		t.Fatalf("聚合后输出 %d 条，want 2，实际: %v", len(emitted), emitted)
	}
	if !strings.Contains(emitted[0], "重复3次") {
		t.Fatalf("折叠消息应标注重复次数，实际: %q", emitted[0])
	}
	if !strings.HasPrefix(emitted[0], "FFmpeg: ") {
		t.Fatalf("日志应带 FFmpeg 前缀: %q", emitted[0])
	}
	if !strings.Contains(emitted[1], "other error") {
		t.Fatalf("第二条应为不同消息: %q", emitted[1])
	}
}

func TestFFmpegStderrSinkNoNewlineResidue(t *testing.T) {
	var emitted []string
	var mu sync.Mutex

	sink := &ffmpegStderrSink{aggWindow: 50 * time.Millisecond}
	sink.emit = func(_ string, msg string) {
		mu.Lock()
		emitted = append(emitted, msg)
		mu.Unlock()
	}

	// 无结尾换行的最后一行，close 时必须输出，不能丢
	if _, err := io.WriteString(sink, "tail line without newline"); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	sink.close()

	mu.Lock()
	defer mu.Unlock()
	if len(emitted) != 1 || !strings.Contains(emitted[0], "tail line without newline") {
		t.Fatalf("无换行残余行未正确输出: %v", emitted)
	}
}

func TestConcatenateTSFiles(t *testing.T) {
	dir := t.TempDir()

	pieces := make([]string, 0, 3)
	for _, content := range []string{"AAAA", "BBBB", "CCCC"} {
		path := filepath.Join(dir, "piece_"+content+".ts")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("写分片失败: %v", err)
		}
		pieces = append(pieces, path)
	}

	rawPath := filepath.Join(dir, "raw.ts")
	if err := concatenateTSFiles(pieces, rawPath); err != nil {
		t.Fatalf("预拼接失败: %v", err)
	}

	got, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatalf("读拼接产物失败: %v", err)
	}
	if string(got) != "AAAABBBBCCCC" {
		t.Fatalf("拼接内容 = %q，want AAAABBBBCCCC", got)
	}
}

func TestCollectHLSUploadFilesExcludesKey(t *testing.T) {
	dir := t.TempDir()

	// 模拟加密 HLS 产物：2 个分片 + m3u8 + enc.key
	content := map[string]string{
		"seg_00000.ts": "SEG0",
		"seg_00001.ts": "SEG1",
		"index.m3u8":   "M3U8",
		hlsKeyFileName: "0123456789abcdef", // 16 字节密钥
	}
	for name, data := range content {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatalf("写文件 %s 失败: %v", name, err)
		}
	}

	files, totalSize, keySkipped, err := collectHLSUploadFiles(dir)
	if err != nil {
		t.Fatalf("收集待上传文件失败: %v", err)
	}

	if !keySkipped {
		t.Fatal("存在 enc.key 时应标记 keySkipped=true")
	}

	if len(files) != 3 {
		t.Fatalf("待上传文件数 = %d，want 3（应剔除 enc.key）", len(files))
	}

	for _, f := range files {
		if f.name == hlsKeyFileName {
			t.Fatal("enc.key 出现在待上传列表中")
		}
	}

	// m3u8 必须排最后
	if files[len(files)-1].name != "index.m3u8" {
		t.Fatalf("最后一个文件 = %q，want index.m3u8", files[len(files)-1].name)
	}

	// 总字节不含密钥（4+4+4=12）
	if totalSize != 12 {
		t.Fatalf("待上传总字节 = %d，want 12（不应计入 enc.key）", totalSize)
	}
}

func TestCollectHLSUploadFilesNoKey(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{"seg_00000.ts", "index.m3u8"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("写文件失败: %v", err)
		}
	}

	files, _, keySkipped, err := collectHLSUploadFiles(dir)
	if err != nil {
		t.Fatalf("收集失败: %v", err)
	}
	if keySkipped {
		t.Fatal("目录无 enc.key，不应标记 keySkipped")
	}
	if len(files) != 2 {
		t.Fatalf("文件数 = %d，want 2", len(files))
	}
}

func TestCollectHLSUploadFilesMissingDir(t *testing.T) {
	if _, _, _, err := collectHLSUploadFiles(filepath.Join(t.TempDir(), "not-exist")); err == nil {
		t.Fatal("目录不存在时应返回错误")
	}
}

func TestQuoteCommandArg(t *testing.T) {
	cases := []struct {
		name string
		arg  string
		want string
	}{
		{"纯安全字符", "-c", "-c"},
		{"普通路径", `C:\ffmpeg\bin\ffmpeg.exe`, `C:\ffmpeg\bin\ffmpeg.exe`},
		{"含空格", `C:\Program Files\ffmpeg\bin\ffmpeg.exe`,
			`"C:\\Program Files\\ffmpeg\\bin\\ffmpeg.exe"`},
		{"含括号", `H:\videos\my movie (v2)\out.mp4`,
			`"H:\\videos\\my movie (v2)\\out.mp4"`},
		{"含双引号", `say "hi"`, `"say \"hi\""`},
		{"含特殊字符&", "a&b", `"a&b"`},
		{"空串", "", `""`},
		{"换行转空格", "a\nb", `"a b"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := quoteCommandArg(tc.arg); got != tc.want {
				t.Fatalf("quoteCommandArg(%q) = %q，want %q", tc.arg, got, tc.want)
			}
		})
	}
}

func TestLogFFmpegCommandQuoted(t *testing.T) {
	// 验证 logFFmpegCommand 等价的拼接效果（含空格/括号路径引用，安全参数裸写）
	parts := []string{quoteCommandArg(`D:\My Tools\ffmpeg.exe`)}
	for _, a := range []string{"-y", `-i`, `H:\My Videos (new)\in.raw.ts`, "-c", "copy"} {
		parts = append(parts, quoteCommandArg(a))
	}
	captured := strings.Join(parts, " ")

	if !strings.Contains(captured, `"D:\\My Tools\\ffmpeg.exe"`) {
		t.Fatalf("ffmpeg 路径未加引号: %s", captured)
	}
	if !strings.Contains(captured, `"H:\\My Videos (new)\\in.raw.ts"`) {
		t.Fatalf("含括号路径未加引号: %s", captured)
	}
	// 安全参数不加引号
	if !strings.Contains(captured, " -y -i ") || !strings.Contains(captured, " -c copy") {
		t.Fatalf("安全参数不应加引号: %s", captured)
	}
}
