package tts

// 流式播报：把一句话切成段，边合成边播。
//
// 为什么是"切段"而不是"真流式"：
// 真流式要求播放器能接 PCM 流（waveOut 逐块推），而 Windows 上现成的 PlaySound
// 只认**完整文件**。换成 waveOut 要自己解 mp3/做重采样/管缓冲——那是另一个量级的活，
// 而且一旦写错就是"声音卡顿/爆音"，比慢一点更难听。
//
// 切段能拿到大头收益：首段合成完就出声（约 0.6 秒），后面的段在这一段播放期间
// 已经合成好了。代价是段与段之间有个几十毫秒的接缝，听起来像换气，不像卡顿。

import (
	"context"
	"log"
	"strings"
)

// SpeakOptions 切段参数。
type SpeakOptions struct {
	MaxSegmentRunes int // 每段最多多少字（太短会切得很碎，默认 18）
	MaxSegments     int // 最多切几段（默认 4；超了就把剩下的并进最后一段）
	// OnPlayStart 在每一段**真正出声前**调用（可能多段）。
	// 给调用方用来压背景音乐、登记口型包络：合成那两秒不该让音乐停着，出声前才停。
	OnPlayStart func(Result)
	// OnPlayEnd 在说完/被打断后调一次（仅当 OnPlayStart 调过）。
	OnPlayEnd func()
}

func (o SpeakOptions) normalized() SpeakOptions {
	if o.MaxSegmentRunes <= 0 {
		o.MaxSegmentRunes = 18
	}
	if o.MaxSegments <= 0 {
		o.MaxSegments = 4
	}
	return o
}

// SpeakStat 一次播报的统计（给「AI 运行台」用）。
// SynthMs 是**累计**合成耗时：多段并行时它会大于墙钟，回答问题“音频总共花了多少模型侧时间”。
type SpeakStat struct {
	Segments int
	SynthMs  int64
	Bytes    int
}

// Speak 合成并播放一句话。短句走原来的"整句合成再播"，长句走切段流水线。
//
// 任何一段合成失败都只丢这一段（记日志继续），不把整句打断——
// 直播里"少说半句"远好过"突然静音到下一句"。
func Speak(ctx context.Context, p *Player, cfg Config, text string, opt SpeakOptions) (SpeakStat, error) {
	var st SpeakStat
	segs := SplitForStream(text, opt)
	if len(segs) == 0 {
		return st, nil
	}
	st.Segments = len(segs)
	// 只在“真的要出声”时通知调用方（压音乐），合成阶段不通知；
	// 无论正常说完、部分失败还是被取消，只要开过口就必须成对回调。
	started := false
	play := func(res Result) error {
		if opt.OnPlayStart != nil {
			opt.OnPlayStart(res)
		}
		started = true
		return p.Play(ctx, res.Audio, res.MIME)
	}
	defer func() {
		if started && opt.OnPlayEnd != nil {
			opt.OnPlayEnd()
		}
	}()
	if len(segs) == 1 {
		res, err := Synthesize(ctx, cfg, segs[0])
		if err != nil {
			return st, err
		}
		st.SynthMs, st.Bytes = res.Ms, len(res.Audio)
		return st, play(res)
	}

	// 流水线：同一时刻只有一个播放，但合成可以和播放并行。
	// 通道带 1 的缓冲：下一段合成好就交出去，不阻塞合成协程。
	type slot struct {
		res Result
		err error
	}
	next := make(chan slot, 1)
	synth := func(seg string) {
		res, err := Synthesize(ctx, cfg, seg)
		select {
		case next <- slot{res, err}:
		case <-ctx.Done():
		}
	}
	go synth(segs[0])

	var firstErr error
	played := 0
	for i := range segs {
		var s slot
		select {
		case s = <-next:
		case <-ctx.Done():
			return st, nil // 被打断（抢话/下播）不是故障
		}
		// 先把下一段的合成发出去，再播当前这段：播放期间它就能合成完。
		if i+1 < len(segs) {
			go synth(segs[i+1])
		}
		if s.err != nil {
			log.Printf("[tts] 第 %d/%d 段合成失败（跳过这一段）：%v", i+1, len(segs), s.err)
			if firstErr == nil {
				firstErr = s.err
			}
			continue
		}
		st.SynthMs += s.res.Ms
		st.Bytes += len(s.res.Audio)
		if err := play(s.res); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		played++
	}
	// 只有一段都没播出去才算失败；部分成功就是成功（观众已经听到了）。
	if played == 0 && firstErr != nil {
		return st, firstErr
	}
	return st, nil
}

// SplitForStream 把一句话切成若干段。
//
// 切点优先选标点（，。！？；、…），没有标点才硬切；
// 过短的片段会被并进前一段——每段都只有三四个字的话，接缝会比停顿还密。
func SplitForStream(text string, opt SpeakOptions) []string {
	opt = opt.normalized()
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	runes := []rune(text)
	if len(runes) <= opt.MaxSegmentRunes {
		return []string{text}
	}

	var out []string
	var cur []rune
	flush := func() {
		if s := strings.TrimSpace(string(cur)); s != "" {
			out = append(out, s)
		}
		cur = cur[:0]
	}

	for _, r := range runes {
		cur = append(cur, r)
		// 到标点且长度过半，或者硬到上限，就切
		if isBreakPunct(r) && len(cur) >= opt.MaxSegmentRunes/2 {
			flush()
			continue
		}
		if len(cur) >= opt.MaxSegmentRunes {
			flush()
		}
	}
	flush()

	// 段数封顶：超出的并进最后一段，避免一句话被切成七八段、接缝比话还多。
	if len(out) > opt.MaxSegments {
		head := out[:opt.MaxSegments-1]
		tail := strings.Join(out[opt.MaxSegments-1:], "")
		out = append(head, tail)
	}
	return out
}

func isBreakPunct(r rune) bool {
	switch r {
	case '，', '。', '！', '？', '；', '、', '…', '~', '\n', ',', '.', '!', '?', ';':
		return true
	}
	return false
}
