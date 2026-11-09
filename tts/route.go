package tts

import (
	"strings"
	"sync"
)

// 设备路由开关。刻意做成包级状态而不是 Player 字段：SetOutputDevice 本身是进程级的
// Windows 调用，摆在 Player 上会让人误以为"每台播放器各选各的设备"。
var routeMu sync.Mutex
var routedNow bool

// RouteDevice 按设备名片段选输出设备。命中后必须让播放器放弃 ffplay——
// ffplay 只认系统默认设备，留着它 SetOutputDevice 就是假的。
// 返回 false 表示没匹配到，此时不改出口、不改路由状态，由调用方决定回落。
func RouteDevice(name string) (OutputDevice, bool) {
	want := strings.ToLower(strings.TrimSpace(name))
	if want == "" {
		return OutputDevice{}, false
	}
	var hit OutputDevice
	found := false
	for _, d := range OutputDevices() {
		if strings.Contains(strings.ToLower(d.Name), want) {
			hit, found = d, true
			break
		}
	}
	if !found {
		return OutputDevice{}, false
	}
	SetOutputDevice(hit.ID)
	setRouted(true)
	return hit, true
}

// Routed 当前是否被强制路由到指定设备。
func Routed() bool {
	routeMu.Lock()
	defer routeMu.Unlock()
	return routedNow
}

func setRouted(v bool) {
	routeMu.Lock()
	routedNow = v
	routeMu.Unlock()
}

// exitBin 当前 ffplay 出口路径（空=只走原生通道）。
func (p *Player) exitBin() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bin
}

// ApplyRoute 在选定设备后重建出口。没路由过就什么都不做。
func ApplyRoute(p *Player) {
	if p == nil || !Routed() {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bin = ""
	p.native = nativeAvailable()
}
