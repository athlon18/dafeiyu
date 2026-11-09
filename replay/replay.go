// Package replay 把一份 JSONL 帧文件打在虚拟钟上，喂给线上同一条 rules.Rules，
// 用来回答那句只会吼出来的问题：「冷场的时候她到底会不会开口」。
//
// 为什么要有这个包：验证冷场出声不需要直播间、也不需要声卡、也不需要 API key。
// 真实链路要等开播才有样本，而"模板这条腿通不通"必须在一台没网没麦克风的机器上
// 就能跑出红绿。因此回放绝不悄悄丢帧——丢一帧就等于把房间假装得更安静，
// 冷场测试会因此说谎：Load 只认四类观众 kind，坏行直接带 路径:行号 报错。
package replay

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/athlon18/dafeiyu/rules"
)

// Frame 一行 JSONL = 一帧观众侧事件。t 是相对回放开始的秒数。
type Frame struct {
	T       float64 `json:"t"`
	Kind    string  `json:"kind"`
	User    string  `json:"user"`
	Content string  `json:"content"`
	AtMe    bool    `json:"atMe"`
	Gift    string  `json:"gift"`
	Count   int     `json:"count"`
}

// audience 只列观众会干的事。guide/idle 是主播自己的输出，混进帧文件里就等于
// 让脚本替主播把话说了，回放测的就不再是"她会不会开口"。
var audience = map[rules.Kind]bool{
	rules.KindGift:    true,
	rules.KindEnter:   true,
	rules.KindChat:    true,
	rules.KindOffline: true,
}

// Load 读帧文件。未知 kind 或坏 JSON 一律报错并指出 路径:行号。
func Load(path string) ([]Frame, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var out []Frame
	for line := 1; sc.Scan(); line++ {
		text := strings.TrimSpace(sc.Text())
		if text == "" {
			continue
		}
		var fr Frame
		if err := json.Unmarshal([]byte(text), &fr); err != nil {
			return nil, fmt.Errorf("%s:%d: 这一行不是合法 JSON: %w", path, line, err)
		}
		kind := rules.Kind(fr.Kind)
		if !audience[kind] {
			return nil, fmt.Errorf("%s:%d: 未知 kind %q，回放只认 gift/enter/chat/offline", path, line, fr.Kind)
		}
		out = append(out, fr)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%s: 读取失败: %w", path, err)
	}
	return out, nil
}

// Session 一次回放的输入：帧 + 话术配置 + 观察帧去向的 Sink。
// 话术字段直接给到 rules.Config，所以回放走的匹配表和线上完全同一份。
type Session struct {
	Frames       []Frame
	Step         time.Duration
	GuideEvery   time.Duration
	IdleAfter    time.Duration
	GuideLines   []string
	IdleLines    []string
	OfflineLines []string
	EnterLine    string
	GiftLines    []rules.Line
	Sink         func(rules.Event)
}

// emitHost 把主播自己产出的一句话（引导/兜底）也交给 Sink，
// 好让上层能验"句子真走到了编排器"，而不只是计数器加了一。
func (s Session) emitHost(k rules.Kind, line string) {
	if s.Sink == nil {
		return
	}
	s.Sink(rules.Event{Kind: k, Content: line})
}

// Result 回放结论。Emitted = 递到编排器门口的句子总数。
// 注意：弹幕帧没配 ChatLine 时只代表"交给编排器"（线上这一步可能再进 LLM），
// 回放没有 LLM，所以想靠模板腿证明会出声，就得配齐 EnterLine/GiftLines。
type Result struct {
	Emitted   int
	LastFrame time.Time
	ByKind    map[rules.Kind]int
}

const (
	virtualTick   = time.Second
	coldRoomWatch = 400 * time.Second
	// maxTicks 是硬边界：有人把 t 写成 1e12 时，回放也必须在有穷的步数内收工。
	maxTicks = 1 << 16
	// paceBatch 攒够一批 tick 才睡一次。Windows 的定时器粒度是十几毫秒，
	// 逐 tick 睡 Step 会让"几百毫秒跑完 400 秒"变成几分钟，回放自己先把测试超时。
	paceBatch = 64
)

