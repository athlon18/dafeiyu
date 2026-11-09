// Package stage 是"画面上的大肥鱼"看到的那份现场状态。
//
// 声音由本机声卡在放，叠层不能再放一遍（那会双重出声），所以这里只往外送三样：
// 在不在说话、从哪一刻开始说、每一格嘴该张多大。叠层每秒取一次，按经过的毫秒数取格。
package stage

import (
	"sync"
	"time"

	"github.com/athlon18/dafeiyu/tts"
)

type State struct {
	Speaking bool   `json:"speaking"`
	SpeakAt  int64  `json:"speakAt"` // 第一段出声的 Unix 毫秒
	Env      []int  `json:"env"`     // 20Hz 响度，0~100
	Caption  string             `json:"caption"`
	Model    string             `json:"model"`
	Credit   string             `json:"credit"`
	Params   map[string]float64 `json:"params"`
}

type Stage struct {
	mu       sync.Mutex
	speaking bool
	speakAt  int64
	env      []int
	caption  string
	model    string
	credit   string
	params   map[string]float64
}

func New(model, credit string, params map[string]float64) *Stage {
	return &Stage{model: model, credit: credit, params: params}
}

// Begin 一句话要说了。字幕先上，包络等第一段合成好再登记。
func (s *Stage) Begin(text string) {
	s.mu.Lock()
	s.caption = text
	s.mu.Unlock()
}

// Segment 登记一段即将出声的音频。多段流水线时只有第一段定起点，
// 后面的包络接在后面——各自定时刻的话，叠层会对不上嘴。返回这一段贡献的格数。
func (s *Stage) Segment(res tts.Result) int {
	env := Envelope(res)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.speaking {
		s.speaking = true
		s.speakAt = time.Now().UnixMilli()
		s.env = env
	} else {
		s.env = append(s.env, env...)
	}
	return len(env)
}

// End 说完/被抢话：清掉说话状态，叠层下一拍收到 speaking=false 就把嘴合上。
// 字幕留着——观众得来得及读完刚说的那句。
func (s *Stage) End() {
	s.mu.Lock()
	s.speaking = false
	s.speakAt = 0
	s.env = nil
	s.mu.Unlock()
}

func (s *Stage) Snapshot() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{
		Speaking: s.speaking,
		SpeakAt:  s.speakAt,
		Env:      append([]int(nil), s.env...),
		Caption:  s.caption,
		Model:    s.model,
		Credit:   s.credit,
		Params:   s.params,
	}
}
