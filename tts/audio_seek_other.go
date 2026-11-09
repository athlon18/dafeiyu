//go:build !windows

// 非 Windows 没有 waveOut：暂停/续播/位置一律不支持，
// 如实返回 false/0，让上层走“停下 + 从头播”的降级路径，
// 而不是假装停住了。
package tts

func nativePause() bool     { return false }
func nativeResume() bool    { return false }
func nativePosition() int   { return 0 }
func nativeSeekable() bool  { return false }
