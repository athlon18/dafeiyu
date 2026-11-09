package host

import (
	"testing"
)

// 蒸馏来的 compose 只会走 LLM，没有模板兜底。边车的第一条腿是"没配 key 也能开口"，
// 所以事件（给叠层看）和话术（给嘴说）必须是两个字段：
// Gift/Enter 让弹幕流显示真实发生的事，Speak 让嘴能说出配好的句子。

func TestSpeakRendersVerbatimWithoutThinker(t *testing.T) {
	th := &fakeThinker{reply: "不该被用到"}
	cfg := Config{GlobalGapSec: 0}
	h := newTestHost(t, cfg, th, &fakeGuard{}, &fakeSpeaker{})

	if !h.Handle(ChatInput{User: "老板甲", Gift: "嘉年华", Count: 1, Speak: "感谢老板甲的嘉年华"}) {
		t.Fatal("礼物事件没被收下")
	}
	waitSaid(t, h, "感谢老板甲的嘉年华")
	if n := th.callCount(); n != 0 {
		t.Fatalf("配了 Speak 还去调 LLM，调了 %d 次", n)
	}
}

// Speak 不替代事件本身：叠层要看得见"谁送了什么"。
func TestSpeakKeepsEventVisibleInFeed(t *testing.T) {
	cfg := Config{GlobalGapSec: 0}
	h := newTestHost(t, cfg, &fakeThinker{reply: "好"}, &fakeGuard{}, &fakeSpeaker{})
	h.Handle(ChatInput{User: "老板甲", Gift: "嘉年华", Count: 2, Speak: "感谢老板甲"})
	waitSaid(t, h, "感谢老板甲")
	feed, _ := h.Stats()["feed"].([]string)
	if len(feed) != 1 {
		t.Fatalf("礼物事件应进弹幕流一条: %v", feed)
	}
	if feed[0] != "老板甲 送出 嘉年华×2" {
		t.Fatalf("弹幕流文案不对: %q", feed[0])
	}
}

func TestSpeakForEnterShowsWelcomeInFeed(t *testing.T) {
	cfg := Config{GlobalGapSec: 0}
	h := newTestHost(t, cfg, &fakeThinker{reply: "好"}, &fakeGuard{}, &fakeSpeaker{})
	h.Handle(ChatInput{User: "新人", Enter: true, Speak: "欢迎新人进入直播间"})
	waitSaid(t, h, "欢迎新人进入直播间")
	feed, _ := h.Stats()["feed"].([]string)
	if len(feed) != 1 || feed[0] != "新人 进入直播间" {
		t.Fatalf("进场要能在弹幕流里看见: %v", feed)
	}
}

func TestBlankSpeakFallsBackToThinker(t *testing.T) {
	th := &fakeThinker{reply: "LLM 给的"}
	cfg := Config{GlobalGapSec: 0}
	h := newTestHost(t, cfg, th, &fakeGuard{}, &fakeSpeaker{})
	h.Handle(ChatInput{User: "a", Content: "在吗", AtMe: true, Speak: "   "})
	waitSaid(t, h, "LLM 给的")
	if th.callCount() != 1 {
		t.Fatalf("空白 Speak 应回落到 LLM, 实际调用 %d 次", th.callCount())
	}
}

func TestSpeakHonorsMaxSayRunes(t *testing.T) {
	cfg := Config{GlobalGapSec: 0, MaxSayRunes: 4}
	h := newTestHost(t, cfg, &fakeThinker{reply: "好"}, &fakeGuard{}, &fakeSpeaker{})
	h.Handle(ChatInput{User: "a", Enter: true, Speak: "这句太长要被截断"})
	waitSaid(t, h, "这句太长")
}
