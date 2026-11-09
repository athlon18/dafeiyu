package stage

import (
	"encoding/binary"
	"math"
	"testing"

	"github.com/athlon18/dafeiyu/tts"
)

// wavPCM16 造一段 16bit 单声道 wav：前半有音、后半静音。
func wavPCM16(rate int, samples []int16) []byte {
	data := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(s))
	}
	out := []byte("RIFF")
	out = appendLE32(out, uint32(36+len(data)))
	out = append(out, []byte("WAVEfmt ")...)
	out = appendLE32(out, 16)
	out = appendLE16(out, 1)          // PCM
	out = appendLE16(out, 1)          // 单声道
	out = appendLE32(out, uint32(rate))
	out = appendLE32(out, uint32(rate*2))
	out = appendLE16(out, 2)
	out = appendLE16(out, 16)
	out = append(out, []byte("data")...)
	out = appendLE32(out, uint32(len(data)))
	return append(out, data...)
}

func appendLE32(b []byte, v uint32) []byte { return append(b, byte(v), byte(v>>8), byte(v>>16), byte(v>>24)) }
func appendLE16(b []byte, v uint16) []byte { return append(b, byte(v), byte(v>>8)) }

func tone(rate, ms int, amp float64) []int16 {
	n := rate * ms / 1000
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(math.Sin(2*math.Pi*220*float64(i)/float64(rate)) * amp * 32767)
	}
	return out
}

func silence(rate, ms int) []int16 { return make([]int16, rate*ms/1000) }

// 包络的用途只有一个：让嘴张得跟响度对得上。所以"响的地方>0、静的地方==0"必须成立。
func TestEnvelopeLoudThenSilent(t *testing.T) {
	samples := append(tone(22050, 500, 0.5), silence(22050, 500)...)
	res := tts.Result{Audio: wavPCM16(22050, samples), MIME: "audio/wav"}
	env := Envelope(res)
	if len(env) < 18 {
		t.Fatalf("20Hz 包络应该有约 20 格，实际 %d", len(env))
	}
	loud, quiet := env[2], env[len(env)-2]
	if loud <= 0 {
		t.Fatalf("响的地方包络是 %d，嘴不会张", loud)
	}
	if quiet != 0 {
		t.Fatalf("静音处包络是 %d，嘴该合上", quiet)
	}
	for _, v := range env {
		if v < 0 || v > 100 {
			t.Fatalf("包络必须落在 0~100，实际 %d", v)
		}
	}
}

// 底噪/极轻的声音不该被当成"在说话"，否则停顿处嘴还张着。
func TestEnvelopeZeroesNearSilence(t *testing.T) {
	res := tts.Result{Audio: wavPCM16(22050, tone(22050, 400, 0.001)), MIME: "audio/wav"}
	env := Envelope(res)
	for i, v := range env {
		if v != 0 {
			t.Fatalf("第 %d 格=%d，接近静音应当归零", i, v)
		}
	}
}

// 认不出格式时返回 nil：叠层会退化成匀速张嘴，至少嘴会动，而不是整张图僵住。
func TestEnvelopeUnknownFormatIsNil(t *testing.T) {
	if env := Envelope(tts.Result{Audio: []byte("not audio"), MIME: "application/octet-stream"}); env != nil {
		t.Fatalf("认不出的格式应当返回 nil，实际 %v", env)
	}
	if env := Envelope(tts.Result{Audio: nil, MIME: "audio/wav"}); env != nil {
		t.Fatalf("空音频应当返回 nil，实际 %v", env)
	}
}

// 立体声要混成单声道再算：直接按 2 字节读会把左右声道当采样点，包络会抖成锯齿。
func TestEnvelopeHandlesStereo(t *testing.T) {
	mono := tone(22050, 300, 0.5)
	data := make([]int16, 0, len(mono)*2)
	for _, s := range mono {
		data = append(data, s, s)
	}
	res := tts.Result{Audio: wavPCM16Stereo(22050, data), MIME: "audio/wav"}
	env := Envelope(res)
	if len(env) < 4 {
		t.Fatalf("立体声包络格数不对: %d", len(env))
	}
	if env[1] <= 0 {
		t.Fatalf("立体声没算出响度: %v", env[:6])
	}
}

func wavPCM16Stereo(rate int, samples []int16) []byte {
	raw := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(s))
	}
	out := []byte("RIFF")
	out = appendLE32(out, uint32(36+len(raw)))
	out = append(out, []byte("WAVEfmt ")...)
	out = appendLE32(out, 16)
	out = appendLE16(out, 1)
	out = appendLE16(out, 2) // 立体声
	out = appendLE32(out, uint32(rate))
	out = appendLE32(out, uint32(rate*4))
	out = appendLE16(out, 4)
	out = appendLE16(out, 16)
	out = append(out, []byte("data")...)
	out = appendLE32(out, uint32(len(raw)))
	return append(out, raw...)
}
