package store

import (
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "dafeiyu.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// 保存页上只有三块（连哪儿/从哪儿出声/她说什么），存进去必须原样读得回来——
// 读不回来就是"点了保存但下次进来又变回默认"，那是比没按钮更糟的骗人。
func TestRoundTripKeepsEveryField(t *testing.T) {
	s := open(t)
	want := Settings{
		Conn:  Conn{RoomID: "731905913385", Cookie: "ttwid=abc"},
		Voice: Voice{Engine: "openai", BaseURL: "http://127.0.0.1:9880/v1", Model: "tts-1", Timbre: "zh_CN-huayan-medium", Speed: 1.2, Device: "CABLE Input"},
		Rules: []Rule{
			{Moment: "有人送嘉年华", Line: "感谢 {user} 的嘉年华", Enabled: true},
			{Moment: "安静满 1 分钟", Line: "刚进来的先坐", Enabled: false},
		},
	}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Conn != want.Conn {
		t.Fatalf("连接没存住: %+v", got.Conn)
	}
	if got.Voice != want.Voice {
		t.Fatalf("声音没存住: %+v", got.Voice)
	}
	if len(got.Rules) != 2 {
		t.Fatalf("规则条数不对: %d", len(got.Rules))
	}
	if got.Rules[0].Moment != "有人送嘉年华" || !got.Rules[0].Enabled {
		t.Fatalf("第一条不对: %+v", got.Rules[0])
	}
	if got.Rules[1].Enabled {
		t.Fatalf("关掉的开关被存成开: %+v", got.Rules[1])
	}
}

// 顺序＝优先级（页面上明写了），重排后必须按新顺序读回。
func TestRuleOrderIsPriority(t *testing.T) {
	s := open(t)
	if err := s.Save(Settings{Rules: []Rule{
		{Moment: "A", Line: "a", Enabled: true},
		{Moment: "B", Line: "b", Enabled: true},
		{Moment: "C", Line: "c", Enabled: true},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(Settings{Rules: []Rule{
		{Moment: "C", Line: "c", Enabled: true},
		{Moment: "A", Line: "a", Enabled: true},
		{Moment: "B", Line: "b", Enabled: true},
	}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range got.Rules {
		order = append(order, r.Moment)
	}
	if len(order) != 3 || order[0] != "C" || order[1] != "A" || order[2] != "B" {
		t.Fatalf("重排没存住: %v", order)
	}
}

// 空规则表是合法状态（用户可能全删了），不许报错也不许凭空造一条。
func TestEmptyRulesStayEmpty(t *testing.T) {
	s := open(t)
	if err := s.Save(Settings{Rules: nil}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rules) != 0 {
		t.Fatalf("空表被造出规则: %+v", got.Rules)
	}
}

// 闭麦必须存住：重启把主播的麦克风状态悄悄改回"开"，比不响更糟。
func TestMutedSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "dafeiyu.db")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMuted(true); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Muted {
		t.Fatal("闭麦状态没存住，重启会自动开麦")
	}
}

// 存不下时必须报错，不能静默"保存成功"。
func TestSaveRejectsBlankLine(t *testing.T) {
	s := open(t)
	err := s.Save(Settings{Rules: []Rule{{Moment: "有人送嘉年华", Line: "   ", Enabled: true}}})
	if err == nil {
		t.Fatal("空话术被存下了：那条规则会让她在该说话时说出空气")
	}
}
