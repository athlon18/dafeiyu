package source

import (
	"testing"
	"time"

	pb "github.com/athlon18/douyin-protocol/webcast"
	live "github.com/athlon18/douyin-live-client/live"

	"github.com/athlon18/dafeiyu/rules"
)

func collect(t *testing.T, atNames []string) (*Source, *[]rules.Event) {
	t.Helper()
	got := []rules.Event{}
	s := New(Config{RoomNo: "7", AtNames: atNames, Sink: func(ev rules.Event) { got = append(got, ev) }})
	return s, &got
}

func user(nick string) *pb.User { return &pb.User{NickName: nick} }

func TestGiftMapsTypedFields(t *testing.T) {
	s, got := collect(t, nil)
	s.Handler().OnGift(live.GiftEvent{User: user("老板甲"), GiftName: "嘉年华", Count: 3, UnitPrice: 3000})
	if len(*got) != 1 {
		t.Fatalf("事件数不对: %d", len(*got))
	}
	ev := (*got)[0]
	if ev.Kind != rules.KindGift || ev.GiftName != "嘉年华" || ev.GiftCount != 3 || ev.User != "老板甲" {
		t.Fatalf("typed 字段没搬全: %+v", ev)
	}
	if ev.GiftUnitPrice != 3000 {
		t.Fatalf("单价没带上（分档口径要用）: %+v", ev)
	}
}

func TestChatDetectsAtMe(t *testing.T) {
	s, got := collect(t, []string{"大肥鱼"})
	s.Handler().OnChat(live.ChatEvent{User: user("观众乙"), Content: "@大肥鱼 今天玩啥"})
	if len(*got) != 1 {
		t.Fatalf("事件数不对: %d", len(*got))
	}
	if !(*got)[0].AtMe || (*got)[0].Content != "@大肥鱼 今天玩啥" {
		t.Fatalf("@ 没识别: %+v", (*got)[0])
	}
}

func TestAtMeFalseWithoutAlias(t *testing.T) {
	s, got := collect(t, []string{"大肥鱼"})
	s.Handler().OnChat(live.ChatEvent{User: user("a"), Content: "今天玩啥"})
	if len(*got) != 1 || (*got)[0].AtMe {
		t.Fatalf("没 @ 却判成 @我: %+v", *got)
	}
}

func TestMemberBecomesEnterOnlyForJoinAction(t *testing.T) {
	s, got := collect(t, nil)
	h := s.Handler()
	h.OnMember(live.MemberEvent{User: user("新人"), Action: 1})
	h.OnMember(live.MemberEvent{User: user("离开的人"), Action: 2})
	if len(*got) != 1 {
		t.Fatalf("离开事件不该当进场: %+v", *got)
	}
	if (*got)[0].Kind != rules.KindEnter {
		t.Fatalf("进场种类不对: %+v", (*got)[0])
	}
}

// 第一刀明确不接点赞/关注/分享：ChatInput 上没有诚实的表达形态，接了就是造假话。
func TestLikeAndSocialIgnored(t *testing.T) {
	s, got := collect(t, nil)
	h := s.Handler()
	h.OnLike(live.LikeEvent{User: user("a"), Delta: 5})
	h.OnSocial(live.SocialEvent{User: user("a"), Action: 1})
	h.OnEmojiChat(live.EmojiChatEvent{User: user("a"), Content: "[玫瑰]"})
	h.OnFansclub(live.FansclubEvent{User: user("a"), Content: "加入了粉丝团"})
	if len(*got) != 0 {
		t.Fatalf("第一刀不该转发这些: %+v", *got)
	}
}

func TestControlOfflineOnStatus3(t *testing.T) {
	s, got := collect(t, nil)
	s.Handler().OnControl(live.ControlEvent{Status: 3})
	if len(*got) != 1 || (*got)[0].Kind != rules.KindOffline {
		t.Fatalf("下播没翻成 offline: %+v", *got)
	}
}

func TestControlOtherStatusIgnored(t *testing.T) {
	s, got := collect(t, nil)
	s.Handler().OnControl(live.ControlEvent{Status: 4})
	if len(*got) != 0 {
		t.Fatalf("非下播状态被当成收场: %+v", *got)
	}
}

func TestEveryFrameAdvancesLastFrame(t *testing.T) {
	s, _ := collect(t, nil)
	before := s.LastFrame()
	time.Sleep(2 * time.Millisecond)
	s.Handler().OnChat(live.ChatEvent{User: user("a"), Content: "在吗"})
	if s.LastFrame().Equal(before) {
		t.Fatal("收到帧却没推进 lastFrame，冷场判据会永远成立")
	}
}

func TestNilUserDoesNotPanic(t *testing.T) {
	s, got := collect(t, nil)
	s.Handler().OnGift(live.GiftEvent{User: nil, GiftName: "嘉年华", Count: 1})
	if len(*got) != 1 || (*got)[0].User != "" {
		t.Fatalf("空 User 应安全降级成空昵称: %+v", *got)
	}
}
