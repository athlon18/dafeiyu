package toolenv

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// streamClient 只给一键下载静态 ffmpeg 用：整包几十 MB，不设总超时，但每步都得有上限，
// 否则断网时会一直挂着，把"装不上"变成"看不到失败"。
var streamClient = &http.Client{
	Transport: &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSHandshakeTimeout: 15 * time.Second,
		MaxIdleConns:        2,
	},
}

// 本机工具环境（ffmpeg 等）的「探测 + 一键安装」闭环。
//
// 为什么要有这套：录制转 MP4、抽音轨转文字、切片合并/修复都依赖 ffmpeg，但服务器常常没装，
// 而旧版只在配置页甩一句「未安装，请自行 brew install」——用户没有闭环。这里补上：
// 点「安装」→ 我们把静态 ffmpeg 下到应用自带目录并验证；成功就绿，失败弹窗给出确切命令与重试。
//
// 关键取舍：装到 data/bin（应用自带目录），不改系统 PATH、不需要 sudo，卸载=删目录；
// 且 API 侧解析 ffmpeg 时「先看 data/bin，再看 PATH」，所以自动装的那份能被全仓复用。

// toolBinDir 应用自带的可执行目录（相对进程工作目录，run-split 起的就是仓库根）。
func toolBinDir() string {
	d := filepath.Join("data", "bin")
	_ = os.MkdirAll(d, 0o755)
	return d
}

func exeName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".exe"
	}
	return base
}

// ResolveFFmpeg 返回 ffmpeg 路径与来源：app=我们自动装的，system=PATH 里的，空=没有。
func ResolveFFmpeg() (path, source string) {
	p := filepath.Join(toolBinDir(), exeName("ffmpeg"))
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Size() > 1<<20 {
		return p, "app"
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p, "system"
	}
	return "", ""
}

// ToolEnv 工具环境探测结果，供前端「工具环境」卡展示与决定能不能自动装。
type ToolEnv struct {
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	FFmpegPath      string `json:"ffmpegPath"`
	FFmpegSource    string `json:"ffmpegSource"` // app|system|""
	FFmpegOK        bool   `json:"ffmpegOk"`
	FFmpegVersion   string `json:"ffmpegVersion"`
	AutoInstallable bool   `json:"autoInstallable"` // 本平台能否走「下载静态版」自动装
	ManualCommand   string `json:"manualCommand"`   // 不能自动装时给的确切命令
}

// DetectTools 探测当前进程的 ffmpeg 状况（安装/合并都发生在监听机，前端应经转发拿 watcher 的结果）。
func DetectTools() ToolEnv {
	env := ToolEnv{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if p, src := ResolveFFmpeg(); p != "" {
		env.FFmpegPath, env.FFmpegSource, env.FFmpegOK = p, src, true
		env.FFmpegVersion = ffmpegVersion(p)
	}
	env.AutoInstallable = runtime.GOOS == "darwin"
	env.ManualCommand = manualFFmpegCommand()
	return env
}

func ffmpegVersion(p string) string {
	out, err := exec.Command(p, "-version").Output()
	if err != nil {
		return ""
	}
	line := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
	if len(line) > 80 {
		line = line[:80]
	}
	return line
}

func manualFFmpegCommand() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install ffmpeg"
	case "linux":
		return "sudo apt-get install -y ffmpeg  # 或 dnf/yum install ffmpeg"
	case "windows":
		return "winget install Gyan.FFmpeg  # 或 choco install ffmpeg"
	}
	return "从 https://ffmpeg.org/download.html 下载静态版，放到 data/bin/ffmpeg"
}

// ===== 安装任务：单实例、内存态、前端轮询进度 =====

