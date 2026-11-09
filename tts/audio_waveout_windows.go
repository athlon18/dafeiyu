//go:build windows

// waveOut 播放器：真正支持“暂停 / 从暂停位置继续 / 精确进度”。
//
// 为什么不继续用 PlaySound（用了很久，最终换掉）：
//   PlaySound 的 API 只有“开始播一个文件”和“停止”，**没有 seek、没有 resume**。
//   所以“暂停 → 继续”只能从头重播，进度条也只能靠“开始时刻 + 时长”估。
//   而且它的 SND_PURGE 只能停“同一个 OS 线程”发起的播放，
//   跨线程 purge 是空操作——这一条让我们绕了很久。
//
// waveOut 是 Windows 的多媒体音频流 API：
//   waveOutWrite 提交 PCM 块、waveOutPause/Restart 真暂停/恢复、
//   waveOutGetPosition 给出精确到采样点的播放位置。
// 代价是要自己解析 wav、管缓冲和状态机，且必须绑死在一个 OS 线程上。
package tts

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

var (
	winmm               = syscall.NewLazyDLL("winmm.dll")
	procWaveOutOpen     = winmm.NewProc("waveOutOpen")
	procWaveOutClose    = winmm.NewProc("waveOutClose")
	procWaveOutPrep     = winmm.NewProc("waveOutPrepareHeader")
	procWaveOutUnprp    = winmm.NewProc("waveOutUnprepareHeader")
	procWaveOutWrite    = winmm.NewProc("waveOutWrite")
	procWaveOutPause    = winmm.NewProc("waveOutPause")
	procWaveOutRestr    = winmm.NewProc("waveOutRestart")
	procWaveOutReset    = winmm.NewProc("waveOutReset")
	procWaveOutPos      = winmm.NewProc("waveOutGetPosition")
	procWaveOutNumDevs  = winmm.NewProc("waveOutGetNumDevs")
	procWaveOutDevCaps  = winmm.NewProc("waveOutGetDevCapsW")
)

// OutputDevice 一个可选的音频输出设备。ID=-1 表示系统默认。
type OutputDevice struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// waveOutCapsW 对应 WAVEOUTCAPSW（MAXPNAMELEN=32）。
type waveOutCapsW struct {
	Mid           uint16
	Pid           uint16
	DriverVersion uint32
	Name          [32]uint16
	Formats       uint32
	Channels      uint16
	Reserved      uint16
	Support       uint32
}

// outputDevice 当前选定的输出设备索引（-1=系统默认）。
//
// 为什么要能选设备：TTS 必须能和游戏声/BGM 分开成两路——
// 主播端把 TTS 送到混音器（Studio One / VoiceMeeter）的单独一轨，
// 闭麦就是在混音台上拉掉那一轨。如果 TTS 和游戏声共用系统默认输出，
// 闭麦会把游戏声一起闭掉，那把推子就没法用。
var outputDevice atomic.Int32

func init() { outputDevice.Store(-1) }

// SetOutputDevice 指定后续播放走哪个设备（-1=系统默认）。
// 切设备时 openLocked 会发现句柄里的设备不一致，先关后重开。
func SetOutputDevice(id int) {
	if id < 0 {
		id = -1
	}
	outputDevice.Store(int32(id))
}

// CurrentOutputDevice 当前选的设备索引（-1=系统默认）。
func CurrentOutputDevice() int { return int(outputDevice.Load()) }

// OutputDevices 枚举本机音频输出设备，第一项是“系统默认”。
//
// 除默认外，设备 ID 是 waveOut 的索引（0..N-1），插拔设备后会变，
// 所以配置里同时存名字，页面上能看出“选的还是不是那一个”。
func OutputDevices() []OutputDevice {
	out := []OutputDevice{{ID: -1, Name: "系统默认（跟随 Windows 默认输出）"}}
	n, _, _ := procWaveOutNumDevs.Call()
	for i := uintptr(0); i < n; i++ {
		var caps waveOutCapsW
		if r, _, _ := procWaveOutDevCaps.Call(i, uintptr(unsafe.Pointer(&caps)), unsafe.Sizeof(caps)); r != 0 {
			continue
		}
		out = append(out, OutputDevice{ID: int(i), Name: utf16Z(caps.Name[:])})
	}
	return out
}

func utf16Z(s []uint16) string {
	n := 0
	for n < len(s) && s[n] != 0 {
		n++
	}
	return string(utf16.Decode(s[:n]))
}

