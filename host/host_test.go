package host

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// 这一组用例只钉一件事：**每一环挂掉时，编排器不许挂**。
// 直播场景里"少说一句"是可以接受的，"整个主播卡死"不可接受。

type fakeThinker struct {
	mu        sync.Mutex
	reply     string
	err       error
	failFirst int // 前 N 次抛错，之后恢复（验证"挂了一次还能继续干活"）
	calls     int
	lastSys   string // 最近一次收到的 system prompt（验证上下文有没有带）
}

func (f *fakeThinker) Think(_ context.Context, sys, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastSys = sys
	if f.err != nil {
		return "", f.err
	}
	if f.calls <= f.failFirst {
		return "", errors.New("网关 502")
	}
	return f.reply, nil
}

func (f *fakeThinker) setReply(s string) {
	f.mu.Lock()
	f.reply = s
	f.mu.Unlock()
}

func (f *fakeThinker) sysAt() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSys
}

func (f *fakeThinker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeSpeaker struct {
	mu   sync.Mutex
	said []string
	err  error
}

func (f *fakeSpeaker) Say(_ context.Context, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.said = append(f.said, text)
	return nil
}

func (f *fakeSpeaker) words() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.said))
	copy(out, f.said)
	return out
}

type fakeGuard struct {
	block  bool
	reason string
}

func (g *fakeGuard) Check(context.Context, string) (bool, string) {
	if g.block {
		return false, g.reason
	}
	return true, ""
}

// newTestHost 起一个可用的主播。只补"不填就不能跑"的项，**不覆盖用例显式传的规则**：
// 冷却/间隔/AtOnly 这些正是被测对象，盖掉就等于把用例测成了空转。
func newTestHost(t *testing.T, cfg Config, th Thinker, g Guard, sp Speaker) *Host {
	t.Helper()
	cfg.Enabled = true
	if cfg.RoomNo == "" {
		cfg.RoomNo = "test-room" // 暖场要求先绑房间
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 8
	}
	if cfg.MaxSayRunes <= 0 {
		cfg.MaxSayRunes = 60
	}
	if cfg.LLMTimeout <= 0 {
		cfg.LLMTimeout = 2
	}
	if th == nil {
		th = &fakeThinker{reply: "好"}
	}
	if sp == nil {
		sp = &fakeSpeaker{}
	}
	h := New(cfg, th, g, sp)
	h.Start()
	t.Cleanup(h.Stop)
	return h
}

// waitFor 等一个条件成立。编排器是异步的，只能用轮询观察结果。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等待超时：%s", what)
}

func TestDisabledHostDropsEverything(t *testing.T) {
	sp := &fakeSpeaker{}
	h := New(Config{Enabled: false}, &fakeThinker{reply: "不该出现"}, nil, sp)
	h.Start()
	defer h.Stop()

	if h.Handle(ChatInput{User: "甲", Content: "在吗"}) {
		t.Fatal("关闭状态不该入队")
	}
	time.Sleep(50 * time.Millisecond)
	if got := sp.words(); len(got) != 0 {
		t.Fatalf("关闭状态居然说了一句：%v", got)
	}
}

// 关了「只回 @」且抽样为 0/1 = 普通弹幕每条都回（受冷却/间隔约束）。
// 以前把 0 当“永不回”，表现就是“AtOnly 关了却一声不吭”。
func TestAtOnlyOffAnswersPlainChat(t *testing.T) {
	sp := &fakeSpeaker{}
	h := newTestHost(t, Config{AtOnly: false, SampleEvery: 0}, &fakeThinker{reply: "嗯"}, nil, sp)

	if !h.Handle(ChatInput{User: "甲", Content: "随便说一句"}) {
		t.Fatal("AtOnly 关了、抽样为 0 时普通弹幕应当入队")
	}
	waitFor(t, "说出一句", func() bool { return len(sp.words()) == 1 })
}

