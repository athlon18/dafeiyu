// Package rules 直播间事件到「要不要说、说哪句」的匹配表。
//
// 时间一律从参数进来，包内不读钟：冷却、轮播、兜底这三件事不在这里就永远没法单测。
// 产出的 ChatInput 带 Speak/Line（配好的句子），因此**没接 LLM 也能开口**——
// 这是"模板兜底 + 可选 LLM"两条腿里的模板那条。
package rules

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/athlon18/dafeiyu/host"
)

// Kind 事件种类。只列第一刀支持的；点赞/关注/分享在 ChatInput 上没有诚实的表达形态，
// 硬套会让叠层显示成假观众发言，推到第四刀连同语义一起补。
type Kind string

const (
	KindGift    Kind = "gift"
	KindEnter   Kind = "enter"
	KindChat    Kind = "chat"
	KindOffline Kind = "offline"
	KindGuide   Kind = "guide"
	KindIdle    Kind = "idle"
)

type Event struct {
	Kind          Kind
	User          string
	UID           string
	Content       string
	AtMe          bool
	GiftName      string
	GiftCount     int
	GiftUnitPrice uint64
}

// Line 一条可配的话术行。Stage=0 表示不看连击段数。
type Line struct {
	Match string
	Stage uint8
	Lines []string
}

type Config struct {
	RoomNo string

	// 各段自带的单人冷却。话术路径在编排器里不过 LLM 那条冷却闸，
	// 所以这层必须自己兜住；默认值对齐 host.DefaultConfig().CooldownSec。
	GiftCooldown    time.Duration
	EnterCooldown   time.Duration
	ChatCooldown    time.Duration
	OfflineCooldown time.Duration

	GiftLines    []Line
	EnterLine    string
	ChatLine     string // 空=弹幕交给 LLM 组句；非空=固定模板，不调 LLM
	Guide        []string
	GuideEvery   time.Duration
	IdleLines    []string
	IdleAfter    time.Duration
	OfflineLines []string
}

func (c Config) withDefaults() Config {
	if c.GiftCooldown <= 0 {
		c.GiftCooldown = 30 * time.Second
	}
	if c.EnterCooldown <= 0 {
		c.EnterCooldown = 30 * time.Second
	}
	if c.ChatCooldown <= 0 {
		c.ChatCooldown = 30 * time.Second
	}
	if c.OfflineCooldown <= 0 {
		c.OfflineCooldown = 30 * time.Second
	}
	return c
}

type Rules struct {
	cfg Config

	mu           sync.Mutex
	giftIdx      map[string]int
	offlineIdx   int
	guideRot     int
	idleIdx      int
	lastByClass  map[Kind]map[string]time.Time
	lastGuide    time.Time
	lastIdleEmit time.Time
}

func New(cfg Config) (*Rules, error) {
	if len(cfg.Guide) > 0 && cfg.GuideEvery <= 0 {
		return nil, fmt.Errorf("配了引导话术就必须给 GuideEvery（轮播周期）")
	}
	if len(cfg.IdleLines) > 0 && cfg.IdleAfter <= 0 {
		return nil, fmt.Errorf("配了兜底话术就必须给 IdleAfter（安静多久算冷场）")
	}
	return &Rules{
		cfg:         cfg.withDefaults(),
		giftIdx:     map[string]int{},
		lastByClass: map[Kind]map[string]time.Time{},
	}, nil
}

// Match 一帧 → 一次要喂给编排器的输入。false = 这帧不值得开口。
func (r *Rules) Match(ev Event, now time.Time) (host.ChatInput, bool) {
	switch ev.Kind {
	case KindGift:
		return r.gift(ev, now)
	case KindEnter:
		if r.cfg.EnterLine == "" {
			return host.ChatInput{}, false
		}
		if !r.allow(KindEnter, identity(ev), now, r.cfg.EnterCooldown) {
			return host.ChatInput{}, false
		}
		return host.ChatInput{RoomNo: r.cfg.RoomNo, User: ev.User, UID: ev.UID,
			Enter: true, Speak: render(r.cfg.EnterLine, ev, 0)}, true
	case KindChat:
		return r.chat(ev, now)
	case KindOffline:
		if len(r.cfg.OfflineLines) == 0 {
			return host.ChatInput{}, false
		}
		if !r.allow(KindOffline, "room", now, r.cfg.OfflineCooldown) {
			return host.ChatInput{}, false
		}
		r.mu.Lock()
		line := r.cfg.OfflineLines[r.offlineIdx%len(r.cfg.OfflineLines)]
		r.offlineIdx++
		r.mu.Unlock()
		return host.ChatInput{RoomNo: r.cfg.RoomNo, Line: line}, true
	}
	return host.ChatInput{}, false
}

