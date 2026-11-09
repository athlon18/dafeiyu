// Package guard 开口前的本地词表闸。
//
// 只有一道：口播和（第四刀的）发评论共用它，不给任何一条路径开后门。
// 定位是"明知有雷还念"的最后一层，不是合规保证——真正的保证来自话术本身不越界。
// 主仓的 modules/ai/redline 是多方共用的重包，本仓不拖进来；接口形状保持一致，
// 将来要换成语义判据，只动这一个包。
package guard

import (
	"context"
	"strings"
)

type Guard struct {
	words []string
}

func New(words []string) *Guard {
	out := make([]string, 0, len(words))
	for _, w := range words {
		if w = strings.TrimSpace(w); w != "" {
			out = append(out, w)
		}
	}
	return &Guard{words: out}
}

// Size 词表条数。配置页要显示"当前拦几条"，漏填时不能让人以为有防线。
func (g *Guard) Size() int { return len(g.words) }

func (g *Guard) Check(_ context.Context, text string) (bool, string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return true, ""
	}
	for _, w := range g.words {
		if strings.Contains(text, w) {
			return false, "命中拦截词：" + w
		}
	}
	return true, ""
}