// 进场欢迎：说出来，但受自己的欢迎间隔约束（不是每个进场都念）。
func TestWelcomeSpeaksWithOwnGap(t *testing.T) {
	sp := &fakeSpeaker{}
	h := newTestHost(t, Config{WelcomeEnabled: true, WelcomeGapSec: 3600}, &fakeThinker{reply: "欢迎欢迎"}, nil, sp)

	if !h.Handle(ChatInput{User: "甲", Enter: true}) {
		t.Fatal("第一个进场应当欢迎")
	}
	waitFor(t, "说出欢迎", func() bool { return len(sp.words()) == 1 })
	if h.Handle(ChatInput{User: "乙", Enter: true}) {
		t.Fatal("欢迎间隔内第二个进场不该再说")
	}
}

// 暖场：开关关掉时 Idle 不入队；打开时它不看 AtOnly/关键词，直接说一句。
func TestIdleSpeaksOnlyWhenEnabled(t *testing.T) {
	off := newTestHost(t, Config{IdleTalkEnabled: false}, &fakeThinker{reply: "嗯"}, nil, &fakeSpeaker{})
	if off.Handle(ChatInput{Idle: true}) {
		t.Fatal("暖场关掉时 Idle 不该入队")
	}

	sp := &fakeSpeaker{}
	on := newTestHost(t, Config{IdleTalkEnabled: true}, &fakeThinker{reply: "大家晚上好"}, nil, sp)
	if !on.Handle(ChatInput{Idle: true}) {
		t.Fatal("暖场打开时 Idle 应入队")
	}
	waitFor(t, "说出暖场句", func() bool { return len(sp.words()) == 1 })
}

func TestAtOnlyIgnoresPlainChat(t *testing.T) {
	sp := &fakeSpeaker{}
	h := newTestHost(t, Config{AtOnly: true}, &fakeThinker{reply: "嗯"}, nil, sp)

	if h.Handle(ChatInput{User: "甲", Content: "随便说一句"}) {
		t.Fatal("非 @ 的普通弹幕不该入队")
	}
	if !h.Handle(ChatInput{User: "甲", Content: "@主播 你好", AtMe: true}) {
		t.Fatal("@ 的弹幕应当入队")
	}
	waitFor(t, "说出一句", func() bool { return len(sp.words()) == 1 })
}

func TestKeywordTriggersEvenWithoutAt(t *testing.T) {
	sp := &fakeSpeaker{}
	h := newTestHost(t, Config{AtOnly: true, Keywords: []string{"点歌", "唱歌"}}, &fakeThinker{reply: "这就唱"}, nil, sp)

	if !h.Handle(ChatInput{User: "甲", Content: "能点歌吗"}) {
		t.Fatal("命中关键词应当入队")
	}
	waitFor(t, "说出关键词回复", func() bool { return len(sp.words()) == 1 })
}

func TestGiftAlwaysAnswered(t *testing.T) {
	sp := &fakeSpeaker{}
	h := newTestHost(t, Config{AtOnly: true}, &fakeThinker{reply: "谢谢老板"}, nil, sp)

	if !h.Handle(ChatInput{User: "甲", Gift: "小心心", Count: 3}) {
		t.Fatal("礼物触发不该被 AtOnly 挡住")
	}
	waitFor(t, "谢礼", func() bool { return len(sp.words()) == 1 })
}

// 礼物也要讲节奏：连续刷礼物时不能每个都念一句，否则队列被打满、真弹幕反而被丢。
// （这条是现网跑出来的：开播十几条事件就丢了 5 条，全是礼物连续触发打满队列造成的）
func TestGiftRespectsGlobalGap(t *testing.T) {
	sp := &fakeSpeaker{}
	h := newTestHost(t, Config{GlobalGapSec: 60}, &fakeThinker{reply: "谢谢"}, nil, sp)

	if !h.Handle(ChatInput{User: "甲", Gift: "小心心", Count: 1}) {
		t.Fatal("第一个礼物应当入队")
	}
	waitFor(t, "第一句说完", func() bool { return len(sp.words()) == 1 })
	if h.Handle(ChatInput{User: "乙", Gift: "大啤酒", Count: 1}) {
		t.Fatal("全局间隔期内第二个礼物不该入队")
	}
}

