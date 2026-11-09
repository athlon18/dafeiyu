// Package tts 语音合成：把 AI 主播要说的一句话，变成能推进 OBS 虚拟声卡的音频。
//
// 为什么单独建这一层，而不是在编排器里直接调厂商 SDK：
// 音频出口是整条链里最需要可替换的一环——今天用 edge-tts 零成本跑通，明天换商用音色
// 只是换一行配置。绑死一家，等于把「能不能播」押在它的可用性上。
//
// 与 modules/ai/llm 同构：OpenAI 兼容端点优先（POST {base}/audio/speech），其余按 Kind 插拔。
// 可缺席是第一原则：TTS 失败只让主播「哑一句」，不许把编排器带崩。
package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Kind 合成方式。空值按 openai 处理（多数网关兼容这一套）。
type Kind string

const (
	KindOpenAI Kind = "openai" // POST {base}/audio/speech，返回 mp3
	KindEdge   Kind = "edge"   // 调本机 edge-tts 命令行，返回 mp3
	// KindSAPI Windows 自带 System.Speech：**快但难听**。
	// 实测同一句话：edge 2.5 秒（大头是出网固定开销），SAPI 90~130 毫秒。
	// 定位是“低延迟模式 / edge 挂了兜底”，不适合当默认——机械音会毁掉人设。
	KindSAPI Kind = "sapi"
)

// Config 一次合成的可调项。字段刻意与 model.AsrConfig 同形，便于前端复用同一套表单。
type Config struct {
	Kind       Kind
	BaseURL    string
	APIKey     string
	Model      string  // openai: tts-1 / gpt-4o-mini-tts；edge 忽略
	Voice      string  // 音色名，两家不同，各自有默认
	Speed      float64 // 倍率，1.0=原速；<=0 视为默认
	TimeoutSec int
}

// Result 一次合成的产物。音频字节直接进播放器，不落盘（落盘由调用方决定）。
type Result struct {
	Audio []byte
	MIME  string
	Ms    int64 // 合成耗时，给「AI 运行台」用
}

const (
	// maxTextRunes 单次合成上限。一句直播回复超过这个长度，先截断再合成——
	// 整段话一口气念完观众也听不进去，编排层本来就该按句切。
	maxTextRunes = 500

	defaultVoiceOpenAI = "alloy"
	// 中文女声，edge-tts 自带、无需任何密钥。
	defaultVoiceEdge = "zh-CN-XiaoxiaoNeural"

	defaultTimeoutSec = 30
)

// Configured 配置是否具备「可以试一次」的条件。
// edge 只判命令在不在：真正的失败留给 Synthesize 返回，预检不必抢跑。
func Configured(c Config) bool {
	switch c.Kind {
	case KindEdge:
		return true
	case KindSAPI:
		return true // Windows 上恒可用；非 Windows 在合成时报错，不在这里拦
	case KindOpenAI, "":
		return strings.TrimSpace(c.BaseURL) != ""
	}
	return false
}

// Preflight 把「为什么现在用不了、要配什么」一次讲清（与 llm.Preflight 同口径）。
func Preflight(c Config) error {
	switch c.Kind {
	case KindEdge:
		if _, err := exec.LookPath("edge-tts"); err != nil {
			return fmt.Errorf("本机没有 edge-tts 命令（pip install edge-tts 后重试）")
		}
		return nil
	case KindSAPI:
		return nil // 音色/引擎是否存在，合成时才知道
	case KindOpenAI, "":
		if strings.TrimSpace(c.BaseURL) == "" {
			return fmt.Errorf("未填写 TTS 服务地址(Base URL)")
		}
		return nil
	default:
		return fmt.Errorf("不认识的 TTS 类型 %q（支持 openai / edge）", c.Kind)
	}
}

