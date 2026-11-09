package rules

import (
	"testing"
	"time"

	"github.com/athlon18/dafeiyu/host"
)

func TestStageOfThresholds(t *testing.T) {
	cases := []struct {
		count uint64
		want  uint8
	}{{0, 1}, {9, 1}, {10, 2}, {49, 2}, {50, 3}, {99, 3}, {100, 4}, {5000, 4}}
	for _, c := range cases {
		if got := StageOf(c.count); got != c.want {
			t.Errorf("StageOf(%d)=%d want %d", c.count, got, c.want)
		}
	}
}

func TestIsEpicOnlyListedNames(t *testing.T) {
	if !IsEpic("嘉年华") || !IsEpic("超级跑车") {
		t.Fatal("出厂 epic 名单丢了")
	}
	if IsEpic("小心心") {
		t.Fatal("未列入名单的礼物不许自判 epic")
	}
}

func baseConfig() Config {
	return Config{
		RoomNo:    "7",
		EnterLine: "欢迎 {user}",
		GiftLines: []Line{
			{Match: "嘉年华", Stage: 0, Lines: []string{"感谢 {user} 的嘉年华"}},
			{Match: "小心心", Stage: 2, Lines: []string{"{user} 连了 {count} 个小心心", "又是一波小心心"}},
		},
		Guide:      []string{"引导一", "引导二"},
		GuideEvery: 10 * time.Second,
		IdleAfter:  5 * time.Second,
		IdleLines:  []string{"兜底一", "兜底二"},
		OfflineLines: []string{"今天就到这里"},
	}
}

