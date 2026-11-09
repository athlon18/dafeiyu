//go:build !windows

// 非 Windows 平台没有 MCI，只能靠 ffplay；没有 ffplay 就是"这台机器放不出声"。
// 明确返回错误而不是静默成功：静默会让"主播不说话"变成一个查不出来的谜。
package tts

import (
	"context"
	"fmt"
)

func nativeAvailable() bool { return false }

func playNative(context.Context, string) error {
	return fmt.Errorf("%s", NotFoundHint())
}

// 非 Windows 平台没有多设备选择：ffplay 走系统默认输出。
// 保留同名 API 是为了让配置层不用按平台写两套分支。

type OutputDevice struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func SetOutputDevice(int)      {}
func CurrentOutputDevice() int { return -1 }

func OutputDevices() []OutputDevice {
	return []OutputDevice{{ID: -1, Name: "系统默认（本平台不支持选设备）"}}
}
