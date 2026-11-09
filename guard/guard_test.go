package guard

import (
	"context"
	"testing"
)

func TestBlocksListedWord(t *testing.T) {
	g := New([]string{"加微信", "私聊"})
	if ok, why := g.Check(context.Background(), "想聊的加微信说"); ok {
		t.Fatalf("该拦没拦（reason=%q）", why)
	} else if why == "" {
		t.Fatal("拦下必须给理由，否则后台看不到是哪条红线挡的")
	}
}

func TestPassesCleanText(t *testing.T) {
	g := New([]string{"加微信"})
	if ok, _ := g.Check(context.Background(), "感谢老板的小心心"); !ok {
		t.Fatal("正常话术被误拦")
	}
}

// 空词表必须显式等于"不拦"。若它被读成"全都安全"，配置页漏填时会静默失去防线。
func TestEmptyListPasses(t *testing.T) {
	g := New(nil)
	if ok, _ := g.Check(context.Background(), "任何话"); !ok {
		t.Fatal("空词表应当放行")
	}
	if g.Size() != 0 {
		t.Fatal("Size 应能暴露词表条数")
	}
}

func TestTrimmedWordsAndBlankInput(t *testing.T) {
	g := New([]string{" 加微信 ", "", "  "})
	if g.Size() != 1 {
		t.Fatalf("空白词应当丢掉，实际 %d 条", g.Size())
	}
	if ok, _ := g.Check(context.Background(), "   "); !ok {
		t.Fatal("空文本没有红线可言，交给上层自己丢")
	}
}
