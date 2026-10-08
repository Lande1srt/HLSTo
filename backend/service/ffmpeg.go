package service

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ulikunitz/xz"
)

// FFmpeg 检测来源
const (
	ffmpegSourceEnv     = "env"     // 环境变量 FFMPEG_PATH 指定
	ffmpegSourceSystem  = "system"  // 宿主机公共 PATH
	ffmpegSourcePrivate = "private" // 应用私有目录
)

// 私有安装阶段
const (
	ffmpegPhaseIdle        = "idle"
	ffmpegPhaseDownloading = "downloading"
	ffmpegPhaseExtracting  = "extracting"
	ffmpegPhaseVerifying   = "verifying"
)

// 相关环境变量名
const (
	envFFmpegPath        = "FFMPEG_PATH"         // 手动指定 ffmpeg 可执行文件路径
	envFFmpegDownloadURL = "FFMPEG_DOWNLOAD_URL" // 手动指定私有安装包地址（zip/tar.xz）
)

const (
	detectCacheTTL = 5 * time.Second // 检测结果缓存时间，避免高频执行外部进程
	probeTimeout   = 8 * time.Second // ffmpeg -version 探测超时
)

// versionRe 解析 "ffmpeg version 7.0.2-essentials_build ..." 中的版本号
var versionRe = regexp.MustCompile(`ffmpeg version (\S+)`)

// FFmpegStatus 描述 ffmpeg 的检测与安装状态
type FFmpegStatus struct {
	Available  bool   `json:"available"`           // 是否可用
	Source     string `json:"source"`              // 来源：env / system / private
	Path       string `json:"path"`                // 可执行文件绝对路径
	Version    string `json:"version"`             // 版本号
	PrivateDir string `json:"privateDir"`          // 私有安装目录
	Supported  bool   `json:"supported"`           // 当前平台/架构是否支持私有自动安装
	Installing bool   `json:"installing"`          // 是否正在安装
	Progress   int    `json:"progress"`            // 安装进度 0-100
	Phase      string `json:"phase"`               // 安装阶段
	Message    string `json:"message"`             // 安装状态描述
	LastError  string `json:"lastError,omitempty"` // 最近一次安装错误
}

// downloadSource 描述一个私有安装包来源
type downloadSource struct {
	url  string // 下载地址
	name string // 来源描述（日志展示）
}

// FFmpegService 负责 ffmpeg 的检测与私有安装
type FFmpegService struct {
	mu          sync.Mutex
	cached      *FFmpegStatus
	cachedAt    time.Time
	installing  bool
	phase       string
	progress    int
	message     string
	lastError   string
	installLock sync.Mutex // 串行化安装操作
}

// NewFFmpegService 创建 FFmpeg 服务，私有二进制位于工作目录下的 ffmpeg/bin
func NewFFmpegService() *FFmpegService {
	return &FFmpegService{
		phase:   ffmpegPhaseIdle,
		message: "尚未执行私有安装",
	}
}

// privateRoot 私有安装根目录：{工作目录}/ffmpeg
func (s *FFmpegService) privateRoot() (string, error) {
	pwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("获取工作目录失败: %w", err)
	}
	return filepath.Join(pwd, "ffmpeg"), nil
}

// privateBinary 私有目录中的 ffmpeg 可执行文件路径
func (s *FFmpegService) privateBinary() (string, error) {
	root, err := s.privateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "bin", ffmpegBinaryName()), nil
}

// ffmpegBinaryName 当前平台 ffmpeg 二进制文件名
func ffmpegBinaryName() string {
	if runtime.GOOS == "windows" {
		return "ffmpeg.exe"
	}
	return "ffmpeg"
}

// Status 返回 ffmpeg 综合状态；forceRefresh 为 true 时跳过检测缓存
func (s *FFmpegService) Status(forceRefresh bool) FFmpegStatus {
	s.mu.Lock()
	// 检测结果可缓存；安装中不执行外部探测，直接拼装
	needDetect := forceRefresh ||
		s.cached == nil ||
		time.Since(s.cachedAt) > detectCacheTTL
	if !needDetect && s.cached != nil {
		detected := *s.cached
		installing := s.installing
		phase := s.phase
		progress := s.progress
		message := s.message
		lastError := s.lastError
		s.mu.Unlock()

		detected.Installing = installing
		detected.Phase = phase
		detected.Progress = progress
		detected.Message = message
		detected.LastError = lastError
		return detected
	}
	s.mu.Unlock()

	detected := s.detect()

	s.mu.Lock()
	detected.Installing = s.installing
	detected.Phase = s.phase
	detected.Progress = s.progress
	detected.Message = s.message
	detected.LastError = s.lastError
	status := detected
	s.mu.Unlock()
	return status
}

