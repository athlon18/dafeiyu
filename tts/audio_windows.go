//go:build windows

// Windows 原生播放。
//
// 为什么不用 MCI：它在"open 之后 play"这一步的行为依赖驱动实现，
// 实测同一台机器上 PowerShell 调得通、Go 里 open 返回成功却拿不到设备状态；
// 而 PlaySoundW 参数极简、返回值明确，一次就通。直播要的是"点了就响"，
// 不是一个需要按机器调教的多媒体子系统。
//
// 代价是 PlaySound 只认 wav：mp3 先用 ffmpeg 转一道（ffmpeg 本来就在本仓 data/bin，
// ASR 抽音轨一直用它）。多花几十毫秒，换来"任何 Windows 机器都能出声"。
package tts

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/athlon18/dafeiyu/util/toolenv"
)

var procPlaySoundW = syscall.NewLazyDLL("winmm.dll").NewProc("PlaySoundW")

// waveSeekable waveOut 是否真的在这台机器上跑通过（决定 UI 能不能“暂停后续播”）。
var waveSeekable atomic.Bool

// PlaySoundW 的 flags。
const (
	sndAsync     = 0x00000001 // 异步：立即返回，便于同线程 purge
	sndNoDefault = 0x00000002
	sndPurge     = 0x00000040
	sndFilename  = 0x00020000
)

// nativeAvailable Windows 上恒 true：PlaySound 是系统能力，不需要安装任何东西。
func nativeAvailable() bool { return true }

// nativePause / nativeResume / nativePosition / nativeSeekable 把 waveOut 的能力
// 暴露给上层。不支持时返回 false/0，由调用方降级为“停下 + 从头播”。
func nativePause() bool    { return WavePause() }
func nativeResume() bool   { return WaveResume() }
func nativePosition() int  { return WavePosition() }
func nativeSeekable() bool { return waveSeekable.Load() }

// playNative 播放一个本地音频文件，阻塞到播完或被 ctx 取消。
//
// 三级回退：
//  1. waveOut（首选）：支持 pause/resume/精确位置，点歌台的进度条和续播靠它；
//  2. PlaySoundW：能出声但不支持 seek，万一 waveOut 在这台机器上打不开就用它；
//  3. 两者都失败才报错。
func playNative(ctx context.Context, file string) error {
	wav, err := toWAV(ctx, file)
	if err != nil {
		return err
	}
	defer func() {
		// 转出来的临时 wav 用完就删，否则一小时能攒几十个百 MB 文件。
		if wav != file {
			_ = os.Remove(wav)
		}
	}()

	// —— 1) waveOut ——
	if b, rerr := os.ReadFile(wav); rerr == nil {
		if werr := PlayWave(ctx, b); werr == nil {
			waveSeekable.Store(true)
			return nil
		} else {
			waveSeekable.Store(false)
			// 指定了具体设备时不能回退 PlaySound：它只认系统默认输出，
			// 回退会把声音送到另一路——混音器上看起来就是‘选了设备没用’。
			if CurrentOutputDevice() >= 0 {
				return werr
			}
		}
	}

	// —— 2) PlaySound 回退 ——
	return playSoundFallback(ctx, wav)
}

// playSoundFallback 老的 PlaySound 通道：能出声，但不支持 seek。
// 注意 SND_PURGE 只能停同一个 OS 线程发起的播放，所以这里绑线程。
func playSoundFallback(ctx context.Context, wav string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	p, err := syscall.UTF16PtrFromString(wav)
	if err != nil {
		return err
	}
	r, _, _ := procPlaySoundW.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(0),
		uintptr(sndFilename|sndAsync|sndNoDefault),
	)
	if r == 0 {
		return fmt.Errorf("PlaySound 播放失败（格式不支持或设备不可用）")
	}

	select {
	case <-time.After(wavDuration(wav)):
		return nil
	case <-ctx.Done():
		_, _, _ = procPlaySoundW.Call(uintptr(0), uintptr(0), uintptr(sndPurge))
		return nil
	}
}

// wavDuration 从 wav 头算时长。
//
// 不能假设 data 在固定偏移 44：ffmpeg 会先写 LIST/INFO 等 chunk，
// data 的位置是变的。读错的结果是时长算成 0 → 立刻认为“播完了”
// （current 被清空），而 SND_ASYNC 的声音还在响——就是“切歌切不动”的元凶。
// 这里按 RIFF 规范逐 chunk 遍历，找不到就回退一个大值。
func wavDuration(path string) time.Duration {
	const fallback = 10 * time.Minute
	f, err := os.Open(path)
	if err != nil {
		return fallback
	}
	defer f.Close()

	head := make([]byte, 12)
	if _, err := io.ReadFull(f, head); err != nil {
		return fallback
	}
	if string(head[0:4]) != "RIFF" || string(head[8:12]) != "WAVE" {
		return fallback
	}

	var byteRate uint32
	for {
		ch := make([]byte, 8)
		if _, err := io.ReadFull(f, ch); err != nil {
			return fallback
		}
		size := binary.LittleEndian.Uint32(ch[4:8])
		switch string(ch[0:4]) {
		case "fmt ":
			fmtData := make([]byte, size)
			if _, err := io.ReadFull(f, fmtData); err != nil {
				return fallback
			}
			if len(fmtData) >= 12 {
				byteRate = binary.LittleEndian.Uint32(fmtData[8:12])
			}
		case "data":
			if byteRate == 0 {
				return fallback
			}
			dataSize := int64(size)
			// 流式写入的 wav 会把这个值写成 0/0xFFFFFFFF，那就用文件长度反推。
			if dataSize <= 0 || dataSize > 1<<31 {
				end, err := f.Seek(0, io.SeekEnd)
				if err != nil {
					return fallback
				}
				cur, _ := f.Seek(0, io.SeekCurrent)
				_ = cur
				dataSize = end - (12 + 8 + int64(size))
			}
			if dataSize <= 0 {
				return fallback
			}
			sec := float64(dataSize) / float64(byteRate)
			if sec <= 0 || sec > 3600 {
				return fallback
			}
			return time.Duration(sec * float64(time.Second))
		default:
			if _, err := f.Seek(int64(size), io.SeekCurrent); err != nil {
				return fallback
			}
		}
	}
}

// toWAV 保证拿到一个 PlaySound 认得的 wav。已经是 wav 就原样用。
func toWAV(ctx context.Context, src string) (string, error) {
	if strings.HasSuffix(strings.ToLower(src), ".wav") {
		return src, nil
	}
	ff, _ := toolenv.ResolveFFmpeg()
	if ff == "" {
		return "", fmt.Errorf("需要 ffmpeg 把音频转成 wav（本机没找到 ffmpeg；设置→转文字 里可一键安装）")
	}
	dst := strings.TrimSuffix(src, filepath.Ext(src)) + ".conv.wav"
	cmd := exec.CommandContext(ctx, ff, "-y", "-loglevel", "error",
		"-i", src, "-ac", "1", "-ar", "24000", "-c:a", "pcm_s16le", dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("转 wav 失败: %v: %s", err, snippet(out))
	}
	return dst, nil
}
