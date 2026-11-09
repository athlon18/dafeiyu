package stage

import (
	"testing"
	"time"

	"github.com/athlon18/dafeiyu/tts"
)

func TestBeginSegmentEnd(t *testing.T) {
	s := New("大肥鱼", "模型：Mao绒绒家园", map[string]float64{"Paramcol_3": 1})
	if got := s.Snapshot(); got.Speaking {
		t.Fatal("还没开口就 speaking=true，叠层会一直张着嘴")
	}
	s.Begin("感谢老板的嘉年华")
	s.Segment(tts.Result{Audio: wavPCM16(22050, tone(22050, 300, 0.5)), MIME: "audio/wav"})
	got := s.Snapshot()
	if !got.Speaking {
		t.Fatal("登记了包络却没说在说话")
	}
	if got.Caption != "感谢老板的嘉年华" {
		t.Fatalf("字幕没跟上: %q", got.Caption)
	}
	if got.SpeakAt == 0 || time.Now().UnixMilli()-got.SpeakAt > 1000 {
		t.Fatalf("起点时刻没设: %d", got.SpeakAt)
	}
	if len(got.Env) == 0 {
		t.Fatal("包络是空的，嘴只会匀速动")
	}
	s.End()
	after := s.Snapshot()
	if after.Speaking || after.SpeakAt != 0 || len(after.Env) != 0 {
		t.Fatalf("说完必须清干净，否则嘴停在半开: %+v", after)
	}
	// 字幕要留在说完之后一小会儿，观众才来得及读——但不能再算"在说话"。
	if after.Caption != "感谢老板的嘉年华" {
		t.Fatalf("字幕被一起清掉了: %q", after.Caption)
	}
}

// 多段流水线：第一段定起点，后面的包络接在后面，不能各定各的时刻。
func TestSegmentsAppendInOrder(t *testing.T) {
	s := New("大肥鱼", "", nil)
	s.Begin("一二三")
	first := s.Segment(tts.Result{Audio: wavPCM16(22050, tone(22050, 200, 0.5)), MIME: "audio/wav"})
	atAfterFirst := s.Snapshot().SpeakAt
	s.Segment(tts.Result{Audio: wavPCM16(22050, tone(22050, 200, 0.5)), MIME: "audio/wav"})
	got := s.Snapshot()
	if got.SpeakAt != atAfterFirst {
		t.Fatal("第二段改了起点时刻，叠层会对不上嘴")
	}
	if len(got.Env) < first*2 {
		t.Fatalf("第二段没接上去: %d < %d", len(got.Env), first*2)
	}
}

// 抢话/被打断时 End 必须把状态清掉，且重复调用不许 panic。
func TestEndIsIdempotent(t *testing.T) {
	s := New("大肥鱼", "", nil)
	s.End()
	s.Begin("x")
	s.End()
	s.End()
	if s.Snapshot().Speaking {
		t.Fatal("End 之后还在说话")
	}
}

func TestSnapshotCarriesIdentity(t *testing.T) {
	s := New("大肥鱼", "模型：Mao绒绒家园", map[string]float64{"Paramcol_3": 1})
	got := s.Snapshot()
	if got.Model != "大肥鱼" || got.Credit != "模型：Mao绒绒家园" {
		t.Fatalf("形象身份没带出来: %+v", got)
	}
}
