package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"m3u8-downloader-web/model"
)

// TestTaskCRUDRoundTrip 覆盖 AddTask/UpdateTask/GetTask/GetAllTasks 的列与占位符一致性。
// 历史 bug：INSERT 占位符数量与列数不匹配（26 vs 25），新任务落库直接失败。
func TestTaskCRUDRoundTrip(t *testing.T) {
	dir := t.TempDir()
	orig, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(orig)

	s, err := NewSQLiteStorage()
	if err != nil {
		t.Fatalf("初始化存储失败: %v", err)
	}
	defer s.Close()

	workDir := filepath.Join(dir, "download_001")
	task := &model.Task{
		ID:                 "task-1",
		URL:                "http://example.com/a.m3u8",
		Name:               "movie",
		Status:             model.StatusDownloading,
		Progress:           12.5,
		Speed:              "1 MB/s",
		ThreadCount:        8,
		HostType:           "v1",
		CreatedAt:          time.Now().Truncate(time.Second),
		KeyPath:            filepath.Join(dir, "keys", "task-1.key"),
		PlayURL:            "http://example.com/movie.m3u8",
		WorkDir:            workDir,
		NodeOrigin:         "http://node.example",
		MergeAfterDownload: true, MergeMethod: model.MergeMethodGomedia, FFmpegMuxMode: model.MuxModeH265,
		CompressAfterMerge: true, CompressBitrateThreshold: 4096, CompressTargetBitrate: 3000,
		HLSPackEnabled: true, HLSEncryptEnabled: true, HLSEncryptMode: model.HLSEncryptSpecified,
		HLSKeyURL: "http://example.com/enc.key", HLSPackForm: model.HLSPackFormSingle,
		EnableChunkedUpload: true,
	}

	// AddTask：列/占位符/参数数量不匹配会在此直接报错
	if err := s.AddTask(task); err != nil {
		t.Fatalf("AddTask 失败: %v", err)
	}

	// GetTask：含 WorkDir/KeyPath/PlayURL 全字段回读
	got, err := s.GetTask("task-1")
	if err != nil {
		t.Fatalf("GetTask 失败: %v", err)
	}
	if got == nil {
		t.Fatal("GetTask 返回 nil")
	}
	if got.WorkDir != workDir {
		t.Errorf("WorkDir = %q, want %q", got.WorkDir, workDir)
	}
	if got.KeyPath != task.KeyPath {
		t.Errorf("KeyPath = %q, want %q", got.KeyPath, task.KeyPath)
	}
	if got.PlayURL != task.PlayURL {
		t.Errorf("PlayURL = %q, want %q", got.PlayURL, task.PlayURL)
	}
	if got.ThreadCount != 8 {
		t.Errorf("ThreadCount = %d", got.ThreadCount)
	}
	if !got.MergeAfterDownload || got.MergeMethod != model.MergeMethodGomedia || got.FFmpegMuxMode != model.MuxModeH265 ||
		!got.CompressAfterMerge || got.CompressBitrateThreshold != 4096 || got.CompressTargetBitrate != 3000 ||
		!got.HLSPackEnabled || !got.HLSEncryptEnabled || got.HLSEncryptMode != model.HLSEncryptSpecified ||
		got.HLSKeyURL != task.HLSKeyURL || got.HLSPackForm != model.HLSPackFormSingle ||
		!got.EnableChunkedUpload || got.NodeOrigin != task.NodeOrigin {
		t.Errorf("任务配置快照回读不完整: %+v", got)
	}

	// GetAllTasks
	all, err := s.GetAllTasks()
	if err != nil {
		t.Fatalf("GetAllTasks 失败: %v", err)
	}
	if len(all) != 1 || all[0].WorkDir != workDir {
		t.Fatalf("GetAllTasks 异常: %+v", all)
	}

	// UpdateTask：work_dir 等列正确写回
	completed := time.Now().Truncate(time.Second)
	got.Status = model.StatusCompleted
	got.OutputPath = filepath.Join(workDir, "movie.mp4")
	got.Progress = 100
	got.CompletedAt = &completed
	if err := s.UpdateTask(got); err != nil {
		t.Fatalf("UpdateTask 失败: %v", err)
	}

	again, err := s.GetTask("task-1")
	if err != nil {
		t.Fatalf("更新后 GetTask 失败: %v", err)
	}
	if again.Status != model.StatusCompleted || again.OutputPath != got.OutputPath {
		t.Errorf("更新未生效: status=%s output=%s", again.Status, again.OutputPath)
	}
	if again.WorkDir != workDir {
		t.Errorf("更新后 WorkDir 丢失: %q", again.WorkDir)
	}
	if again.CompletedAt == nil {
		t.Errorf("CompletedAt 未持久化")
	}
	if !again.EnableChunkedUpload || again.HLSPackForm != model.HLSPackFormSingle || again.NodeOrigin != task.NodeOrigin {
		t.Errorf("更新后配置快照丢失: %+v", again)
	}
}
