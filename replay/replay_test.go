package replay

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/athlon18/dafeiyu/rules"
)

// 回放包的唯一考点：没房间、没声卡，也得证明「冷场时她会开口」。
// 所以断言只准看 Run 返回的 Result 和 Load 的报错，不看包内状态——
// 包内改坏了、这些门就得变红，这才是门。

func fixture(name string) string { return filepath.Join("..", "testdata", name) }

// voiceSession 一份"该有的出声腿都配上"的回放配置：模板话术齐全、不依赖 LLM。
func voiceSession(frames []Frame) Session {
	return Session{
		Frames:       frames,
		Step:         time.Millisecond,
		GuideEvery:   30 * time.Second,
		IdleAfter:    20 * time.Second,
		GuideLines:   []string{"想看下一局的扣个 1"},
		IdleLines:    []string{"直播间有点安静，谁来陪我聊两句"},
		OfflineLines: []string{"今天就到这里，明天见"},
		EnterLine:    "欢迎 {user} 来到直播间",
		GiftLines: []rules.Line{
			{Match: "小心心", Lines: []string{"{user} 比了个心"}},
			{Match: "嘉年华", Lines: []string{"感谢 {user} 的嘉年华"}},
		},
	}
}

func TestReplayDrivesGuideAndIdleWithoutFrames(t *testing.T) {
	frames, err := Load(fixture("frames.jsonl"))
	if err != nil {
		t.Fatalf("读不到回放帧: %v", err)
	}
	frames = append(frames, Frame{T: 420, Kind: string(rules.KindOffline)})
	var delivered []rules.Event
	s := voiceSession(frames)
	s.Sink = func(ev rules.Event) { delivered = append(delivered, ev) }

	res := Run(context.Background(), s)

	if res.ByKind[rules.KindGuide] == 0 || res.ByKind[rules.KindIdle] == 0 {
		t.Fatalf("整场回放下来引导 %d 条、兜底 %d 条——冷场时一声不吭的这不是交付，是一张贴在屏幕上的静音贴纸："+
			"Run 没把 rules 的 NextGuide/NextIdle 接到虚拟钟上。结果: %+v", res.ByKind[rules.KindGuide], res.ByKind[rules.KindIdle], res)
	}
	for _, k := range []rules.Kind{rules.KindGift, rules.KindChat, rules.KindEnter, rules.KindOffline} {
		if res.ByKind[k] == 0 {
			t.Fatalf("观众帧 %q 一条都没换来出声，说明话术表没配进回放: %+v", k, res)
		}
	}
	if res.Emitted < res.ByKind[rules.KindGift]+res.ByKind[rules.KindChat]+res.ByKind[rules.KindEnter]+
		res.ByKind[rules.KindGuide]+res.ByKind[rules.KindIdle] {
		t.Fatalf("Emitted=%d 比各来源加起来还少，计数在漏: %+v", res.Emitted, res)
	}
	audience := 0
	for _, ev := range delivered {
		if ev.Kind == rules.KindGift || ev.Kind == rules.KindChat ||
			ev.Kind == rules.KindEnter || ev.Kind == rules.KindOffline {
			audience++
		}
	}
	if audience != len(frames) {
		t.Fatalf("Sink 只收到 %d 条观众帧，文件里有 %d 帧——丢帧的回放会假装房间很安静", audience, len(frames))
	}
}

// 引导/兜底的句子必须原样交给 Sink：只回一个"发生了"的信号，上层就没法验文本真走到了编排器。
func TestSinkReceivesHostLinesWithText(t *testing.T) {
	var guideText, idleText []string
	// 必须有一条观众帧打底：没来过人的房间，兜底按契约是"不判冷场"（可能只是没连上）。
	s := voiceSession([]Frame{{T: 0, Kind: string(rules.KindChat), User: "甲", Content: "在吗"}})
	s.Sink = func(ev rules.Event) {
		switch ev.Kind {
		case rules.KindGuide:
			guideText = append(guideText, ev.Content)
		case rules.KindIdle:
			idleText = append(idleText, ev.Content)
		}
	}
	Run(context.Background(), s)
	if len(guideText) == 0 || guideText[0] != "想看下一局的扣个 1" {
		t.Fatalf("引导没把句子交出来: %v", guideText)
	}
	if len(idleText) == 0 || idleText[0] != "直播间有点安静，谁来陪我聊两句" {
		t.Fatalf("兜底没把句子交出来: %v", idleText)
	}
}

func TestReplayStopsWithoutCancel(t *testing.T) {
	watchdog := time.AfterFunc(5*time.Second, func() {
		t.Error("Run 5 秒还没自己回来：虚拟钟过了观察窗又没人取消 ctx，它就该停；卡住说明回放没有边界")
	})
	defer watchdog.Stop()

	done := make(chan Result, 1)
	frames, err := Load(fixture("frames.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	go func() { done <- Run(context.Background(), voiceSession(frames)) }()

	select {
	case res := <-done:
		if res.ByKind[rules.KindGuide] == 0 {
			t.Fatalf("自己停了，但一场引导都没播: %+v", res)
		}
	case <-time.After(5 * time.Second):
	}
}

func TestLoadRejectsUnknownKind(t *testing.T) {
	_, err := Load(fixture("bad.jsonl"))
	if err == nil {
		t.Fatal("未知 kind 被静默吞了：少一帧就等于房间更安静，冷场测试会说谎")
	}
	path := fixture("bad.jsonl")
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), ":2") {
		t.Fatalf("报错得指出 路径:行号 才修得动，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "like") {
		t.Fatalf("报错没说是哪个 kind: %v", err)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	_, err := Load(fixture("broken.jsonl"))
	if err == nil {
		t.Fatal("坏 JSON 被跳过了：同上一条，丢帧的回放不可信")
	}
	path := fixture("broken.jsonl")
	if !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), ":2") {
		t.Fatalf("报错得指出 路径:行号 才修得动，实际: %v", err)
	}
}

// 主播自己说话不算观众活动：引导/兜底不许把冷场钟往后推。
func TestHostLinesDoNotAdvanceColdRoomClock(t *testing.T) {
	s := Session{
		Frames:     []Frame{{T: 5, Kind: "chat", User: "路人乙", Content: "在吗"}},
		Step:       time.Millisecond,
		GuideEvery: 2 * time.Second,
		IdleAfter:  3 * time.Second,
		GuideLines: []string{"引导一", "引导二"},
		IdleLines:  []string{"兜底一", "兜底二"},
	}
	res := Run(context.Background(), s)

	if got := res.LastFrame.UTC().Format(time.RFC3339); got != "1970-01-01T00:00:05Z" {
		t.Fatalf("全场只有一条 t=5s 的弹幕，LastFrame 却是 %s——引导/兜底被当成观众活动，"+
			"她自己的声音把冷场钟重置了，回放会永远报「房间不冷」", got)
	}
	if res.ByKind[rules.KindGuide] < 2 || res.ByKind[rules.KindIdle] < 2 {
		t.Fatalf("t=5s 之后本该反复出声，实际引导 %d 条、兜底 %d 条: %+v",
			res.ByKind[rules.KindGuide], res.ByKind[rules.KindIdle], res)
	}
}
