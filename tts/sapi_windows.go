//go:build windows

// Windows 本地 TTS（SAPI / System.Speech）。
//
// 定位：**快**，不是**好听**。
// 实测同一句话：edge-tts 要 2.5 秒（大头是连微软服务器的固定开销，跟文本长短几乎无关），
// 本地 SAPI 只要 90~130 毫秒。想压低首包延迟，唯一的正解是别出网。
//
// 代价是音质：系统自带的中文音色（Microsoft Huihui Desktop）是十几年前的引擎，
// 听起来像老式导航。所以它只适合作"低延迟模式"或"edge 挂了时的兜底"，
// 不适合当默认——AI 主播的声音是人设的一部分，机械音会毁掉观感。
package tts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// sapiDefaultVoice Windows 自带的中文音色。找不到就让系统自己选默认。
const sapiDefaultVoice = "Microsoft Huihui Desktop"

// synthSAPI 调 System.Speech 合成一段 wav。
//
// 为什么走 PowerShell 而不是 Go 里直接调 COM：
// SAPI 的 COM 接口（ISpVoice）需要手写一堆 vtable 和流对象，而 System.Speech
// 把这一整套包好了。代价是每次要起一个 PowerShell 进程（约 200~300ms），
// 即便这样整体仍比 edge-tts 快 5 倍以上。等以后真要榨到 50ms 再考虑 COM。
func synthSAPI(ctx context.Context, text, voice string, timeoutSec int) ([]byte, string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, "", fmt.Errorf("TTS 文本为空")
	}
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	dir, err := os.MkdirTemp("", "dysapi-")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = os.RemoveAll(dir) }()

	in := filepath.Join(dir, "in.txt")
	out := filepath.Join(dir, "out.wav")
	// 文本落文件而不是拼进命令行：直播弹幕里什么引号、换行都可能出现，内联必炸。
	if err := os.WriteFile(in, []byte(text), 0o600); err != nil {
		return nil, "", err
	}

	v := strings.TrimSpace(voice)
	if v == "" {
		v = sapiDefaultVoice
	}
	// -Encoding UTF8 是硬要求：默认编码下中文会变成乱码念出来。
	ps := fmt.Sprintf(`Add-Type -AssemblyName System.Speech
$t = Get-Content -Raw -Encoding UTF8 '%s'
$s = New-Object System.Speech.Synthesis.SpeechSynthesizer
try { $s.SelectVoice('%s') } catch { }
$s.SetOutputToWaveFile('%s')
$s.Speak($t)
$s.Dispose()`, in, v, out)

	child, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()
	cmd := exec.CommandContext(child, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", ps)
	if b, err := cmd.CombinedOutput(); err != nil {
		return nil, "", fmt.Errorf("SAPI 合成失败: %v: %s", err, snippet(b))
	}
	audio, err := os.ReadFile(out)
	if err != nil {
		return nil, "", fmt.Errorf("SAPI 没产出音频（音色 %s 可能不存在）: %w", v, err)
	}
	if len(audio) == 0 {
		return nil, "", fmt.Errorf("SAPI 输出空音频")
	}
	return audio, "audio/wav", nil
}

// sapiVoices 列出本机可用的音色（配置页下拉用；调用方少，代价可接受）。
func sapiVoices(ctx context.Context) []string {
	child, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(child, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		`Add-Type -AssemblyName System.Speech; (New-Object System.Speech.Synthesis.SpeechSynthesizer).GetInstalledVoices() | ForEach-Object { $_.VoiceInfo.Name }`).Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			names = append(names, s)
		}
	}
	return names
}
