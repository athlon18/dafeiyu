package stage

// 口型同步用的"响度包络"。算法与数值原样搬自主仓 service/host_speak.go，不自创：
// 20Hz 一格、底噪阈值 0.004、开方提亮后乘 1.8 压到 0~100。
//
// 为什么不把 TTS 音频再送一份给叠层去播：声音已经由本机声卡在放，浏览器再放一遍就是双重出声。
// 叠层只需要知道"这一刻嘴该张多大"，所以后端把合成好的音频压成响度序列，
// 连同开始播放的时刻放进 state，叠层按 Date.now()-speakAt 取对应一格。

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"strings"

	"github.com/hajimehoshi/go-mp3"

	"github.com/athlon18/dafeiyu/tts"
)

const speakEnvHz = 20

// silenceFloorRMS 低于这个响度算底噪，归零——否则停顿处嘴还张着。
const silenceFloorRMS = 0.004

// Envelope 把一段音频压成 20Hz 响度（0~100）。认不出的格式返回 nil，
// 叠层会退化成"说话时匀速张嘴"，至少嘴会动。
func Envelope(res tts.Result) []int {
	var pcm []int16
	var rate int
	switch {
	case strings.Contains(res.MIME, "wav"):
		pcm, rate = decodeWAVPCM16(res.Audio)
	case strings.Contains(res.MIME, "mpeg"), strings.Contains(res.MIME, "mp3"):
		pcm, rate = decodeMP3PCM16(res.Audio)
	}
	if rate <= 0 || len(pcm) == 0 {
		return nil
	}
	win := rate / speakEnvHz
	if win < 1 {
		win = 1
	}
	out := make([]int, 0, len(pcm)/win+1)
	for i := 0; i+win <= len(pcm); i += win {
		var sum float64
		for _, s := range pcm[i : i+win] {
			f := float64(s) / 32768
			sum += f * f
		}
		rms := math.Sqrt(sum / float64(win))
		v := 0
		if rms > silenceFloorRMS {
			v = int(math.Min(1, math.Sqrt(rms)*1.8) * 100)
		}
		out = append(out, v)
	}
	return out
}

// decodeWAVPCM16 只解 16bit PCM 的 RIFF/WAVE（Piper/Kokoro 都是这个形状）。
// 不走第三方库：需要的只是 fmt/data 两个 chunk，自己拆几十行更省依赖。
func decodeWAVPCM16(b []byte) ([]int16, int) {
	if len(b) < 44 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, 0
	}
	var channels, bits, rate int
	var data []byte
	for pos := 12; pos+8 <= len(b); {
		id := string(b[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(b[pos+4 : pos+8]))
		pos += 8
		if size < 0 || pos+size > len(b) {
			size = len(b) - pos
		}
		switch id {
		case "fmt ":
			if size >= 16 {
				channels = int(binary.LittleEndian.Uint16(b[pos+2 : pos+4]))
				rate = int(binary.LittleEndian.Uint32(b[pos+4 : pos+8]))
				bits = int(binary.LittleEndian.Uint16(b[pos+14 : pos+16]))
			}
		case "data":
			data = b[pos : pos+size]
		}
		pos += size + size&1
	}
	if bits != 16 || channels < 1 || rate <= 0 || len(data) < 2 {
		return nil, rate
	}
	frame := 2 * channels
	out := make([]int16, 0, len(data)/frame)
	for i := 0; i+frame <= len(data); i += frame {
		var sum int32
		for c := 0; c < channels; c++ {
			sum += int32(int16(binary.LittleEndian.Uint16(data[i+2*c : i+2*c+2])))
		}
		// 多声道先求均值：直接按 2 字节读会把左右声道当采样点，包络会抖成锯齿。
		out = append(out, int16(sum/int32(channels)))
	}
	return out, rate
}

func decodeMP3PCM16(b []byte) ([]int16, int) {
	d, err := mp3.NewDecoder(bytes.NewReader(b))
	if err != nil {
		return nil, 0
	}
	raw, err := io.ReadAll(io.LimitReader(d, 64<<20))
	if err != nil || len(raw) < 4 {
		return nil, d.SampleRate()
	}
	out := make([]int16, 0, len(raw)/4)
	for i := 0; i+4 <= len(raw); i += 4 {
		l := int32(int16(binary.LittleEndian.Uint16(raw[i : i+2])))
		r := int32(int16(binary.LittleEndian.Uint16(raw[i+2 : i+4])))
		out = append(out, int16((l+r)/2))
	}
	return out, d.SampleRate()
}
