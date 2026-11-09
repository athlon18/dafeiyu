package host

// 点歌/放歌的版权闸。
//
// 为什么需要它：抖音对直播间播放音乐的判定是**音频指纹**，不是歌名——
// 哪怕你把文件改名、降调、只放片段，只要旋律匹配到未授权曲目就会被拦
// （轻则静音该段，重则限流/警告）。
//
// 所以这个闸的定位要说清楚：它只能防住「明知有雷还去放」这一种，
// 防不住「这首歌其实没授权」。真正的保险只有两条：
//   1. 只用抖音官方曲库里标「已授权」的音乐；
//   2. 或者用自己买了商用授权的素材（含 CC0 音乐）。
//
// 命中时调用方的正确动作是**跳过并切下一首**，不是弹窗让人手点——
// 直播里没有等人点确认的时间。

import "strings"

// BannedKeywordList 把配置里的文本拆成关键词（认中英文逗号、分号、换行）。
func BannedKeywordList(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		switch r {
		case ',', '，', ';', '；', '\n', '\r', '\t', '|':
			return true
		}
		return false
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// IsTrackBanned 判断这首歌能不能放。title 建议传「歌名 + 歌手」拼起来的串，
// 这样关键词填歌名或填歌手都能命中。
//
// 大小写不敏感；关键词是子串匹配（"周杰" 能命中 "周杰伦 - 晴天"）。
func IsTrackBanned(title string, banned []string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return false
	}
	for _, kw := range banned {
		k := strings.ToLower(strings.TrimSpace(kw))
		if k != "" && strings.Contains(t, k) {
			return true
		}
	}
	return false
}