func mustRules(t *testing.T, cfg Config) *Rules {
	t.Helper()
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestGiftMatchRendersTemplate(t *testing.T) {
	r := mustRules(t, baseConfig())
	in, ok := r.Match(Event{Kind: KindGift, User: "老板甲", GiftName: "嘉年华", GiftCount: 1}, time.Unix(0, 0))
	if !ok {
		t.Fatal("嘉年华没命中")
	}
	if in.Gift != "嘉年华" || in.Count != 1 {
		t.Fatalf("礼物字段没带上，叠层就显示不出送了什么: %+v", in)
	}
	if in.Speak != "感谢 老板甲 的嘉年华" {
		t.Fatalf("话术没渲染: %q", in.Line)
	}
	if in.RoomNo != "7" || in.UID == "" && in.User == "" {
		t.Fatalf("房间/身份没带: %+v", in)
	}
}

func TestGiftUnlistedMisses(t *testing.T) {
	r := mustRules(t, baseConfig())
	if _, ok := r.Match(Event{Kind: KindGift, User: "a", GiftName: "棒棒糖", GiftCount: 1}, time.Unix(0, 0)); ok {
		t.Fatal("表外礼物被自造了话术")
	}
}

func TestStageGateRejectsBelowStage(t *testing.T) {
	r := mustRules(t, baseConfig())
	if _, ok := r.Match(Event{Kind: KindGift, User: "a", GiftName: "小心心", GiftCount: 3}, time.Unix(0, 0)); ok {
		t.Fatal("stage=2 的规则被 1 档连击命中了")
	}
	in, ok := r.Match(Event{Kind: KindGift, User: "a", GiftName: "小心心", GiftCount: 20}, time.Unix(0, 0))
	if !ok {
		t.Fatal("2 档连击没命中")
	}
	if in.Speak != "a 连了 20 个小心心" {
		t.Fatalf("话术没渲染: %q", in.Line)
	}
}

// 同一句话术轮着说，不然冷场时是复读机。
func TestGiftLinesRotate(t *testing.T) {
	r := mustRules(t, baseConfig())
	first, _ := r.Match(Event{Kind: KindGift, User: "a", GiftName: "小心心", GiftCount: 20}, time.Unix(0, 0))
	second, _ := r.Match(Event{Kind: KindGift, User: "b", GiftName: "小心心", GiftCount: 30}, time.Unix(0, 0))
	if first.Speak == second.Speak {
		t.Fatalf("两条话术轮播没生效: %q / %q", first.Speak, second.Speak)
	}
}

func TestChatKeepsRawContentWithoutTemplate(t *testing.T) {
	r := mustRules(t, baseConfig())
	in, ok := r.Match(Event{Kind: KindChat, User: "a", Content: "今天玩什么", AtMe: true}, time.Unix(0, 0))
	if !ok {
		t.Fatal("弹幕没被收下")
	}
	if in.Content != "今天玩什么" || !in.AtMe || in.Speak != "" {
		t.Fatalf("没配模板时弹幕应原样交给编排/LLM: %+v", in)
	}
}

func TestChatTemplateBypassesLLM(t *testing.T) {
	cfg := baseConfig()
	cfg.ChatLine = "{user} 问的是：{content}"
	r := mustRules(t, cfg)
	in, ok := r.Match(Event{Kind: KindChat, User: "a", Content: "几点了"}, time.Unix(0, 0))
	if !ok {
		t.Fatal("没命中")
	}
	if in.Speak != "a 问的是：几点了" {
		t.Fatalf("模板没生效: %+v", in)
	}
}

func TestEmptyChatMisses(t *testing.T) {
	r := mustRules(t, baseConfig())
	if _, ok := r.Match(Event{Kind: KindChat, User: "a", Content: "   "}, time.Unix(0, 0)); ok {
		t.Fatal("空弹幕不该开口")
	}
}

func TestEnterCarriesEnterFlagAndNoLine(t *testing.T) {
	r := mustRules(t, baseConfig())
	in, ok := r.Match(Event{Kind: KindEnter, User: "新人"}, time.Unix(0, 0))
	if !ok || !in.Enter {
		t.Fatalf("进场没打 Enter: %+v ok=%v", in, ok)
	}
	// 进场话术由 host 的 Welcome 分支处理，这里不自渲染，免得两处口径打架。
	if in.Line != "" {
		t.Fatalf("进场不该抢着渲染话术: %q", in.Line)
	}
}

func TestOfflineUsesLineWithoutAudienceIdentity(t *testing.T) {
	r := mustRules(t, baseConfig())
	in, ok := r.Match(Event{Kind: KindOffline}, time.Unix(0, 0))
	if !ok || in.Line == "" {
		t.Fatalf("下播收场必须走 Line: %+v ok=%v", in, ok)
	}
	if in.User != "" || in.Content != "" || in.Gift != "" || in.Enter {
		t.Fatalf("下播收场不该有观众身份: %+v", in)
	}
}

func TestOfflineMissesWithoutLines(t *testing.T) {
	cfg := baseConfig()
	cfg.OfflineLines = nil
	r := mustRules(t, cfg)
	if _, ok := r.Match(Event{Kind: KindOffline}, time.Unix(0, 0)); ok {
		t.Fatal("没配收场话术却造出了一句")
	}
}

func TestGuideRotatesEveryInterval(t *testing.T) {
	r := mustRules(t, baseConfig())
	t0 := time.Unix(0, 0)
	a, ok := r.NextGuide(t0)
	if !ok || a.Line != "引导一" {
		t.Fatalf("首条引导没播出: %+v ok=%v", a, ok)
	}
	if a.Idle {
		t.Fatal("引导不该冒充暖场，否则会被 IdleTalkEnabled 挡掉")
	}
	if _, ok := r.NextGuide(t0.Add(1 * time.Second)); ok {
		t.Fatal("间隔内重复播引导")
	}
	b, ok := r.NextGuide(t0.Add(10 * time.Second))
	if !ok || b.Line != "引导二" {
		t.Fatalf("引导没轮播: %+v ok=%v", b, ok)
	}
}

func TestGuideDisabledWithoutLines(t *testing.T) {
	cfg := baseConfig()
	cfg.Guide = nil
	r := mustRules(t, cfg)
	if _, ok := r.NextGuide(time.Unix(0, 0)); ok {
		t.Fatal("没配引导话术却产出引导")
	}
}

func TestIdleOnlyAfterQuietWindow(t *testing.T) {
	r := mustRules(t, baseConfig())
	last := time.Unix(0, 0)
	if _, ok := r.NextIdle(last.Add(2*time.Second), last); ok {
		t.Fatal("没到安静窗口就兜底")
	}
	in, ok := r.NextIdle(last.Add(6*time.Second), last)
	if !ok || !in.Idle || in.Line != "兜底一" {
		t.Fatalf("冷场兜底没走 Idle+Line: %+v ok=%v", in, ok)
	}
}

func TestIdleNeedsKnownLastFrame(t *testing.T) {
	r := mustRules(t, baseConfig())
	if _, ok := r.NextIdle(time.Now(), time.Time{}); ok {
		t.Fatal("从没收到过帧时不该兜底（可能压根没连上房间）")
	}
}

func TestIdleDoesNotRepeatWithinWindow(t *testing.T) {
	r := mustRules(t, baseConfig())
	last := time.Unix(0, 0)
	if _, ok := r.NextIdle(last.Add(6*time.Second), last); !ok {
		t.Fatal("首次兜底就该放行")
	}
	if _, ok := r.NextIdle(last.Add(7*time.Second), last); ok {
		t.Fatal("同一安静窗口内重复兜底 = 复读机")
	}
}

func TestNewRejectsLinesWithoutInterval(t *testing.T) {
	cfg := baseConfig()
	cfg.GuideEvery = 0
	if _, err := New(cfg); err == nil {
		t.Fatal("配了引导话术却没给周期，必须报错")
	}
	cfg = baseConfig()
	cfg.IdleAfter = 0
	if _, err := New(cfg); err == nil {
		t.Fatal("配了兜底话术却没给安静阈值，必须报错")
	}
}

// Match 的返回值必须能被 host.Handle 直接收下——两包之间的契约就在这条。
func TestMatchedInputsAreAcceptedByHost(t *testing.T) {
	r := mustRules(t, baseConfig())
	h := host.New(host.Config{Enabled: true, RoomNo: "7", QueueSize: 8, MaxSayRunes: 60, GlobalGapSec: 0}, nil, nil, nil)
	h.Start()
	defer h.Stop()
	for _, ev := range []Event{
		{Kind: KindGift, User: "a", GiftName: "嘉年华", GiftCount: 1},
		{Kind: KindEnter, User: "b"},
		{Kind: KindOffline},
	} {
		in, ok := r.Match(ev, time.Unix(0, 0))
		if !ok {
			t.Fatalf("事件没命中: %+v", ev)
		}
		if !h.Handle(in) {
			t.Fatalf("命中的事件被编排器挡了: %+v -> %+v", ev, in)
		}
	}
}
