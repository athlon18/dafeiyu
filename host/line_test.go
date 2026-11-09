package host

import (
	"strings"
	"testing"
	"time"
)

// Line 是"没人触发也要说"的那类话（玩法引导 / 冷场兜底 / 下播收场）。
// 它必须原样播出、不惊动 LLM、也不混进弹幕流——三条各钉一个用例。

func waitSaid(t *testing.T, h *Host, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ := h.Stats()["lastSaid"].(string); got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("没等到播出 %q，实际 %v", want, h.Stats()["lastSaid"])
}

func TestLineSaysVerbatimWithoutThinker(t *testing.T) {
	th := &fakeThinker{reply: "不该被用到"}
	sp := &fakeSpeaker{}
	cfg := Config{GlobalGapSec: 0}
	h := newTestHost(t, cfg, th, &fakeGuard{}, sp)

	if !h.Handle(ChatInput{Line: "送出礼物就能触发一次互动"}) {
		t.Fatal("Line 输入没被收下")
	}
	waitSaid(t, h, "送出礼物就能触发一次互动")
	if n := th.callCount(); n != 0 {
		t.Fatalf("Line 不该惊动 LLM，调了 %d 次", n)
	}
}

func TestLineNotPushedIntoFeed(t *testing.T) {
	cfg := Config{GlobalGapSec: 0}
	h := newTestHost(t, cfg, &fakeThinker{reply: "好"}, &fakeGuard{}, &fakeSpeaker{})
	h.Handle(ChatInput{Line: "轮播引导"})
	waitSaid(t, h, "轮播引导")
	feed, _ := h.Stats()["feed"].([]string)
	for _, l := range feed {
		if l != "" {
			t.Fatalf("Line 是主播自己的话，不该伪装成观众发言进弹幕流: %q", l)
		}
	}
	if len(feed) != 0 {
		t.Fatalf("弹幕流应留空，实际 %v", feed)
	}
}

// 引导和兜底也必须受全局间隔约束，否则冷场时会连珠炮。
func TestLineHonorsGlobalGap(t *testing.T) {
	cfg := Config{GlobalGapSec: 1}
	h := newTestHost(t, cfg, &fakeThinker{reply: "好"}, &fakeGuard{}, &fakeSpeaker{})
	if !h.Handle(ChatInput{Line: "第一句"}) {
		t.Fatal("首句就被挡了")
	}
	waitSaid(t, h, "第一句")
	if h.Handle(ChatInput{Line: "第二句"}) {
		t.Fatal("全局间隔内的第二句 Line 应当被挡")
	}
	time.Sleep(1100 * time.Millisecond)
	if !h.Handle(ChatInput{Line: "隔够了再说"}) {
		t.Fatal("过了全局间隔就该放行")
	}
	waitSaid(t, h, "隔够了再说")
}

// 逐句留痕是合规回溯的唯一凭据：自播报不许写成"某观众说了什么"。
func TestOriginOfSelfAnnounce(t *testing.T) {
	if got := originOf(ChatInput{Line: "轮播引导"}); !strings.Contains(got, "自播报") {
		t.Fatalf("引导句的留痕应标明自播报，实际 %q", got)
	}
	if got := originOf(ChatInput{User: "甲", Content: "在吗"}); !strings.Contains(got, "在吗") {
		t.Fatalf("观众弹幕的留痕应带原文，实际 %q", got)
	}
}
