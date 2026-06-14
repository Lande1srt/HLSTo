package model

import "time"

type TaskStatus string

const (
	StatusPending     TaskStatus = "pending"
	StatusDownloading TaskStatus = "downloading"
	StatusMerging     TaskStatus = "merging"
	StatusPaused      TaskStatus = "paused"
	StatusUploading   TaskStatus = "uploading"
	StatusCompleted   TaskStatus = "completed"
	StatusFailed      TaskStatus = "failed"
)

type RetryMode string

const (
	RetryModeMissing     RetryMode = "retry_missing"
	RetryModeRedownload  RetryMode = "full_redownload"
	RetryModeForceMerge  RetryMode = "force_merge"
)

type Task struct {
	ID                 string     `json:"id"`
	URL                string     `json:"url"`
	Name               string     `json:"name"`
	Status             TaskStatus `json:"status"`
	Progress           float64    `json:"progress"`
	Speed              string     `json:"speed"`
	TotalSegments      int        `json:"totalSegments"`
	DownloadedSegments int        `json:"downloadedSegments"`
	OutputPath         string     `json:"outputPath,omitempty"`
	Error              string     `json:"error,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
	CompletedAt        *time.Time `json:"completedAt,omitempty"`
	ThreadCount        int        `json:"threadCount"`
	HostType           string     `json:"hostType"` 
	Cookie             string     `json:"cookie"` 
	Referer            string     `json:"referer"` 
	AutoClear          bool       `json:"autoClear"`
	SavePath           string     `json:"savePath"`
	EnableWebDAV       bool       `json:"enableWebDAV"`
	WebDAVURL          string     `json:"webDAVURL"`
	WebDAVUsername     string     `json:"webDAVUsername"`
	WebDAVPassword     string     `json:"webDAVPassword"`
	WebDAVRemoteDir    string     `json:"webDAVRemoteDir"`
	DeleteAfterUpload  bool       `json:"deleteAfterUpload"`
}

type DownloadRequest struct {
	URL             string     `json:"url"`
	ThreadCount     int        `json:"threadCount"`
	OutputName      string     `json:"outputName"`
	HostType        string     `json:"hostType"` 
	Cookie          string     `json:"cookie"` 
	Referer         string     `json:"referer"` 
	AutoClear       bool       `json:"autoClear"`
	SavePath        string     `json:"savePath"`
	EnableWebDAV    bool       `json:"enableWebDAV"`
	WebDAVURL       string     `json:"webDAVURL"`
	WebDAVUsername  string     `json:"webDAVUsername"`
	WebDAVPassword  string     `json:"webDAVPassword"`
	WebDAVRemoteDir string     `json:"webDAVRemoteDir"`
	DeleteAfterUpload bool     `json:"deleteAfterUpload"`
	RetryMode       RetryMode  `json:"retryMode"`
}

type APIResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type Settings struct {
	DefaultThreadCount int    `json:"defaultThreadCount"`
	DefaultOutputName  string `json:"defaultOutputName"`
	DefaultSavePath    string `json:"defaultSavePath"`
	AutoClear          bool   `json:"autoClear"`
	HostType           string `json:"hostType"`
	EnableWebDAV       bool   `json:"enableWebDAV"`
	WebDAVURL          string `json:"webDAVURL"`
	WebDAVUsername     string `json:"webDAVUsername"`
	WebDAVPassword     string `json:"webDAVPassword"`
	WebDAVRemoteDir    string `json:"webDAVRemoteDir"`
	DeleteAfterUpload  bool   `json:"deleteAfterUpload"` 
	TaskSortOrder      string `json:"taskSortOrder"` // "desc" or "asc"
	DefaultReferer     string `json:"defaultReferer"` 
	
	// 队列控制配置
	DownloadConcurrency int  `json:"downloadConcurrency"` // 同时下载数量
	MergeConcurrency    int  `json:"mergeConcurrency"`    // 同时合并数量
	UploadConcurrency   int  `json:"uploadConcurrency"`   // 同时上传数量
	SingleMode          bool `json:"singleMode"`          // 单状态处理模式
	
	// 预下载检查配置
	EnablePreDownloadCheck bool `json:"enablePreDownloadCheck"` // 是否启用预下载检查
	MinFreeSpaceMB         int  `json:"minFreeSpaceMB"`         // 最小可用空间(MB)
	
	// 磁盘刷新配置
	DiskRefreshInterval int `json:"diskRefreshInterval"` // 磁盘信息自动刷新间隔(秒)，默认10秒，最大3600秒
}

type WebSocketMessage struct {
	Type               string      `json:"type"`
	TaskID             string      `json:"taskId,omitempty"`
	Progress           float64     `json:"progress,omitempty"`
	Speed              string      `json:"speed,omitempty"`
	Level              string      `json:"level,omitempty"`
	Message            string      `json:"message,omitempty"`
	Timestamp          string      `json:"timestamp"`
	Data               interface{} `json:"data,omitempty"`
	DownloadedSegments int         `json:"downloadedSegments,omitempty"`
	TotalSegments      int         `json:"totalSegments,omitempty"`
	Status             string      `json:"status,omitempty"`
	OutputPath         string      `json:"outputPath,omitempty"`
}

type LogEntry struct {
	Level     string `json:"level"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}
