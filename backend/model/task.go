package model

import (
	"time"
)

type TaskStatus string

const (
	StatusPending     TaskStatus = "pending"
	StatusDownloading TaskStatus = "downloading"
	StatusMerging     TaskStatus = "merging"
	StatusCompressing TaskStatus = "compressing" // 码率压缩（FFmpeg 转码，CPU 密集）
	StatusPacking     TaskStatus = "packing"     // 二次 HLS 分片（FFmpeg，CPU 密集）
	StatusPaused      TaskStatus = "paused"
	StatusUploading   TaskStatus = "uploading"
	StatusCompleted   TaskStatus = "completed"
	StatusFailed      TaskStatus = "failed"
)

type RetryMode string

const (
	RetryModeMissing    RetryMode = "retry_missing"
	RetryModeRedownload RetryMode = "full_redownload"
	RetryModeForceMerge RetryMode = "force_merge"
)

type Task struct {
	ID                 string     `json:"id"`
	URL                string     `json:"url"`
	Name               string     `json:"name"`
	Status             TaskStatus `json:"status"`
	Progress           float64    `json:"progress"`
	Speed              string     `json:"speed"`
	TotalSegments      int        `json:"totalSegments"`             // M3U8: 分片总数
	DownloadedSegments int        `json:"downloadedSegments"`        // M3U8: 已下载分片数
	TotalBytes         int64      `json:"totalBytes,omitempty"`      // 通用视频: 总字节数
	DownloadedBytes    int64      `json:"downloadedBytes,omitempty"` // 通用视频: 已下载字节数
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
	KeyPath            string     `json:"keyPath,omitempty"` // 二次 HLS 加密密钥本地路径（供下载，随任务删除）
	PlayURL            string     `json:"playUrl,omitempty"` // 上传完成后可播放的 m3u8 远程地址（二次 HLS）
	WorkDir            string     `json:"workDir,omitempty"` // 本次下载工作目录（cache/产物所在），重试/强制合并据此定位，避免目录错位
}

type DownloadRequest struct {
	URL               string    `json:"url"`
	ThreadCount       int       `json:"threadCount"`
	OutputName        string    `json:"outputName"`
	HostType          string    `json:"hostType"`
	Cookie            string    `json:"cookie"`
	Referer           string    `json:"referer"`
	AutoClear         bool      `json:"autoClear"`
	SavePath          string    `json:"savePath"`
	EnableWebDAV      bool      `json:"enableWebDAV"`
	WebDAVURL         string    `json:"webDAVURL"`
	WebDAVUsername    string    `json:"webDAVUsername"`
	WebDAVPassword    string    `json:"webDAVPassword"`
	WebDAVRemoteDir   string    `json:"webDAVRemoteDir"`
	DeleteAfterUpload bool      `json:"deleteAfterUpload"`
	RetryMode         RetryMode `json:"retryMode"`
	// NodeOrigin 服务端节点源地址（scheme://host），由 HTTP 入口填充，写入 single 产物的 meta.json
	NodeOrigin string `json:"-"`
}

type APIResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type Settings struct {
	DefaultThreadCount  int    `json:"defaultThreadCount"`
	DefaultOutputName   string `json:"defaultOutputName"`
	DefaultSavePath     string `json:"defaultSavePath"`
	AutoClear           bool   `json:"autoClear"`
	HostType            string `json:"hostType"`
	TaskSortOrder       string `json:"taskSortOrder"`
	DefaultReferer      string `json:"defaultReferer"`
	DiskRefreshInterval int    `json:"diskRefreshInterval"`

	// WebDAV 上传配置（嵌套分组，JSON 保持扁平）
	WebDAVSettings
	// 下载完成动作：合并 / 二次 HLS 分片
	PostDownloadSettings
	// 合并后高码率压缩（可选，默认关闭，默认阈值 2048kbps）
	CompressSettings
	HLSPackSettings
	// 队列并发控制
	QueueSettings
	// 预下载磁盘检查
	PreDownloadSettings
}

// FFmpeg 编码模式（FFmpeg 处理封装时生效）
const (
	MuxModeCopy = "copy" // 源流式复制（-c copy，不重新编码）
	MuxModeH264 = "h264" // 转码为 H.264
	MuxModeH265 = "h265" // 转码为 H.265
)

// 合并方式
const (
	MergeMethodAuto    = "auto"    // 自动：检测 FFmpeg，兜底 Go（默认）
	MergeMethodFFmpeg  = "ffmpeg"  // 仅 FFmpeg：失败不回退 Go
	MergeMethodGomedia = "gomedia" // 仅 Go：不使用 FFmpeg
)

// PostDownloadSettings 下载完成动作中的合并配置
type PostDownloadSettings struct {
	// MergeAfterDownload 下载完成后合并，默认开启；关闭时保留 TS 分片且不允许后续动作
	MergeAfterDownload bool `json:"mergeAfterDownload"`
	// MergeMethod 主动指定合并方式：auto / ffmpeg / gomedia
	MergeMethod string `json:"mergeMethod"`
	// FFmpegMuxMode FFmpeg 处理时的编码模式：copy / h264 / h265
	FFmpegMuxMode string `json:"ffmpegMuxMode"`
}