// 上下文：第二句的 system prompt 里必须能看到第一轮说过什么。
// 这条钉的是“接得上话”—— 没它时同一波弹幕里 A 问完 B 接话，主播只会当作全新问题。
func TestHistoryCarriesPreviousTurn(t *testing.T) {
	th := &fakeThinker{reply: "第一句回复"}
	sp := &fakeSpeaker{}
	h := newTestHost(t, Config{HistoryTurns: 3}, th, nil, sp)

	h.Handle(ChatInput{User: "小明", UID: "1", Content: "@主播 今天玩什么", AtMe: true})
	waitFor(t, "第一句说完", func() bool { return len(sp.words()) == 1 })

	th.setReply("第二句回复")
	h.Handle(ChatInput{User: "小红", UID: "2", Content: "@主播 他刚才问的啥", AtMe: true})
	waitFor(t, "第二句说完", func() bool { return len(sp.words()) == 2 })

	sys := th.sysAt()
	if !strings.Contains(sys, "小明") || !strings.Contains(sys, "第一句回复") {
		t.Fatalf("第二轮没带上第一轮上下文：%s", sys)
	}
}

// 关掉记忆（HistoryTurns=0）时，prompt 里不该出现历史那段。
func TestHistoryDisabledWhenZero(t *testing.T) {
	th := &fakeThinker{reply: "嗯"}
	sp := &fakeSpeaker{}
	h := newTestHost(t, Config{HistoryTurns: 0}, th, nil, sp)

	h.Handle(ChatInput{User: "甲", UID: "1", Content: "@主播 一", AtMe: true})
	waitFor(t, "第一句说完", func() bool { return len(sp.words()) == 1 })
	h.Handle(ChatInput{User: "乙", UID: "2", Content: "@主播 二", AtMe: true})
	waitFor(t, "第二句说完", func() bool { return len(sp.words()) == 2 })

	if strings.Contains(th.sysAt(), "刚才直播间里发生过") {
		t.Fatalf("关掉记忆却仍然带了上下文：%s", th.sysAt())
	}
}

func TestCooldownBlocksSameUser(t *testing.T) {
	sp := &fakeSpeaker{}
	h := newTestHost(t, Config{CooldownSec: 60}, &fakeThinker{reply: "嗯"}, nil, sp)

	if !h.Handle(ChatInput{User: "甲", UID: "1", Content: "@主播 在吗", AtMe: true}) {
		t.Fatal("第一次应当入队")
	}
	waitFor(t, "第一句说完", func() bool { return len(sp.words()) == 1 })
	if h.Handle(ChatInput{User: "甲", UID: "1", Content: "@主播 还在吗", AtMe: true}) {
		t.Fatal("冷却期内同一个人不该再入队")
	}
	if !h.Handle(ChatInput{User: "乙", UID: "2", Content: "@主播 我呢", AtMe: true}) {
		t.Fatal("换了个人应当能入队")
	}
}