// Run 在虚拟钟上跑一遍。虚拟钟一个 tick 走 1 秒，起点是 Unix epoch——
// 刻意不用零值时间，因为 rules.NextIdle 把零值读成"这房间一帧都没来过"，
// 那样 t=0 的帧就永远算不出冷场。LastFrame.Unix() 因此就是最后一条观众帧的相对秒数。
//
// Step 是一个虚拟 tick 的真实时长：线上不用等 400 秒，测试里给 time.Millisecond，
// 整个回放压进几百毫秒；不给它节流，循环就是纯空转烧 CPU。
func Run(ctx context.Context, s Session) Result {
	res := Result{ByKind: map[rules.Kind]int{}}

	r, err := rules.New(rules.Config{
		RoomNo:       "replay",
		Guide:        s.GuideLines,
		GuideEvery:   s.GuideEvery,
		IdleLines:    s.IdleLines,
		IdleAfter:    s.IdleAfter,
		OfflineLines: s.OfflineLines,
		EnterLine:    s.EnterLine,
		GiftLines:    s.GiftLines,
	})
	if err != nil {
		// Result 带不了 error：话术配歪了就退化成"一句都没说"，让门自己变红。
		return res
	}

	frames := byTime(s.Frames)
	end := coldRoomWatch
	if len(frames) > 0 {
		if last := time.Duration(frames[len(frames)-1].T * float64(time.Second)); last > end {
			end = last
		}
	}
	ticks := int(end / virtualTick)
	if ticks > maxTicks {
		ticks = maxTicks
	}

	var pacer *time.Ticker
	if s.Step > 0 {
		pacer = time.NewTicker(s.Step * paceBatch)
		defer pacer.Stop()
	}

	base := time.Unix(0, 0).UTC()
	next := 0
	for tick := 0; tick <= ticks; tick++ {
		now := base.Add(time.Duration(tick) * virtualTick)

		// (a) 到点帧全部投递。冷却挡掉的帧也算"观众活动"——刷屏时房间不冷。
		for ; next < len(frames) && frames[next].T <= float64(tick); next++ {
			ev := event(frames[next])
			if s.Sink != nil {
				s.Sink(ev)
			}
			if _, ok := r.Match(ev, now); ok {
				res.Emitted++
				res.ByKind[ev.Kind]++
			}
			res.LastFrame = now // 零值=还没来过人，NextIdle 自己会挡
		}

		// (b) 主播自己的话：引导轮播 + 冷场兜底。它们不许动冷场钟。
		// 选中的句子也要交给 Sink——回放的意义是看"句子有没有走到编排器"，
		// 只计数不外传，cmd 那层就没法验。
		if in, ok := r.NextGuide(now); ok {
			res.Emitted++
			res.ByKind[rules.KindGuide]++
			s.emitHost(rules.KindGuide, in.Line)
		}
		if in, ok := r.NextIdle(now, res.LastFrame); ok {
			res.Emitted++
			res.ByKind[rules.KindIdle]++
			s.emitHost(rules.KindIdle, in.Line)
		}

		// (c) 帧喂完且过了观察窗就收工。
		if next >= len(frames) && time.Duration(tick)*virtualTick >= end {
			return res
		}
		if pacer != nil && (tick+1)%paceBatch == 0 {
			select {
			case <-ctx.Done():
				return res
			case <-pacer.C:
			}
		} else if ctx.Err() != nil {
			return res
		}
	}

	// 撞上 maxTicks 才走到这里：剩帧一次性补投，绝不静默丢，
	// 否则"房间很安静"这个结论就是假造的。
	for ; next < len(frames); next++ {
		ev := event(frames[next])
		if s.Sink != nil {
			s.Sink(ev)
		}
		if _, ok := r.Match(ev, base.Add(time.Duration(ticks)*virtualTick)); ok {
			res.Emitted++
			res.ByKind[ev.Kind]++
		}
	}
	return res
}

func event(f Frame) rules.Event {
	// 帧文件里没 UID，冷却就按昵称分人；线上是真 UID，同一昵称在回放里算同一个人。
	return rules.Event{
		Kind:      rules.Kind(f.Kind),
		User:      f.User,
		Content:   f.Content,
		AtMe:      f.AtMe,
		GiftName:  f.Gift,
		GiftCount: f.Count,
	}
}

// byTime 按 t 升序排。手工拼的帧文件顺序经常是记到哪写到哪，
// 回放按时间因果走才和线上一个样（不改动调用方传进来的切片）。
func byTime(in []Frame) []Frame {
	out := append([]Frame(nil), in...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out
}
