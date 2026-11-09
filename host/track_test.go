package host

import "testing"

// 版权闸这两个用例钉的是“别把它做成花架子”：
// 关键词要能拆干净，匹配要能覆盖“歌名里带歌手”这种常见写法。

func TestBannedKeywordList(t *testing.T) {
	got := BannedKeywordList("界元甲, 以界之名；夜界\n暗界\t星界|听大嘴的话")
	want := []string{"界元甲", "以界之名", "夜界", "暗界", "星界", "听大嘴的话"}
	if len(got) != len(want) {
		t.Fatalf("拆出 %d 个，期望 %d 个：%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个是 %q，期望 %q（全量 %v）", i, got[i], want[i], got)
		}
	}
	if n := len(BannedKeywordList("   ,,  \n ")); n != 0 {
		t.Fatalf("空内容应拆出 0 个，实际 %d", n)
	}
}

func TestIsTrackBanned(t *testing.T) {
	banned := BannedKeywordList("晴天,周杰,界元甲")
	cases := []struct {
		title string
		want  bool
	}{
		{"周杰伦 - 晴天", true},      // 歌名 + 歌手拼起来，两个关键词都能中
		{"晴天 (Live)", true},       // 只写歌名也能中
		{"界元甲 - 某某", true},       // 全清单命中的那首
		{"夜曲", false},             // 不在清单里
		{"", false},                // 空标题不误报
		{"  ", false},              // 空白不误报
	}
	for _, c := range cases {
		if got := IsTrackBanned(c.title, banned); got != c.want {
			t.Errorf("IsTrackBanned(%q) = %v，期望 %v", c.title, got, c.want)
		}
	}
	// 清单为空时一律放行（关掉闸不制造假拦截）
	if IsTrackBanned("周杰伦 - 晴天", nil) {
		t.Error("清单为空时不该拦")
	}
}