// 默认高码率压缩阈值（kbps）
const DefaultCompressBitrateThreshold = 2048

// CompressSettings 合并完成后的码率检查与压缩配置
type CompressSettings struct {
	// CompressAfterMerge 合并完成后检查码率，超阈值则压缩；默认关闭（可选不强制）
	CompressAfterMerge bool `json:"compressAfterMerge"`
	// CompressBitrateThreshold 触发压缩的码率阈值（kbps），默认 2048
	CompressBitrateThreshold int `json:"compressBitrateThreshold"`
	// CompressTargetBitrate 压缩目标视频码率（kbps）；0 表示取阈值本身
	CompressTargetBitrate int `json:"compressTargetBitrate"`
}

// HLS 加密密钥来源
const (
	HLSEncryptGenerated = "generated" // 生成独立文件密钥（每任务一个随机密钥）
	HLSEncryptSpecified = "specified" // 指定密钥：输入 key 文件 URL 下载后加密
)

// HLS 打包产物形态（可选，默认保持多文件目录；single 为新增能力，不替换原有模式）
const (
	HLSPackFormMulti  = "multi"  // 多文件目录：index.m3u8 + 多个 seg_xxxxx.ts
	HLSPackFormSingle = "single" // 单文件：index.m3u8(BYTERANGE) + 一个 tsbin + meta.json
)

// HLSPackSettings 二次 HLS 分片与加密配置
type HLSPackSettings struct {
	// HLSPackEnabled 将合并产物再次切片为 HLS（依赖 FFmpeg）
	HLSPackEnabled bool `json:"hlsPackEnabled"`
	// HLSEncryptEnabled 二次 HLS 分片可选加密（AES-128）
	HLSEncryptEnabled bool `json:"hlsEncryptEnabled"`
	// HLSEncryptMode 密钥来源：generated / specified
	HLSEncryptMode string `json:"hlsEncryptMode"`
	// HLSKeyURL specified 模式下 key 文件的 URL
	HLSKeyURL string `json:"hlsKeyURL"`
	// HLSPackForm 产物形态：multi（默认）/ single（单 tsbin，可选）
	HLSPackForm string `json:"hlsPackForm"`
}

// NormalizeMuxMode 合法化编码模式，非法值回退为源流式复制（严格匹配，大小写敏感）
func NormalizeMuxMode(mode string) string {
	switch mode {
	case MuxModeH264, MuxModeH265:
		return mode
	default:
		return MuxModeCopy
	}
}

// NormalizeMergeMethod 合法化合并方式，非法值回退为自动（严格匹配，大小写敏感）
func NormalizeMergeMethod(method string) string {
	switch method {
	case MergeMethodFFmpeg, MergeMethodGomedia:
		return method
	default:
		return MergeMethodAuto
	}
}

// NormalizeHLSEncryptMode 合法化加密密钥来源，非法值回退为生成独立密钥（严格匹配）
func NormalizeHLSEncryptMode(mode string) string {
	if mode == HLSEncryptSpecified {
		return HLSEncryptSpecified
	}
	return HLSEncryptGenerated
}

// NormalizeRetryMode 合法化重试模式：仅三种常量有效；空串与未知值统一回退缺失分片重试。
func NormalizeRetryMode(mode RetryMode) RetryMode {
	switch mode {
	case RetryModeMissing, RetryModeRedownload, RetryModeForceMerge:
		return mode
	default:
		return RetryModeMissing
	}
}

// NormalizeHLSPackForm 合法化产物形态，非法值回退为多文件目录（默认行为，严格匹配）
func NormalizeHLSPackForm(form string) string {
	if form == HLSPackFormSingle {
		return HLSPackFormSingle
	}
	return HLSPackFormMulti
}

// WebDAVSettings WebDAV 上传相关配置
type WebDAVSettings struct {
	EnableWebDAV      bool   `json:"enableWebDAV"`
	WebDAVURL         string `json:"webDAVURL"`
	WebDAVUsername    string `json:"webDAVUsername"`
	WebDAVPassword    string `json:"webDAVPassword"`
	WebDAVRemoteDir   string `json:"webDAVRemoteDir"`
	DeleteAfterUpload bool   `json:"deleteAfterUpload"`
}

// QueueSettings 下载 / 合并 / 上传 队列并发控制
type QueueSettings struct {
	DownloadConcurrency int  `json:"downloadConcurrency"`
	MergeConcurrency    int  `json:"mergeConcurrency"`
	CompressConcurrency int  `json:"compressConcurrency"`
	PackConcurrency     int  `json:"packConcurrency"`
	UploadConcurrency   int  `json:"uploadConcurrency"`
	SingleMode          bool `json:"singleMode"`
}

// PreDownloadSettings 预下载磁盘空间检查
type PreDownloadSettings struct {
	EnablePreDownloadCheck bool `json:"enablePreDownloadCheck"`
	MinFreeSpaceMB         int  `json:"minFreeSpaceMB"`
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
