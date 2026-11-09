// 音频出口：把合成好的字节送到本机播放设备。
//
// 为什么用 ffplay 而不是 Go 音频库：
// CGO 音频库（portaudio/malgo）会让交叉编译和打包都变复杂，而 ffmpeg 本来就是本仓依赖
// （ASR 抽音轨在用）。主播端只要把系统默认输出设成虚拟声卡（VB-Cable / VoiceMeeter），
// OBS 的音频输入就能收到——这一层根本不需要知道设备名。
//
// 时序上只有一条铁律：**同一时刻只有一句在响**。新的一句进来先掐掉旧的，
// 否则两句话叠着念，观众听到的是"两个主播在抢麦"。
package tts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/athlon18/dafeiyu/util/toolenv"
)

// Player 单实例播放器。零值不可用，必须用 NewPlayer 构造（要探测 ffplay 在哪）。
type Player struct {
	bin    string // ffplay 路径；空=本机没装
	native bool   // 系统原生播放可用（Windows 的 MCI，零依赖）
	mu     sync.Mutex
	cmd    *exec.Cmd
}

// NewPlayer 探测可用的音频出口：ffplay 优先（便于跨平台统一行为），
// 没有时用系统原生通道（Windows MCI）——主播机上 ffplay 往往没跟 ffmpeg 一起装。
func NewPlayer() *Player {
	return &Player{bin: ffplayPath(), native: nativeAvailable()}
}

// Available 本机有没有任何可用的播放通道。没有时编排器应当整个静音，而不是每次调用都报错。
func (p *Player) Available() bool { return p != nil && (p.bin != "" || p.native) }

// NotFoundHint 给配置页/日志用的一句话，说明缺什么、怎么补。
func NotFoundHint() string {
	return "本机没有可用的音频出口（Windows 走系统自带 MCI；其他平台需安装 ffmpeg 自带的 ffplay）"
}

// Play 播放一段音频，**阻塞到播完**。调用方（编排器）靠阻塞来保证"一句说完再说下一句"。
//
// 为什么落临时文件而不是从管道喂：ffplay 从 stdin 读 mp3 时无法 seek，
// 遇到带 ID3 头的流会吞掉开头几百毫秒。TTS 单句音频只有几百 KB，落盘更稳。
func (p *Player) Play(ctx context.Context, audio []byte, mime string) error {
	if !p.Available() {
		return fmt.Errorf("%s", NotFoundHint())
	}
	if len(audio) == 0 {
		return fmt.Errorf("没有可播放的音频")
	}
	dir, err := os.MkdirTemp("", "dyplay-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := filepath.Join(dir, "say"+extOf(mime))
	if err := os.WriteFile(file, audio, 0o600); err != nil {
		return err
	}
	// 没装 ffplay 时走系统原生播放（Windows MCI）：多一个外部程序，
	// 就多一个"今天播不出声"的理由。
	if p.bin == "" {
		return playNative(ctx, file)
	}

	args := []string{"-nodisp", "-autoexit", "-loglevel", "error", file}
	cmd := exec.CommandContext(ctx, p.bin, args...)
	p.swap(cmd)
	defer p.swap(nil)
	if err := cmd.Run(); err != nil {
		// 被 Stop() 掐掉时说不上是故障：编排器打断旧句是正常操作。
		if ctx.Err() != nil || isKilled(err) {
			return nil
		}
		return fmt.Errorf("播放失败: %w", err)
	}
	return nil
}

// Stop 打断当前这一句（主播要抢话 / 场景切换 / 手动下播）。
func (p *Player) Stop() {
	p.mu.Lock()
	cmd := p.cmd
	p.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// Pause 暂停当前播放（保留位置）。返回 false 表示当前通道不支持暂停
// （例如回退到 ffplay/PlaySound 时），调用方应改用 Stop 而不是以为停住了。
func (p *Player) Pause() bool { return nativePause() }

// Resume 从暂停位置继续。返回 false 表示当前通道不支持续播。
func (p *Player) Resume() bool { return nativeResume() }

// Position 当前播放位置（秒）。不支持时返回 0。
func (p *Player) Position() int { return nativePosition() }

// SupportsSeek 本机通道能不能“暂停后续播”。
// 为什么需要它：点歌台的“暂停/继续”完全依赖这个能力，
// 不支持时必须在 UI 上如实表现（从头播），而不是让人以为停在意。
func (p *Player) SupportsSeek() bool { return nativeSeekable() }

func (p *Player) swap(cmd *exec.Cmd) {
	p.mu.Lock()
	p.cmd = cmd
	p.mu.Unlock()
}

// ffplayPath 优先取 ffmpeg 同目录下的 ffplay（同一套安装流程装出来的），再回落 PATH。
func ffplayPath() string {
	name := "ffplay"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if ff, _ := toolenv.ResolveFFmpeg(); ff != "" {
		cand := filepath.Join(filepath.Dir(ff), name)
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	if p, err := exec.LookPath("ffplay"); err == nil {
		return p
	}
	return ""
}

func extOf(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "audio/wav", "audio/x-wav", "audio/wave":
		return ".wav"
	case "audio/ogg", "audio/opus":
		return ".ogg"
	default:
		return ".mp3"
	}
}

// isKilled 判断错误是不是"进程被外部杀掉"，与网络/文件错误区分开。
func isKilled(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "killed") || strings.Contains(s, "terminated")
}
