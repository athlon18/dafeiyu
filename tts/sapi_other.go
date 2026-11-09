//go:build !windows

// 非 Windows 没有 SAPI；选择这个通道时如实报错，不要静默降级——
// 静默降级会让人以为“换了更快的声音”，其实还在用原来那条慢的。
package tts

import (
	"context"
	"fmt"
)

func synthSAPI(context.Context, string, string, int) ([]byte, string, error) {
	return nil, "", fmt.Errorf("本地 SAPI 通道只在 Windows 上可用")
}

func sapiVoices(context.Context) []string { return nil }