// detect 按优先级检测 ffmpeg：环境变量 → 系统 PATH → 私有目录
func (s *FFmpegService) detect() FFmpegStatus {
	root, _ := s.privateRoot()
	status := FFmpegStatus{
		PrivateDir: root,
		Supported:  platformSupported(),
		Source:     "",
	}

	// 1. 环境变量 FFMPEG_PATH 显式指定，优先级最高
	if p := strings.TrimSpace(os.Getenv(envFFmpegPath)); p != "" {
		if version, ok := probeFFmpeg(p); ok {
			status.Available = true
			status.Source = ffmpegSourceEnv
			status.Path, _ = filepath.Abs(p)
			status.Version = version
			s.cacheDetect(&status)
			return status
		}
	}

	// 2. 宿主机公共环境变量 PATH
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		if version, ok := probeFFmpeg(p); ok {
			status.Available = true
			status.Source = ffmpegSourceSystem
			status.Path, _ = filepath.Abs(p)
			status.Version = version
			s.cacheDetect(&status)
			return status
		}
	}

	// 3. 应用私有目录
	if p, err := s.privateBinary(); err == nil {
		if version, ok := probeFFmpeg(p); ok {
			status.Available = true
			status.Source = ffmpegSourcePrivate
			status.Path, _ = filepath.Abs(p)
			status.Version = version
			s.cacheDetect(&status)
			return status
		}
	}

	s.cacheDetect(&status)
	return status
}

// cacheDetect 缓存检测结果
func (s *FFmpegService) cacheDetect(status *FFmpegStatus) {
	s.mu.Lock()
	copied := *status
	s.cached = &copied
	s.cachedAt = time.Now()
	s.mu.Unlock()
}

// probeFFmpeg 执行 ffmpeg -version 验证二进制可用性并解析版本号
func probeFFmpeg(binaryPath string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, binaryPath, "-version").Output()
	if err != nil {
		return "", false
	}
	return parseVersionOutput(out)
}

// parseVersionOutput 从 ffmpeg -version 输出中解析版本号
func parseVersionOutput(output []byte) (string, bool) {
	firstLine, _, _ := bytes.Cut(output, []byte("\n"))
	if m := versionRe.FindStringSubmatch(strings.TrimSpace(string(firstLine))); len(m) == 2 {
		return m[1], true
	}
	return "", false
}

// StartInstall 启动后台私有安装；若已在安装则返回错误
func (s *FFmpegService) StartInstall() error {
	s.installLock.Lock()
	defer s.installLock.Unlock()

	s.mu.Lock()
	if s.installing {
		s.mu.Unlock()
		return errors.New("ffmpeg 正在安装中，请等待当前安装完成")
	}
	if !platformSupported() {
		s.mu.Unlock()
		return fmt.Errorf("当前平台 %s/%s 暂不支持自动私有安装，请参照 ffmpeg 官网手动安装", runtime.GOOS, runtime.GOARCH)
	}
	s.installing = true
	s.phase = ffmpegPhaseDownloading
	s.progress = 0
	s.message = "准备下载安装包..."
	s.lastError = ""
	s.mu.Unlock()

	go s.runInstall()
	return nil
}

// runInstall 依次尝试各下载源，全部失败时记录汇总错误
func (s *FFmpegService) runInstall() {
	defer func() {
		s.mu.Lock()
		s.installing = false
		s.phase = ffmpegPhaseIdle
		s.mu.Unlock()
		// 安装结束后强制刷新检测缓存
		s.Status(true)
	}()

	sources, err := s.buildSources()
	if err != nil {
		s.mu.Lock()
		s.lastError = err.Error()
		s.message = "无法构建下载源"
		s.mu.Unlock()
		return
	}

	var failureMsgs []string
	for _, src := range sources {
		s.updateInstall(ffmpegPhaseDownloading, 0, fmt.Sprintf("正在从 %s 下载...", src.name))
		if err := s.installFromSource(src); err != nil {
			logMsg := fmt.Sprintf("%s 安装失败: %v", src.name, err)
			failureMsgs = append(failureMsgs, logMsg)
			s.mu.Lock()
			s.lastError = err.Error()
			s.mu.Unlock()
			continue
		}

		s.updateInstall(ffmpegPhaseVerifying, 100, "安装成功")
		return
	}

	s.mu.Lock()
	s.message = "所有下载源均安装失败"
	s.lastError = strings.Join(failureMsgs, "\n")
	s.mu.Unlock()
}