type InstallJob struct {
	Tool       string    `json:"tool"`
	Status     string    `json:"status"` // running|done|failed
	Log        []string  `json:"log"`
	Err        string    `json:"err"`
	Manual     string    `json:"manual"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

var (
	installMu  sync.Mutex
	installJob *InstallJob
)

// StartInstallFFmpeg 拉起一次 ffmpeg 安装；已有任务在跑则回 busy。
func StartInstallFFmpeg() error {
	installMu.Lock()
	defer installMu.Unlock()
	if installJob != nil && installJob.Status == "running" {
		return fmt.Errorf("已有安装任务在进行中，请稍候")
	}
	env := DetectTools()
	if env.FFmpegOK && env.FFmpegSource == "system" {
		// 系统里已有：不必再下载，直接报成功并说明来源，避免重复下载几十 MB
		installJob = &InstallJob{Tool: "ffmpeg", Status: "done", StartedAt: time.Now(), FinishedAt: time.Now(),
			Log: []string{"检测到系统已安装 ffmpeg：" + env.FFmpegPath + "，无需再装"}}
		return nil
	}
	if !env.AutoInstallable {
		installJob = &InstallJob{Tool: "ffmpeg", Status: "failed", StartedAt: time.Now(), FinishedAt: time.Now(),
			Err: "本平台暂不支持自动下载，请按手动命令安装", Manual: env.ManualCommand}
		return nil
	}
	installJob = &InstallJob{Tool: "ffmpeg", Status: "running", StartedAt: time.Now(), Log: []string{}}
	go runInstallFFmpeg(installJob)
	return nil
}

// InstallStatus 返回当前安装任务快照（可能为 nil）。
func InstallStatus() *InstallJob {
	installMu.Lock()
	defer installMu.Unlock()
	if installJob == nil {
		return nil
	}
	cp := *installJob
	cp.Log = append([]string(nil), installJob.Log...)
	return &cp
}

func (j *InstallJob) log(line string) {
	installMu.Lock()
	defer installMu.Unlock()
	if len(j.Log) < 200 {
		j.Log = append(j.Log, line)
	}
}

// runInstallFFmpeg 下载 mac 静态 ffmpeg + ffprobe 到 data/bin 并验证。
// evermeet.cx 的 getrelease/zip 是稳定入口（返回最新 x86_64 版，Apple Silicon 经 Rosetta 可跑）。
func runInstallFFmpeg(job *InstallJob) {
	defer func() {
		if r := recover(); r != nil {
			installMu.Lock()
			job.Status, job.Err, job.FinishedAt = "failed", fmt.Sprint(r), time.Now()
			job.Manual = manualFFmpegCommand()
			installMu.Unlock()
		}
	}()

	// ffmpeg 必需；ffprobe 尽力而为（合并/转码只用 ffmpeg，缺 ffprobe 不算安装失败）。
	if err := downloadFromURLs(ffmpegURLs(), filepath.Join(toolBinDir(), exeName("ffmpeg")), job, "ffmpeg"); err != nil {
		installMu.Lock()
		job.Status, job.Err, job.FinishedAt = "failed", "ffmpeg 下载失败："+err.Error(), time.Now()
		job.Manual = manualFFmpegCommand()
		installMu.Unlock()
		job.log("失败：" + err.Error())
		return
	}
	if err := downloadFromURLs(ffprobeURLs(), filepath.Join(toolBinDir(), exeName("ffprobe")), job, "ffprobe"); err != nil {
		job.log("ffprobe 未取到（不影响 ffmpeg 主功能）：" + err.Error())
	}
	// 验证：能跑出 -version 才算真装上
	p := filepath.Join(toolBinDir(), exeName("ffmpeg"))
	if v := ffmpegVersion(p); v == "" {
		installMu.Lock()
		job.Status, job.Err, job.FinishedAt = "failed", "下载完成但 ffmpeg 无法执行（可能被 Gatekeeper 拦截或架构不符）", time.Now()
		job.Manual = manualFFmpegCommand()
		installMu.Unlock()
		return
	} else {
		job.log("验证通过：" + v)
	}
	installMu.Lock()
	job.Status, job.FinishedAt = "done", time.Now()
	installMu.Unlock()
	job.log("安装完成，全仓将优先使用 data/bin 下的这份 ffmpeg")
}

// ffmpegURLs / ffprobeURLs 按架构给出候选静态包地址（arm64 优先 osxexperts 原生 arm 版，x86 优先 evermeet）。
// 多源按序试，哪个通用哪个——静态包地址会失效，不能只押一个。
func ffmpegURLs() []string {
	if runtime.GOARCH == "arm64" {
		return []string{"https://www.osxexperts.net/ffmpeg71arm.zip", "https://evermeet.cx/ffmpeg/ffmpeg-7.1.zip"}
	}
	return []string{"https://evermeet.cx/ffmpeg/ffmpeg-7.1.zip", "https://www.osxexperts.net/ffmpeg71intel.zip"}
}

func ffprobeURLs() []string {
	if runtime.GOARCH == "arm64" {
		return []string{"https://www.osxexperts.net/ffprobe71arm.zip", "https://evermeet.cx/ffmpeg/ffprobe-7.1.zip"}
	}
	return []string{"https://evermeet.cx/ffmpeg/ffprobe-7.1.zip", "https://www.osxexperts.net/ffprobe71intel.zip"}
}

// downloadFromURLs 依次尝试候选地址，第一个成功即返回；全失败返回最后一个错误。
func downloadFromURLs(urls []string, dest string, job *InstallJob, name string) error {
	var lastErr error
	for _, u := range urls {
		job.log("下载 " + name + "：" + u)
		if err := downloadZipBinary(u, dest); err != nil {
			lastErr = err
			job.log("  这个源不行：" + err.Error() + "，换下一个")
			continue
		}
		job.log(name + " 已就位：" + filepath.Join("data/bin", filepath.Base(dest)))
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("没有可用下载源")
	}
	return lastErr
}

// downloadZipBinary 下载一个「zip 里就一个可执行文件」的静态二进制，解出后落盘并置可执行位。
func downloadZipBinary(url, dest string) error {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := streamClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 200<<20)) // 上限 200MB，防跑飞
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("解压 zip 失败：%w", err)
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(dest+".tmp", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			_ = os.Remove(dest + ".tmp") // 清理半截临时文件，别让一次失败的下载在磁盘上留下垃圾（成功路径靠下面的 Rename 自然带走它）
			return err
		}
		_ = os.Rename(dest+".tmp", dest)
		_ = os.Chmod(dest, 0o755)
		return nil
	}
	return fmt.Errorf("zip 里没有可执行文件")
}
