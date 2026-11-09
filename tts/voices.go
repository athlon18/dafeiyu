package tts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Voice 一个可选的音色。配置页的「拉取音色」拉的就是这份清单。
//
// Commercial 只表达一件事：**服务方有没有给出可商用的授权口径**。
// true = 服务自己声明有（如 MiniMax 付费音色）；false = 没声明，页面提示自行核实。
// 这个字段存在的理由和 /api/host/models 的 Commercial 一样：
// 开播是要挣钱的行为，页面必须能一眼看出「这套声音能不能拿去商用」，
// 而不是等被追责了才发现用的是 edge-tts 这种没有商用授权的接口。
type Voice struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Note       string `json:"note"`
	Commercial bool   `json:"commercial"`
}

const (
	// voicesMaxBytes 音色清单的响应上限。清单只有几十行，
	// 超过这个量级说明地址多半填错了（拿回了一整页 HTML），没必要往下读。
	voicesMaxBytes = 256 << 10
	// voicesTimeout 拉清单的硬超时：页面点一下要等得住的量级。
	voicesTimeout = 4 * time.Second
)

// Voices 向当前配置的音色服务问「你有哪些音色」。
//
// 只覆盖 openai / sapi：edge 的音色清单在前端常量里（切通道时还要拿它判断
// 哪些音色名不能带过去），服务端再抄一份就是第二处真相，早晚会漂移。
func Voices(ctx context.Context, c Config) ([]Voice, error) {
	switch c.Kind {
	case KindSAPI:
		names := sapiVoices(ctx)
		out := make([]Voice, 0, len(names))
		for _, n := range names {
			out = append(out, Voice{ID: n, Name: n, Note: "本机 SAPI 引擎 · 音质一般，适合兜底"})
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("本机没装可用的 SAPI 音色")
		}
		return out, nil
	case KindOpenAI, "":
		return fetchVoices(ctx, c)
	}
	return nil, fmt.Errorf("通道 %q 的音色是页面内置清单，不用向服务要", c.Kind)
}

// fetchVoices 走 `GET {base}/voices`：本地 Piper 网关和 MiniMax 网关都实现了这个口。
// 不要求 OpenAI 官方实现（它没有列音色的端点），拉不到就回原因、页面继续手填。
func fetchVoices(ctx context.Context, c Config) ([]Voice, error) {
	base := strings.TrimSpace(c.BaseURL)
	if base == "" {
		return nil, fmt.Errorf("还没填 TTS 服务地址")
	}
	ctx, cancel := context.WithTimeout(ctx, voicesTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, voicesURL(base), nil)
	if err != nil {
		return nil, err
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := (&http.Client{Timeout: voicesTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("连不上音色服务：%w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, voicesMaxBytes))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("音色服务返回 HTTP %d: %s", resp.StatusCode, snippet(body))
	}
	return parseVoices(body)
}

// voiceItem 兼容几种常见回法：{"voices":[...]}/{"data":[...]}/裸数组。
// 字段名优先 id/name/note，缺 name 时用 id 顶上（页面上至少不是空白选项）。
type voiceItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Note       string `json:"note"`
	Commercial bool   `json:"commercial"`
}

func parseVoices(body []byte) ([]Voice, error) {
	var doc struct {
		Voices []voiceItem `json:"voices"`
		Data   []voiceItem `json:"data"`
	}
	items := []voiceItem{}
	if err := json.Unmarshal(body, &doc); err == nil {
		items = append(items, doc.Voices...)
		items = append(items, doc.Data...)
	}
	if len(items) == 0 {
		// 裸数组：有些网关直接把列表当响应体。
		if err := json.Unmarshal(body, &items); err != nil {
			return nil, fmt.Errorf("音色服务没回可识别的清单（需要 GET /voices，返回 {\"voices\":[...]}）")
		}
	}
	out := make([]Voice, 0, len(items))
	for _, it := range items {
		id := strings.TrimSpace(it.ID)
		if id == "" {
			continue // 没有 id 的项没法用来合成，留着只会让页面选出一个必然失败的值
		}
		name := strings.TrimSpace(it.Name)
		if name == "" {
			name = id
		}
		out = append(out, Voice{ID: id, Name: name, Note: strings.TrimSpace(it.Note), Commercial: it.Commercial})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("音色服务返回了空清单")
	}
	return out, nil
}

// voicesURL 与 speechURL 同口径：基址填到 /v1 还是填完整路径都认。
func voicesURL(base string) string {
	base = strings.TrimSuffix(strings.TrimSpace(base), "/")
	if strings.HasSuffix(base, "/audio/speech") {
		base = strings.TrimSuffix(base, "/audio/speech")
	}
	if strings.HasSuffix(base, "/voices") {
		return base
	}
	return base + "/voices"
}