// updateInstall 更新安装进度
func (s *FFmpegService) updateInstall(phase string, progress int, message string) {
	s.mu.Lock()
	s.phase = phase
	s.progress = progress
	s.message = message
	s.mu.Unlock()
}

// installFromSource 完成 下载 → 解压 → 切换 → 验证 的完整流程
func (s *FFmpegService) installFromSource(src downloadSource) error {
	root, err := s.privateRoot()
	if err != nil {
		return err
	}

	// 下载到临时文件，任何失败或结束都清理
	archivePath, err := s.downloadArchive(src)
	if err != nil {
		return err
	}
	defer os.Remove(archivePath)

	// 解压到临时暂存目录（布局：staging/bin/ffmpeg）
	stagingDir, err := os.MkdirTemp(filepath.Dir(root), "ffmpeg-staging-*")
	if err != nil {
		return fmt.Errorf("创建暂存目录失败: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	stagingBin := filepath.Join(stagingDir, "bin")
	if err := os.MkdirAll(stagingBin, 0o755); err != nil {
		return fmt.Errorf("创建暂存 bin 目录失败: %w", err)
	}

	s.updateInstall(ffmpegPhaseExtracting, 0, "正在解压安装包...")
	if err := extractArchive(archivePath, stagingBin); err != nil {
		return err
	}

	stagingBinary := filepath.Join(stagingBin, ffmpegBinaryName())
	if _, err := os.Stat(stagingBinary); err != nil {
		return fmt.Errorf("解压后未找到 ffmpeg 可执行文件: %w", err)
	}

	// 验证暂存二进制可用
	s.updateInstall(ffmpegPhaseVerifying, 95, "正在验证 ffmpeg...")
	version, ok := probeFFmpeg(stagingBinary)
	if !ok {
		return errors.New("下载的 ffmpeg 无法执行或版本验证失败")
	}

	// 原子切换私有目录：旧目录改名备份 → 新目录就位 → 删除备份
	if err := replacePrivateRoot(root, stagingDir); err != nil {
		return fmt.Errorf("切换私有目录失败: %w", err)
	}

	s.updateInstall(ffmpegPhaseVerifying, 100, fmt.Sprintf("FFmpeg %s 私有安装完成", version))
	return nil
}

// downloadArchive 下载安装包并返回临时文件路径
func (s *FFmpegService) downloadArchive(src downloadSource) (string, error) {
	root, err := s.privateRoot()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("创建私有目录失败: %w", err)
	}

	tmp, err := os.CreateTemp(filepath.Dir(root), "ffmpeg-archive-*.download")
	if err != nil {
		return "", fmt.Errorf("创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}

	req, err := http.NewRequest(http.MethodGet, src.url, nil)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("构建下载请求失败: %w", err)
	}

	// 大包不设整体超时（80MB 慢速网络下不能误杀），仅限制响应头等待时间，
	// 防止服务器接受连接后长期静默导致 goroutine 永久阻塞
	client := &http.Client{
		Transport: &http.Transport{
			ResponseHeaderTimeout: 30 * time.Second,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		cleanup()
		return "", fmt.Errorf("下载请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		cleanup()
		return "", fmt.Errorf("下载返回异常状态码: %s", resp.Status)
	}

	// 带进度地写入临时文件
	writer := &progressWriter{
		writer: tmp,
		total:  resp.ContentLength,
		onPct: func(pct int) {
			// 下载占整体安装进度的 0-90%
			s.updateInstall(ffmpegPhaseDownloading, pct*9/10,
				fmt.Sprintf("正在下载... %d%%", pct))
		},
	}

	buf := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(writer, resp.Body, buf); err != nil {
		cleanup()
		return "", fmt.Errorf("写入安装包失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("关闭安装包文件失败: %w", err)
	}

	return tmpPath, nil
}

// progressWriter 包装下载写入，按百分比整数回调
type progressWriter struct {
	writer  io.Writer
	total   int64
	written int64
	lastPct int
	onPct   func(pct int)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.writer.Write(b)
	p.written += int64(n)
	if p.total > 0 && p.onPct != nil {
		pct := int(float64(p.written) / float64(p.total) * 100)
		if pct > 100 {
			pct = 100
		}
		if pct > p.lastPct {
			p.lastPct = pct
			p.onPct(pct)
		}
	}
	return n, err
}

// replacePrivateRoot 将 staging 目录原子替换为正式私有目录
func replacePrivateRoot(root, stagingDir string) error {
	backup := root + ".bak"
	// 清理可能残留的备份
	if _, err := os.Stat(backup); err == nil {
		if err := os.RemoveAll(backup); err != nil {
			return fmt.Errorf("清理旧备份失败: %w", err)
		}
	}

	hadOld := false
	if _, err := os.Stat(root); err == nil {
		hadOld = true
		// Windows 下无法 Rename 到已存在目标，故先改名而非直接覆盖
		if err := os.Rename(root, backup); err != nil {
			return fmt.Errorf("备份旧版本失败: %w", err)
		}
	}

	if err := os.Rename(stagingDir, root); err != nil {
		// 新版本就位失败，尽量回滚旧版本
		if hadOld {
			_ = os.Rename(backup, root)
		}
		return fmt.Errorf("新版本就位失败: %w", err)
	}

	if hadOld {
		if err := os.RemoveAll(backup); err != nil {
			// 备份删除失败不影响使用，仅提示
			return nil
		}
	}
	return nil
}

// extractArchive 解压归档并提取 ffmpeg/ffprobe 到 bin 目录；
// 以文件魔数判定真实类型，避免 URL 后缀与内容不一致
func extractArchive(archivePath, binDir string) error {
	kind, err := sniffArchive(archivePath)
	if err != nil {
		return err
	}

	switch kind {
	case "zip":
		return extractZip(archivePath, binDir)
	case "tarxz":
		return extractTarXZ(archivePath, binDir)
	default:
		return fmt.Errorf("不支持的归档类型: %s", kind)
	}
}

// sniffArchive 读取文件头魔数识别 zip / xz
func sniffArchive(archivePath string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("打开安装包失败: %w", err)
	}
	defer f.Close()

	header := make([]byte, 6)
	if _, err := io.ReadFull(f, header); err != nil {
		return "", fmt.Errorf("读取安装包头失败: %w", err)
	}

	switch {
	case header[0] == 0x50 && header[1] == 0x4b: // PK
		return "zip", nil
	case header[0] == 0xfd && string(header[1:6]) == "7zXZ\x00":
		return "tarxz", nil
	default:
		return "", errors.New("无法识别的安装包格式（仅支持 zip / tar.xz）")
	}
}

// isWantedBinary 判断归档条目是否为需要提取的 ffmpeg/ffprobe
func isWantedBinary(base string) bool {
	switch base {
	case "ffmpeg", "ffmpeg.exe", "ffprobe", "ffprobe.exe":
		return true
	}
	return false
}

// extractZip 解压 zip，提取 ffmpeg/ffprobe
func extractZip(archivePath, binDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("打开 zip 失败: %w", err)
	}
	defer zr.Close()

	found := false
	for _, f := range zr.File {
		base := path.Base(filepath.ToSlash(f.Name))
		if !isWantedBinary(base) {
			continue
		}
		// 仅提取常规文件，跳过目录与符号链接，防止 zip slip 与链接逃逸
		if f.FileInfo().IsDir() || f.FileInfo().Mode()&os.ModeSymlink != 0 {
			continue
		}

		target := filepath.Join(binDir, base)
		if !withinDir(target, binDir) {
			continue
		}

		if err := writeZipEntry(f, target); err != nil {
			return err
		}
		found = true
	}

	if !found {
		return errors.New("zip 中未找到 ffmpeg 可执行文件")
	}
	return nil
}

// writeZipEntry 将单个 zip 条目写入目标文件并赋予可执行权限
func writeZipEntry(f *zip.File, target string) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("打开 zip 条目 %s 失败: %w", f.Name, err)
	}
	defer rc.Close()

	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("创建文件 %s 失败: %w", target, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("解压 %s 失败: %w", f.Name, err)
	}

	// zip 不一定保留 unix 可执行位，显式补 0755
	if err := os.Chmod(target, 0o755); err != nil {
		return fmt.Errorf("设置可执行权限失败: %w", err)
	}
	return nil
}

// extractTarXZ 解压 tar.xz，提取 ffmpeg/ffprobe
func extractTarXZ(archivePath, binDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("打开 tar.xz 失败: %w", err)
	}
	defer f.Close()

	xzr, err := xz.NewReader(bufio.NewReader(f))
	if err != nil {
		return fmt.Errorf("创建 xz 读取器失败: %w", err)
	}

	tr := tar.NewReader(xzr)
	found := false
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("读取 tar 条目失败: %w", err)
		}

		base := path.Base(hdr.Name)
		if !isWantedBinary(base) {
			continue
		}
		// 仅提取常规文件。兼容极旧 tar 包的旧式常规文件标记 '\x00'（tar.TypeRegA 已废弃）
		const legacyTypeRegA = '\x00'
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != legacyTypeRegA {
			continue
		}

		target := filepath.Join(binDir, base)
		if !withinDir(target, binDir) {
			continue
		}

		mode := hdr.FileInfo().Mode().Perm()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return fmt.Errorf("创建文件 %s 失败: %w", target, err)
		}

		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			return fmt.Errorf("解压 %s 失败: %w", hdr.Name, err)
		}
		out.Close()

		// 确保具备可执行权限
		if err := os.Chmod(target, 0o755); err != nil {
			return fmt.Errorf("设置可执行权限失败: %w", err)
		}
		found = true
	}

	if !found {
		return errors.New("tar.xz 中未找到 ffmpeg 可执行文件")
	}
	return nil
}