func (r *Rules) gift(ev Event, now time.Time) (host.ChatInput, bool) {
	stage := StageOf(uint64(maxInt(ev.GiftCount, 1)))
	for i, l := range r.cfg.GiftLines {
		if l.Match == "" || !contains(ev.GiftName, l.Match) {
			continue
		}
		if l.Stage > 0 && stage < l.Stage {
			continue
		}
		if len(l.Lines) == 0 {
			continue
		}
		if !r.allow(KindGift, identity(ev), now, r.cfg.GiftCooldown) {
			return host.ChatInput{}, false
		}
		r.mu.Lock()
		key := strconv.Itoa(i)
		line := l.Lines[r.giftIdx[key]%len(l.Lines)]
		r.giftIdx[key]++
		r.mu.Unlock()
		return host.ChatInput{RoomNo: r.cfg.RoomNo, User: ev.User, UID: ev.UID,
			Gift: ev.GiftName, Count: ev.GiftCount, Speak: render(line, ev, stage)}, true
	}
	return host.ChatInput{}, false
}

func (r *Rules) chat(ev Event, now time.Time) (host.ChatInput, bool) {
	if trim(ev.Content) == "" {
		return host.ChatInput{}, false
	}
	in := host.ChatInput{RoomNo: r.cfg.RoomNo, User: ev.User, UID: ev.UID, Content: ev.Content, AtMe: ev.AtMe}
	if r.cfg.ChatLine != "" {
		if !r.allow(KindChat, identity(ev), now, r.cfg.ChatCooldown) {
			return host.ChatInput{}, false
		}
		in.Speak = render(r.cfg.ChatLine, ev, 0)
	}
	return in, true
}

// NextGuide 到点轮播一条引导。冷场时它和 NextIdle 是唯一稳定的出声来源。
func (r *Rules) NextGuide(now time.Time) (host.ChatInput, bool) {
	if len(r.cfg.Guide) == 0 || r.cfg.GuideEvery <= 0 {
		return host.ChatInput{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.lastGuide.IsZero() && now.Sub(r.lastGuide) < r.cfg.GuideEvery {
		return host.ChatInput{}, false
	}
	r.lastGuide = now
	line := r.cfg.Guide[r.guideRot%len(r.cfg.Guide)]
	r.guideRot++
	return host.ChatInput{RoomNo: r.cfg.RoomNo, Line: line}, true
}

// NextIdle 连续安静超过 IdleAfter 才兜底一句；从没收到过帧时不兜底（那多半是没连上）。
func (r *Rules) NextIdle(now time.Time, lastFrame time.Time) (host.ChatInput, bool) {
	if len(r.cfg.IdleLines) == 0 || r.cfg.IdleAfter <= 0 || lastFrame.IsZero() {
		return host.ChatInput{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Sub(lastFrame) < r.cfg.IdleAfter {
		return host.ChatInput{}, false
	}
	if !r.lastIdleEmit.IsZero() && now.Sub(r.lastIdleEmit) < r.cfg.IdleAfter {
		return host.ChatInput{}, false
	}
	r.lastIdleEmit = now
	line := r.cfg.IdleLines[r.idleIdx%len(r.cfg.IdleLines)]
	r.idleIdx++
	return host.ChatInput{RoomNo: r.cfg.RoomNo, Idle: true, Line: line}, true
}

// allow 按"事件类别 + 人"记冷却。礼物刷屏和进场人潮互不干扰，同类别内各人独立。
func (r *Rules) allow(k Kind, who string, now time.Time, gap time.Duration) bool {
	if who == "" || gap <= 0 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	byUser, ok := r.lastByClass[k]
	if !ok {
		byUser = map[string]time.Time{}
		r.lastByClass[k] = byUser
	}
	if last, seen := byUser[who]; seen && now.Sub(last) < gap {
		return false
	}
	byUser[who] = now
	return true
}

func render(tpl string, ev Event, stage uint8) string {
	n := maxInt(ev.GiftCount, 1)
	return strings.NewReplacer(
		"{user}", ev.User,
		"{gift}", ev.GiftName,
		"{count}", strconv.Itoa(n),
		"{stage}", strconv.Itoa(int(stage)),
		"{content}", ev.Content,
	).Replace(tpl)
}

func contains(hay, needle string) bool {
	return strings.Contains(hay, needle)
}

func trim(s string) string { return strings.TrimSpace(s) }

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func identity(ev Event) string {
	if ev.UID != "" {
		return ev.UID
	}
	return ev.User
}