const (
	waveMapper = 0xFFFFFFFF

	womDone   = 0x00000001 // WAVEHDR.dwFlags: 这块播完了
	whdrBegin = 0x00000010
	whdrEnd   = 0x00000002

	timeSamples = 0x00000002 // waveOutGetPosition 的单位：采样点
)

type waveFormatEx struct {
	FormatTag      uint16
	Channels       uint16
	SamplesPerSec  uint32
	AvgBytesPerSec uint32
	BlockAlign     uint16
	BitsPerSample  uint16
	CbSize         uint16
}

type waveHdr struct {
	Data          uintptr
	BufferLength  uint32
	BytesRecorded uint32
	User          uintptr
	Flags         uint32
	Loops         uint32
	Next          uintptr
	Reserved      uintptr
}

type mmTime struct {
	Type  uint32
	Value uint32
	_     uint32
}

// waveClip 一次播放的 PCM 数据 + 位置信息。
type waveClip struct {
	pcm       []byte
	fmt       waveFormatEx
	hdr       waveHdr
	done      chan struct{} // 播完时关闭
	stopOnce  sync.Once
}

// waveEngine 全局只有一个输出设备（一条声带），串行使用。
type waveEngine struct {
	mu      sync.Mutex
	hwo     uintptr
	device  int32 // 当前句柄打开的 deviceID（-1=默认），用来发现切设备
	clip    *waveClip
	paused  bool
	playing bool
}

var engine = &waveEngine{}

// openLocked 打开设备（已持锁）。
//
// 设备变了（页面上换了输出通道）就先把旧句柄关掉再重开——
// 不关的话声音会一直留在旧设备上，改设置看起来“没生效”。
func (e *waveEngine) openLocked(f waveFormatEx) error {
	sel := int32(-1)
	if v := outputDevice.Load(); v >= 0 {
		sel = v
	}
	if e.hwo != 0 {
		if e.device == sel {
			return nil // 同设备；格式由第一首决定，后续复用
		}
		_, _, _ = procWaveOutClose.Call(e.hwo)
		e.hwo = 0
	}
	dev := uintptr(waveMapper)
	if sel >= 0 {
		dev = uintptr(sel)
	}
	var h uintptr
	r, _, _ := procWaveOutOpen.Call(
		uintptr(unsafe.Pointer(&h)),
		dev,
		uintptr(unsafe.Pointer(&f)),
		0, 0, 0,
	)
	if r != 0 {
		return fmt.Errorf("waveOutOpen 失败（设备 %d，码 %d）", sel, r)
	}
	e.hwo = h
	e.device = sel
	return nil
}

// PlayWave 播放一段 wav 字节流，阻塞到播完或被 ctx 取消。
// 返回后设备仍保持打开，以便下一首立即开始。
func PlayWave(ctx context.Context, wav []byte) error {
	// waveOut 的回调/状态与线程绑定，整段必须固定在同一个 OS 线程。
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	pcm, f, err := parseWAVBytes(wav)
	if err != nil {
		return err
	}
	if len(pcm) == 0 {
		return fmt.Errorf("wav 里没有 PCM 数据")
	}

	engine.mu.Lock()
	if err := engine.openLocked(f); err != nil {
		engine.mu.Unlock()
		return err
	}
	clip := &waveClip{pcm: pcm, fmt: f, done: make(chan struct{})}
	clip.hdr = waveHdr{
		Data:         uintptr(unsafe.Pointer(&pcm[0])),
		BufferLength: uint32(len(pcm)),
	}
	if r, _, _ := procWaveOutPrep.Call(engine.hwo, uintptr(unsafe.Pointer(&clip.hdr)), unsafe.Sizeof(clip.hdr)); r != 0 {
		engine.mu.Unlock()
		return fmt.Errorf("waveOutPrepareHeader 失败（码 %d）", r)
	}
	if r, _, _ := procWaveOutWrite.Call(engine.hwo, uintptr(unsafe.Pointer(&clip.hdr)), unsafe.Sizeof(clip.hdr)); r != 0 {
		_, _, _ = procWaveOutUnprp.Call(engine.hwo, uintptr(unsafe.Pointer(&clip.hdr)), unsafe.Sizeof(clip.hdr))
		engine.mu.Unlock()
		return fmt.Errorf("waveOutWrite 失败（码 %d）", r)
	}
	engine.clip = clip
	engine.playing = true
	engine.paused = false
	hwo := engine.hwo
	engine.mu.Unlock()

	defer func() {
		engine.mu.Lock()
		engine.playing = false
		engine.paused = false
		engine.clip = nil
		engine.mu.Unlock()
		_, _, _ = procWaveOutReset.Call(hwo) // 丢掉还没播完的部分
		_, _, _ = procWaveOutUnprp.Call(hwo, uintptr(unsafe.Pointer(&clip.hdr)), unsafe.Sizeof(clip.hdr))
	}()

	// 轮询 WAVEHDR.dwFlags 的 WHDR_DONE：没事件回调，这是最朴素也最稳的判定。
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil // 被打断不算故障
		case <-tick.C:
			if clip.hdr.Flags&womDone != 0 {
				return nil
			}
		}
	}
}

