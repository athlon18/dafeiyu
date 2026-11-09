package rules

// StageOf 礼物连击分档。阈值与 douyinlive/modules/live/gift_tier.go 一致，
// 改这里等于改口径——那是主播和观众都记住的东西，不许自创。
func StageOf(count uint64) uint8 {
	switch {
	case count >= 100:
		return 4
	case count >= 50:
		return 3
	case count >= 10:
		return 2
	default:
		return 1
	}
}

// epicNames 官方全屏特效级礼物名单，与主仓 epicGiftNames 同表。
// 刻意不按价格判 epic：主仓注释写明"暂无官方阈值口径，先不凭空猜价"。
var epicNames = map[string]bool{
	"嘉年华":  true,
	"超级跑车": true,
}

// IsEpic 供叠层选版式用（第三刀），播报侧只按连击段数分档。
func IsEpic(name string) bool { return epicNames[name] }
