// Package brain 组一句主播要说的话：配了 key 走 LLM，没配就没有 LLM 这条路。
//
// 本包只管"生成"，不管"该不该说"——后者是 rules + host 的事。
// 说出来的话术模板走的是 host.ChatInput.Speak/Line，不经过这里，
// 所以没 key 的机器上除了弹幕回话以外都还能开口。
package brain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	APIKey       string
	BaseURL      string
	Model        string
	Persona      string // 人设 prompt，编排器没给 system 时兜出来用
	TimeoutSec   int
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type request struct {
	Model    string    `json:"model"`
	Messages []message `json:"messages"`
}

type Brain struct {
	cfg Config
	hc  *http.Client
}

// New 没配 key 就返回 nil：调用方必须接得住这个 nil，并让弹幕那段安静下来。
func New(cfg Config) *Brain {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.TimeoutSec <= 0 {
		cfg.TimeoutSec = 20
	}
	return &Brain{cfg: cfg, hc: &http.Client{Timeout: time.Duration(cfg.TimeoutSec) * time.Second}}
}

func (b *Brain) Think(ctx context.Context, sys, usr string) (string, error) {
	if strings.TrimSpace(sys) == "" {
		sys = b.cfg.Persona
	}
	body, err := json.Marshal(request{
		Model:    b.cfg.Model,
		Messages: []message{{Role: "system", Content: sys}, {Role: "user", Content: usr}},
	})
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(b.cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.cfg.APIKey)

	resp, err := b.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("调 LLM 失败: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("LLM 返回 %d: %s", resp.StatusCode, snippet(raw))
	}
	var parsed struct {
		Choices []struct {
			Message message `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", fmt.Errorf("解析 LLM 回复: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("LLM 没给 choices")
	}
	out := strings.TrimSpace(parsed.Choices[0].Message.Content)
	if out == "" {
		return "", fmt.Errorf("LLM 空回复")
	}
	return out, nil
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len([]rune(s)) > 120 {
		return string([]rune(s)[:120])
	}
	return s
}
