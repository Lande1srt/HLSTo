package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"m3u8-downloader-web/storage"
	"m3u8-downloader-web/websocket"
)

// TestMergeWithFFmpegMissingLaterHeaders 端到端复现用户故障：
// 真实故障源特征是「首片含 SPS/PPS，后续分片开头无参数集」——
// 用 HLS muxer + repeat-headers=0 生成此类分片。
// 验证：旧 concat demuxer 直接处理失败；预拼接 + FFmpeg 单文件处理成功。
// 环境没有 ffmpeg 可执行文件时自动跳过。
func TestMergeWithFFmpegMissingLaterHeaders(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		// 回退候选：FFMPEG_PATH 环境变量；项目私有安装 backend/ffmpeg/bin
		candidates := []string{
			os.Getenv("FFMPEG_PATH"),
			filepath.Join("..", "ffmpeg", "bin", ffmpegBinaryName()),
		}
		for _, candidate := range candidates {
			if candidate == "" {
				continue
			}
			abs, absErr := filepath.Abs(candidate)
			if absErr != nil {
				continue
			}
			if _, statErr := os.Stat(abs); statErr == nil {
				ffmpegPath = abs
				err = nil
				break
			}
		}
	}
	if err != nil {
		t.Skip("未找到 ffmpeg（PATH、FFMPEG_PATH 与私有目录均无），跳过集成测试")
	}

	withTempWd(t)
	workDir := t.TempDir()

	// 1. 生成故障型 HLS 分片：首片带 SPS/PPS，后续片不带
	piecePattern := filepath.Join(workDir, "piece_%03d.ts")
	indexPath := filepath.Join(workDir, "index.m3u8")
	gen := exec.Command(ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=20:size=320x240:rate=25",
		"-c:v", "libx264", "-x264-params", "repeat-headers=0",
		"-f", "hls",
		"-hls_time", "2",
		"-hls_list_size", "0",
		"-hls_flags", "independent_segments",
		"-hls_segment_filename", piecePattern,
		indexPath)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("生成故障型 HLS 分片失败: %v | %s", err, out)
	}

	// 2. 收集分片（与生产 collectTsFiles 同规则）
	entries, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatalf("读取工作目录失败: %v", err)
	}
	var pieces []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "piece_") && filepath.Ext(e.Name()) == ".ts" {
			pieces = append(pieces, filepath.Join(workDir, e.Name()))
		}
	}
	sort.Strings(pieces)
	if len(pieces) < 2 {
		t.Skipf("仅产生 %d 个分片，无法模拟故障场景", len(pieces))
	}

	// 3. 对照组：旧 concat demuxer 直接处理这些分片应失败（证明修复针对性）
	listPath := filepath.Join(workDir, "list.txt")
	var sb strings.Builder
	for _, p := range pieces {
		sb.WriteString("file '")
		sb.WriteString(strings.ReplaceAll(p, "'", "'\\''"))
		sb.WriteString("'\n")
	}
	if err := os.WriteFile(listPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("写 concat 清单失败: %v", err)
	}
	refOut := filepath.Join(workDir, "ref.mp4")
	ref := exec.Command(ffmpegPath,
		"-hide_banner", "-loglevel", "error", "-y",
		"-f", "concat", "-safe", "0", "-i", listPath,
		"-c", "copy", refOut)
	if refErr := ref.Run(); refErr == nil {
		// 个别 ffmpeg 版本可能容忍；容忍则对照组无意义但不阻塞主验证
		t.Log("对照组 concat demuxer 意外成功（该版本容错），继续验证主流程")
	} else {
		t.Logf("对照组确认 concat demuxer 处理失败: %v（符合故障特征）", refErr)
	}

	// 4. 构造真实服务栈
	sqliteStorage, err := storage.NewSQLiteStorage()
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}
	t.Cleanup(func() { _ = sqliteStorage.Close() })

	tm := NewTaskManager(sqliteStorage)
	t.Cleanup(tm.Close)

	ds := NewDownloaderService(tm, websocket.NewWebSocketManager())

	// 5. 执行合并（copy 模式，内部预拼接）
	mvName := filepath.Join(workDir, "output.mp4")
	success, stopped := ds.mergeWithFFmpeg("integration-task", ffmpegPath,
		"copy", pieces, mvName, 0, nil)

	if stopped {
		t.Fatal("合并被意外判定为用户停止")
	}
	if !success {
		t.Fatal("故障型分片合并失败（预拼接修复未生效）")
	}

	info, err := os.Stat(mvName)
	if err != nil || info.Size() == 0 {
		t.Fatalf("输出 MP4 异常（不存在或为空）: %v", err)
	}

	// 预拼接临时文件必须清理
	rawPath := filepath.Join(workDir, "output.raw.ts")
	if _, err := os.Stat(rawPath); !os.IsNotExist(err) {
		t.Fatal("预拼接临时文件 raw.ts 未被清理")
	}
}
