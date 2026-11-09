package rules

import (
	"testing"
	"time"
)

// 话术（Speak/Line）在编排器里走的是"配好的句子"分支，那条分支不查单人冷却——
// 所以冷却必须在 rules 这层自己兜住，否则同一个人刷屏就会被逐条答谢。

func TestPerUserCooldownWithinEventClass(t *testing.T) {
	r := mustRules(t, baseConfig())
	ev := Event{Kind: KindGift, User: "老板甲", UID: "u1", GiftName: "嘉年华", GiftCount: 1}
	if _, ok := r.Match(ev, time.Unix(0, 0)); !ok {
		t.Fatal("首次命中应放行")
	}
	if _, ok := r.Match(ev, time.Unix(3, 0)); ok {
		t.Fatal("同一个人 3 秒内再送同档礼物还被答谢 = 逐条复读")
	}
	if _, ok := r.Match(ev, time.Unix(31, 0)); !ok {
		t.Fatal("过了冷却期应重新放行")
	}
}

func TestCooldownIsPerClassNotGlobal(t *testing.T) {
	r := mustRules(t, baseConfig())
	if _, ok := r.Match(Event{Kind: KindGift, UID: "u1", GiftName: "嘉年华", GiftCount: 1}, time.Unix(0, 0)); !ok {
		t.Fatal("礼物应命中")
	}
	in, ok := r.Match(Event{Kind: KindEnter, UID: "u1", User: "同一个老板"}, time.Unix(1, 0))
	if !ok {
		t.Fatal("进场不该被礼物冷却挡掉")
	}
	if in.Speak != "欢迎 同一个老板" {
		t.Fatalf("进场模板没渲染: %+v", in)
	}
}

func TestEnterNeedsNoLLM(t *testing.T) {
	r := mustRules(t, baseConfig())
	in, _ := r.Match(Event{Kind: KindEnter, UID: "u9", User: "新人"}, time.Unix(0, 0))
	if in.Speak == "" {
		t.Fatal("进场欢迎必须自带话术，否则没配 LLM 时这一段是哑的")
	}
}

func TestDifferentUsersNotBlocked(t *testing.T) {
	r := mustRules(t, baseConfig())
	if _, ok := r.Match(Event{Kind: KindGift, UID: "a", GiftName: "嘉年华", GiftCount: 1}, time.Unix(0, 0)); !ok {
		t.Fatal("甲的礼物没命中")
	}
	if _, ok := r.Match(Event{Kind: KindGift, UID: "b", GiftName: "嘉年华", GiftCount: 1}, time.Unix(1, 0)); !ok {
		t.Fatal("乙 1 秒后的同档礼物被甲的冷却挡了")
	}
}
