package tts

import "testing"

// 切句这条钉的是“切得要合理”：句子太短别切（接缝比话还多），
// 太长要按标点切（不能把词组劈开）。

func joinLen(segs []string) int {
	n := 0
	for _, s := range segs {
		n += len([]rune(s))
	}
	return n
}

func TestSplitForStream(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		opt      SpeakOptions
		wantSegs int
	}{
		{"短句不切", "谢谢老板", SpeakOptions{}, 1},
		{"刚好到上限不切", "一二三四五六七八九十一二三四五六七八", SpeakOptions{MaxSegmentRunes: 18}, 1},
		{"长句按标点切", "大家好我是这条鱼，今天我们来玩植物大战僵尸，先种一排向日葵。", SpeakOptions{MaxSegmentRunes: 18, MaxSegments: 4}, 3},
		{"无标点硬切", "一二三四五六七八九十一二三四五六七八九十一二三四五六七八", SpeakOptions{MaxSegmentRunes: 18, MaxSegments: 4}, 2},
		{"段数封顶", "第一段很长很长很长很长，第二段很长很长很长很长，第三段很长很长很长很长，第四段很长很长很长很长，第五段很长很长很长很长。", SpeakOptions{MaxSegmentRunes: 10, MaxSegments: 3}, 3},
	}
	for _, c := range cases {
		got := SplitForStream(c.text, c.opt)
		if len(got) != c.wantSegs {
			t.Errorf("%s：切出 %d 段，期望 %d 段 → %q", c.name, len(got), c.wantSegs, got)
		}
		// 无论怎么切，字不能丢（丢了就是少说一截，直播里最难发现）。
		if n, want := joinLen(got), len([]rune(c.text)); n != want {
			t.Errorf("%s：切完 %d 字，原文 %d 字，字被吞了 → %q", c.name, n, want, got)
		}
	}
}

func TestSplitForStreamEmpty(t *testing.T) {
	if got := SplitForStream("   ", SpeakOptions{}); len(got) != 0 {
		t.Fatalf("空白应切出 0 段，实际 %q", got)
	}
}