// withinDir 校验 target 位于 dir 目录内，防御路径穿越
func withinDir(target, dir string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// platformSupported 判断当前平台/架构是否支持私有自动安装
func platformSupported() bool {
	switch runtime.GOOS {
	case "windows":
		return runtime.GOARCH == "amd64"
	case "linux":
		switch runtime.GOARCH {
		case "amd64", "arm64", "arm", "386":
			return true
		}
	case "darwin":
		// evermeet 提供 Intel 构建，arm64 通过 Rosetta 2 运行
		return runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"
	}
	return false
}

// buildSources 根据平台构建有序下载源；环境变量 FFMPEG_DOWNLOAD_URL 优先级最高
func (s *FFmpegService) buildSources() ([]downloadSource, error) {
	if custom := strings.TrimSpace(os.Getenv(envFFmpegDownloadURL)); custom != "" {
		return []downloadSource{{url: custom, name: "环境变量指定地址"}}, nil
	}

	const (
		btbnWin64 = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip"
		btbnLnx64 = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linux64-gpl.tar.xz"
		btbnArm64 = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-linuxarm64-gpl.tar.xz"
		gyanEss   = "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip"
		evermeet  = "https://evermeet.cx/ffmpeg/getrelease/zip"
		jvs       = "https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-%s-static.tar.xz"
	)

	// GitHub 国内加速前缀（前缀拼接完整 URL）
	ghMirrors := []struct {
		prefix string
		name   string
	}{
		{"https://ghfast.top/", "ghfast 加速"},
		{"https://gh-proxy.com/", "gh-proxy 加速"},
	}

	// githubSources 依次返回：国内加速源 → GitHub 官方直连
	githubSources := func(githubURL string) []downloadSource {
		out := make([]downloadSource, 0, len(ghMirrors)+1)
		for _, m := range ghMirrors {
			out = append(out, downloadSource{url: m.prefix + githubURL, name: m.name})
		}
		out = append(out, downloadSource{url: githubURL, name: "GitHub 官方源"})
		return out
	}

	switch {
	case runtime.GOOS == "windows" && runtime.GOARCH == "amd64":
		// windows/amd64：BtbN 镜像加速 → gyan.dev → GitHub 官方源兜底
		btbn := githubSources(btbnWin64)
		sources := make([]downloadSource, 0, len(btbn)+1)
		sources = append(sources, btbn[:len(ghMirrors)]...)
		sources = append(sources, downloadSource{url: gyanEss, name: "gyan.dev 官方源"})
		sources = append(sources, btbn[len(ghMirrors):]...)
		return sources, nil

	case runtime.GOOS == "linux" && runtime.GOARCH == "amd64":
		sources := githubSources(btbnLnx64)
		sources = append(sources, downloadSource{
			url: fmt.Sprintf(jvs, "amd64"), name: "johnvansickle 官方源",
		})
		return sources, nil

	case runtime.GOOS == "linux" && runtime.GOARCH == "arm64":
		sources := githubSources(btbnArm64)
		sources = append(sources, downloadSource{
			url: fmt.Sprintf(jvs, "arm64"), name: "johnvansickle 官方源",
		})
		return sources, nil

	case runtime.GOOS == "linux" && runtime.GOARCH == "arm":
		// 32 位 ARM 仅有 johnvansickle 静态构建
		return []downloadSource{{
			url: fmt.Sprintf(jvs, "armhf"), name: "johnvansickle 官方源",
		}}, nil

	case runtime.GOOS == "linux" && runtime.GOARCH == "386":
		return []downloadSource{{
			url: fmt.Sprintf(jvs, "i686"), name: "johnvansickle 官方源",
		}}, nil

	case runtime.GOOS == "darwin" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64"):
		return []downloadSource{{url: evermeet, name: "evermeet 官方源"}}, nil
	}

	return nil, fmt.Errorf("当前平台 %s/%s 无可用下载源", runtime.GOOS, runtime.GOARCH)
}
