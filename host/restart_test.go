package host

import (
	"testing"
	"time"
)

// Start 可能被再次调用（后台页"关麦后重开"）。老协程退出时关掉的是**它自己那一代**的
// done，不是字段上后来的那一个——否则会 panic: close of nil channel，
// 或者把新会话的等待者提前放走。
func TestRestartDoesNotDoubleCloseDone(t *testing.T) {
	h := New(Config{GlobalGapSec: 0, Enabled: true}, &fakeThinker{reply: "好"}, &fakeGuard{}, &fakeSpeaker{})
	h.Start()
	h.Stop()
	h.Start()
	h.Stop() // 旧实现在这一步之后由残留协程触发 panic

	// 残留协程该在上面的 Stop 里就走完了
	time.Sleep(100 * time.Millisecond)

	// 重启后必须还能干活
	h2 := New(Config{GlobalGapSec: 0, Enabled: true, RoomNo: "r", QueueSize: 4, MaxSayRunes: 60},
		&fakeThinker{reply: "好"}, &fakeGuard{}, &fakeSpeaker{})
	h2.Start()
	defer h2.Stop()
	h2.Handle(ChatInput{Line: "重启后仍要说得出话"})
	waitSaid(t, h2, "重启后仍要说得出话")
}
