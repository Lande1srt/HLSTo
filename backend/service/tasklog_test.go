package service

import (
	"strings"
	"testing"

	"m3u8-downloader-web/storage"
)

func TestScanProgressBlocks(t *testing.T) {
	// 模拟 ffmpeg -progress pipe:1 的真实输出
	stream := strings.NewReader(strings.Join([]string{
		"frame=42",
		"fps=59.9",
		"bitrate=1024.0kbits/s",
		"speed=1.0x",
		"out_time=00:00:01.000000",
		"out_time_us=1000000",
		"progress=continue",
		"frame=84",
		"fps=60.1",
		"out_time_us=2000000",
		"progress=end",
		"malformed-line-without-separator",
		"trailing=incomplete", // 进程结束前未以 progress= 收尾，不回调
	}, "\n"))

	var blocks []map[string]string
	scanProgressBlocks(stream, func(block map[string]string) {
		// 拷贝，避免实现复用 map 造成前后块互相覆盖
		copied := make(map[string]string, len(block))
		for k, v := range block {
			copied[k] = v
		}
		blocks = append(blocks, copied)
	})

	if len(blocks) != 2 {
		t.Fatalf("解析出 %d 个统计块，want 2", len(blocks))
	}

	b1 := blocks[0]
	if b1["frame"] != "42" || b1["fps"] != "59.9" ||
		b1["bitrate"] != "1024.0kbits/s" || b1["speed"] != "1.0x" ||
		b1["out_time_us"] != "1000000" {
		t.Fatalf("第 1 块字段异常: %#v", b1)
	}

	b2 := blocks[1]
	if b2["frame"] != "84" || b2["out_time_us"] != "2000000" {
		t.Fatalf("第 2 块字段异常: %#v", b2)
	}

	// 空流不应回调
	called := false
	scanProgressBlocks(strings.NewReader(""), func(map[string]string) { called = true })
	if called {
		t.Fatal("空流不应产生回调")
	}
}

func TestTaskManagerAddLogThrottle(t *testing.T) {
	withTempWd(t)

	sqliteStorage, err := storage.NewSQLiteStorage()
	if err != nil {
		t.Fatalf("初始化测试存储失败: %v", err)
	}
	tm := NewTaskManager(sqliteStorage)

	// 同任务 debug：首条落盘，2 秒内第二条被节流丢弃
	tm.AddLog("task-1", "debug", "debug-1")
	tm.AddLog("task-1", "debug", "debug-2-throttled")

	// info 不受节流影响
	tm.AddLog("task-1", "info", "info-1")

	// warn/error 直通
	tm.AddLog("task-1", "warn", "warn-1")
	tm.AddLog("task-1", "error", "error-1")

	// 不同任务独立节流
	tm.AddLog("task-2", "debug", "debug-task2-1")

	// Close 等待持久化通道全部消费完
	tm.Close()

	logs1, err := sqliteStorage.GetLogs("task-1")
	if err != nil {
		t.Fatalf("查询 task-1 日志失败: %v", err)
	}
	if len(logs1) != 4 {
		msgs := make([]string, 0, len(logs1))
		for _, e := range logs1 {
			msgs = append(msgs, e.Level+":"+e.Message)
		}
		t.Fatalf("task-1 日志数 = %d，want 4（debug节流应丢弃1条），实际: %v",
			len(logs1), msgs)
	}

	logs2, err := sqliteStorage.GetLogs("task-2")
	if err != nil {
		t.Fatalf("查询 task-2 日志失败: %v", err)
	}
	if len(logs2) != 1 || logs2[0].Message != "debug-task2-1" {
		t.Fatalf("task-2 日志异常: %#v", logs2)
	}

	if err := sqliteStorage.Close(); err != nil {
		t.Fatalf("关闭存储失败: %v", err)
	}
}