// Synthesize 合成一句话。ctx 控制整体超时，c.TimeoutSec 只兜底没传 ctx 的调用方。
func Synthesize(ctx context.Context, c Config, text string) (Result, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Result{}, fmt.Errorf("TTS 文本为空")
	}
	if r := []rune(text); len(r) > maxTextRunes {
		text = string(r[:maxTextRunes])
	}
	if c.TimeoutSec <= 0 {
		c.TimeoutSec = defaultTimeoutSec
	}
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(c.TimeoutSec)*time.Second)
		defer cancel()
	}

	start := time.Now()
	var (
		audio []byte
		mime  string
		err   error
	)
	switch c.Kind {
	case KindEdge:
		audio, mime, err = synthEdge(ctx, c, text)
	case KindSAPI:
		audio, mime, err = synthSAPI(ctx, text, c.Voice, c.TimeoutSec)
	case KindOpenAI, "":
		audio, mime, err = synthOpenAI(ctx, c, text)
	default:
		err = fmt.Errorf("不认识的 TTS 类型 %q（支持 openai / edge）", c.Kind)
	}
	if err != nil {
		return Result{}, err
	}
	if len(audio) == 0 {
		return Result{}, fmt.Errorf("TTS 返回空音频")
	}
	return Result{Audio: audio, MIME: mime, Ms: time.Since(start).Milliseconds()}, nil
}

// synthOpenAI 走 OpenAI 兼容的 /audio/speech，返回原始音频字节。
func synthOpenAI(ctx context.Context, c Config, text string) ([]byte, string, error) {
	payload := map[string]any{
		"model": modelOr(c.Model, "tts-1"),
		"input": text,
		"voice": voiceOr(c.Voice, defaultVoiceOpenAI),
	}
	if c.Speed > 0 {
		payload["speed"] = c.Speed
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, speechURL(c.BaseURL), bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	client := &http.Client{Timeout: time.Duration(c.TimeoutSec) * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	audio, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("TTS 服务返回 HTTP %d: %s", resp.StatusCode, snippet(audio))
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = "audio/mpeg"
	}
	return audio, mime, nil
}

// synthEdge 调本机 edge-tts。它只输出文件不吐 stdout，所以落临时目录再读回来；
// 临时目录随调用销毁，不在主播机器上留垃圾。
func synthEdge(ctx context.Context, c Config, text string) ([]byte, string, error) {
	bin, err := exec.LookPath("edge-tts")
	if err != nil {
		return nil, "", fmt.Errorf("本机没有 edge-tts 命令（pip install edge-tts 后重试）")
	}
	dir, err := os.MkdirTemp("", "dytts-")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	out := filepath.Join(dir, "out.mp3")

	args := []string{
		// --no-progress：edge-tts 默认往 stderr 刷进度条，失败时那堆字符会掩盖真正的错误行。
		"--no-progress",
		"--voice", voiceOr(c.Voice, defaultVoiceEdge),
		"--text", text,
		"--write-media", out,
	}
	// edge-tts 的语速是百分比增量，跟 OpenAI 的倍率不是一套单位，这里换算。
	if c.Speed > 0 && math.Abs(c.Speed-1) > 0.01 {
		args = append(args, fmt.Sprintf("--rate=%+d%%", int(math.Round((c.Speed-1)*100))))
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, "", fmt.Errorf("edge-tts 失败: %v: %s", err, snippet(b))
	}
	audio, err := os.ReadFile(out)
	if err != nil {
		return nil, "", fmt.Errorf("edge-tts 没产出音频: %w", err)
	}
	return audio, "audio/mpeg", nil
}

// speechURL 兼容用户填了完整 /audio/speech 或只填 /v1 基址两种形态（同 llm.completionsURL）。
func speechURL(base string) string {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/audio/speech") {
		return base
	}
	return base + "/audio/speech"
}

func voiceOr(v, def string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return def
}

func modelOr(m, def string) string {
	if m = strings.TrimSpace(m); m != "" {
		return m
	}
	return def
}

// snippet 把错误响应压成一行，避免把整个 HTML 错误页塞进日志。
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.ReplaceAll(s, "\n", " ")
	if r := []rune(s); len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}