func TestQueueFullDropsInsteadOfBlocking(t *testing.T) {
	// 给一个永远播不完的 speaker：队列必然被塞满。
	block := make(chan struct{})
	sp := &blockingSpeaker{release: block}
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.AtOnly = false
	cfg.QueueSize = 1
	cfg.CooldownSec = 0
	cfg.GlobalGapSec = 0
	h := New(cfg, &fakeThinker{reply: "嗯"}, nil, sp)
	h.Start()

	if !h.Handle(ChatInput{User: "甲", Gift: "小心心", Count: 1}) {
		t.Fatal("第一条应当入队")
	}
	waitFor(t, "进入播放", func() bool { return sp.started() })
	if !h.Handle(ChatInput{User: "乙", Gift: "小心心", Count: 1}) {
		t.Fatal("第二条应当占满队列")
	}
	// 第三条会被丢弃，但**必须立即返回**——这条是防止回归到"阻塞弹幕协程"的关键。
	start := time.Now()
	if h.Handle(ChatInput{User: "丙", Gift: "小心心", Count: 1}) {
		t.Fatal("队列满时不该入队")
	}
	if cost := time.Since(start); cost > 100*time.Millisecond {
		t.Fatalf("队列满时 Handle 用时 %v，说明它在阻塞调用方", cost)
	}
	close(block)
	h.Stop()
}

func TestThinkFailureDoesNotStopHost(t *testing.T) {
	th := &fakeThinker{reply: "恢复了", failFirst: 1}
	sp := &fakeSpeaker{}
	h := newTestHost(t, Config{}, th, nil, sp)

	h.Handle(ChatInput{User: "甲", UID: "1", Content: "@主播 一", AtMe: true})
	waitFor(t, "第一次失败被记下", func() bool { return th.callCount() == 1 })
	if got := sp.words(); len(got) != 0 {
		t.Fatalf("生成失败却说了话：%v", got)
	}
	// 同一个主播必须能继续工作：失败只影响出错的那一句。
	h.Handle(ChatInput{User: "乙", UID: "2", Content: "@主播 二", AtMe: true})
	waitFor(t, "失败后仍能说话", func() bool { return len(sp.words()) == 1 })
}

func TestGuardBlocksUtterance(t *testing.T) {
	sp := &fakeSpeaker{}
	h := newTestHost(t, Config{}, &fakeThinker{reply: "加我微信"}, &fakeGuard{block: true, reason: "导流"}, sp)

	h.Handle(ChatInput{User: "甲", UID: "1", Content: "@主播 私聊", AtMe: true})
	waitFor(t, "被拦下计数", func() bool { return h.Stats()["blocked"].(int64) == 1 })
	if got := sp.words(); len(got) != 0 {
		t.Fatalf("合规拦下的句子不该播出去：%v", got)
	}
}

func TestLongReplyIsTruncated(t *testing.T) {
	long := strings.Repeat("啊", 200)
	sp := &fakeSpeaker{}
	h := newTestHost(t, Config{MaxSayRunes: 10}, &fakeThinker{reply: long}, nil, sp)

	h.Handle(ChatInput{User: "甲", UID: "1", Content: "@主播 说点啥", AtMe: true})
	waitFor(t, "说出一句", func() bool { return len(sp.words()) == 1 })
	if n := len([]rune(sp.words()[0])); n != 10 {
		t.Fatalf("应当截到 10 个字，实际 %d", n)
	}
}

func TestSpeakFailureIsCountedNotFatal(t *testing.T) {
	sp := &fakeSpeaker{err: errors.New("ffplay 不存在")}
	h := newTestHost(t, Config{}, &fakeThinker{reply: "嗯"}, nil, sp)

	h.Handle(ChatInput{User: "甲", UID: "1", Content: "@主播 一", AtMe: true})
	waitFor(t, "失败计数", func() bool { return h.Stats()["sayFailed"].(int64) == 1 })
	if h.Stats()["said"].(int64) != 0 {
		t.Fatal("失败不该计入成功")
	}
}

// blockingSpeaker 模拟"一句要播很久"，用来验证队列满的丢弃行为。
type blockingSpeaker struct {
	release  chan struct{}
	mu       sync.Mutex
	isStartd bool
}

func (b *blockingSpeaker) Say(ctx context.Context, _ string) error {
	b.mu.Lock()
	b.isStartd = true
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return nil
}

func (b *blockingSpeaker) started() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.isStartd
}
