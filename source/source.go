// Package source 把 douyin-live-client 的 typed 事件翻译成 rules 的事件。
//
// 只读事件上已经铺平的结构化字段：e.Frame / e.Raw 一律不碰——回喂原始 protobuf 是
// 主仓过渡期的垫片写法，本仓既然买了 typed 回调，就不该再自己解一遍。
package source

import (
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/athlon18/douyin-protocol/webcast"
	live "github.com/athlon18/douyin-live-client/live"

	"github.com/athlon18/dafeiyu/rules"
)

type Config struct {
	RoomNo  string
	AtNames []string // 主播昵称别名，判 @我 用
	Sink    func(rules.Event)
}

type Source struct {
	cfg Config

	mu        sync.Mutex
	lastFrame time.Time
}

func New(cfg Config) *Source { return &Source{cfg: cfg} }

// Handler 返回给 SDK 的回调集合。刻意借用 *Source 本身：lastFrame 必须和读帧共享一份。
func (s *Source) Handler() live.Handler { return &handler{s: s} }

// NoteFrame 记录"最近一次真的收到帧"。冷场兜底只看这个，不看主播自己说过话。
func (s *Source) NoteFrame(t time.Time) {
	s.mu.Lock()
	s.lastFrame = t
	s.mu.Unlock()
}

func (s *Source) LastFrame() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastFrame
}

func (s *Source) emit(ev rules.Event) {
	s.NoteFrame(time.Now())
	if s.cfg.Sink == nil {
		return
	}
	s.cfg.Sink(ev)
}

// handler 是 Source 的 SDK 回调视图。刻意不内嵌：内嵌会让 live.Handler 的方法集
// 泄漏到 *Source 上，将来谁把 Source 自己当 Handler 传进去就静默少一层收口。
type handler struct{ s *Source }

func (h *handler) atMe(text string) bool {
	for _, n := range h.s.cfg.AtNames {
		if n = strings.TrimSpace(n); n != "" && strings.Contains(text, "@"+n) {
			return true
		}
	}
	return false
}

func (h *handler) OnGift(e live.GiftEvent) {
	h.s.emit(rules.Event{
		Kind:          rules.KindGift,
		User:          e.User.GetNickName(),
		UID:           uidOf(e.User),
		GiftName:      e.GiftName,
		GiftCount:     int(e.Count),
		GiftUnitPrice: e.UnitPrice,
	})
}

func (h *handler) OnChat(e live.ChatEvent) {
	h.s.emit(rules.Event{
		Kind:    rules.KindChat,
		User:    e.User.GetNickName(),
		UID:     uidOf(e.User),
		Content: e.Content,
		AtMe:    h.atMe(e.Content),
	})
}

func (h *handler) OnMember(e live.MemberEvent) {
	// action 的语义由消费方判（SDK 刻意不解释）。只把"进入"当进场，取不到的值一律不当真。
	if e.Action != 1 {
		return
	}
	h.s.emit(rules.Event{Kind: rules.KindEnter, User: e.User.GetNickName(), UID: uidOf(e.User)})
}

func (h *handler) OnControl(e live.ControlEvent) {
	if e.Status != 3 { // 3=下播（SDK README 口径）
		return
	}
	h.s.emit(rules.Event{Kind: rules.KindOffline})
}

// 第一刀故意留空的回调：点赞 / 关注分享 / 表情弹幕 / 粉丝团 / 在线人数。
// 它们在当前编排契约上没有诚实的表达形态，硬套会让叠层显示成假观众发言。
func (h *handler) OnEmojiChat(live.EmojiChatEvent)      {}
func (h *handler) OnFansclub(live.FansclubEvent)        {}
func (h *handler) OnLike(live.LikeEvent)                {}
func (h *handler) OnSocial(live.SocialEvent)            {}
func (h *handler) OnRoomUserSeq(live.RoomUserSeqEvent)  {}

// uidOf 取稳定身份。昵称会改，UID 不会；一个都取不到就交空串，
// 让冷却自然退化到按昵称——不许去扒 unknownFields 猜字段号。
func uidOf(u *pb.User) string {
	if u == nil {
		return ""
	}
	if id := u.GetId(); id != 0 {
		return strconv.FormatUint(id, 10)
	}
	if sid := u.GetShortId(); sid != 0 {
		return strconv.FormatUint(sid, 10)
	}
	return ""
}