// WavePause 暂停当前播放（保留位置）。
func WavePause() bool {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if !engine.playing || engine.paused {
		return false
	}
	if r, _, _ := procWaveOutPause.Call(engine.hwo); r != 0 {
		return false
	}
	engine.paused = true
	return true
}

// WaveResume 从暂停位置继续。
func WaveResume() bool {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if !engine.playing || !engine.paused {
		return false
	}
	if r, _, _ := procWaveOutRestr.Call(engine.hwo); r != 0 {
		return false
	}
	engine.paused = false
	return true
}

// WavePosition 当前播放位置（秒）。设备没开或没在播时返回 0。
func WavePosition() int {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.hwo == 0 || !engine.playing {
		return 0
	}
	var mt mmTime
	mt.Type = timeSamples
	if r, _, _ := procWaveOutPos.Call(engine.hwo, uintptr(unsafe.Pointer(&mt)), unsafe.Sizeof(mt)); r != 0 {
		return 0
	}
	if mt.Type != timeSamples || engine.clip == nil || engine.clip.fmt.SamplesPerSec == 0 {
		return 0
	}
	return int(mt.Value / engine.clip.fmt.SamplesPerSec)
}

// WavePlaying 当前是否有东西在播（含暂停）。
func WavePlaying() bool {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.playing
}

// parseWAVBytes 从 wav 字节流里取出 PCM 与格式。只支持 PCM（fmt tag=1）。
func parseWAVBytes(wav []byte) ([]byte, waveFormatEx, error) {
	var f waveFormatEx
	if len(wav) < 12 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil, f, fmt.Errorf("不是合法的 wav")
	}
	pos := 12
	for pos+8 <= len(wav) {
		id := string(wav[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(wav[pos+4 : pos+8]))
		body := pos + 8
		if body+size > len(wav) {
			size = len(wav) - body
		}
		switch id {
		case "fmt ":
			if size < 16 {
				return nil, f, fmt.Errorf("fmt chunk 太短")
			}
			f.FormatTag = binary.LittleEndian.Uint16(wav[body : body+2])
			f.Channels = binary.LittleEndian.Uint16(wav[body+2 : body+4])
			f.SamplesPerSec = binary.LittleEndian.Uint32(wav[body+4 : body+8])
			f.AvgBytesPerSec = binary.LittleEndian.Uint32(wav[body+8 : body+12])
			f.BlockAlign = binary.LittleEndian.Uint16(wav[body+12 : body+14])
			f.BitsPerSample = binary.LittleEndian.Uint16(wav[body+14 : body+16])
		case "data":
			if f.FormatTag != 1 {
				return nil, f, fmt.Errorf("只支持 PCM wav（当前 fmt=%d）", f.FormatTag)
			}
			return wav[body : body+size], f, nil
		}
		pos = body + size
		if size%2 == 1 {
			pos++ // RIFF 要求偶数字节对齐
		}
	}
	return nil, f, fmt.Errorf("wav 里找不到 data chunk")
}

// parseWAVFile 读文件并解析。
func parseWAVFile(path string) ([]byte, waveFormatEx, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, waveFormatEx{}, err
	}
	return parseWAVBytes(b)
}

// waveAvailable 设备能否打开（探测一次，失败就回退到 PlaySound）。
func waveAvailable() bool {
	f := waveFormatEx{FormatTag: 1, Channels: 1, SamplesPerSec: 24000, AvgBytesPerSec: 48000, BlockAlign: 2, BitsPerSample: 16}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	return engine.openLocked(f) == nil
}

var _ = io.EOF // 保留 io 引用（parse 用到 io 的同类工具时不再重复 import 报错）
