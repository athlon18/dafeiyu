package tts

import "testing"

// 老板踩过的坑：装了 ffplay 时 SetOutputDevice 被静默忽略，声音跑到系统默认设备。
// RouteDevice 必须把出口切成原生设备路由；选不到的设备要返回 false，且不得改变出口。
func TestRouteDeviceUnknownKeepsExit(t *testing.T) {
	p := NewPlayer()
	before := p.exitBin()
	if _, ok := RouteDevice("一定不存在的设备名-xyzzy"); ok {
		t.Fatal("不存在的设备被当成了命中")
	}
	if p.exitBin() != before {
		t.Fatalf("未命中时不该动出口: before=%q after=%q", before, p.exitBin())
	}
}

func TestRouteDeviceEmptyNameIsNoop(t *testing.T) {
	p := NewPlayer()
	before := p.exitBin()
	if _, ok := RouteDevice(""); ok {
		t.Fatal("空设备名不该算已路由")
	}
	if p.exitBin() != before {
		t.Fatalf("空设备名不该动出口: before=%q after=%q", before, p.exitBin())
	}
}

// 路由生效的唯一可观察后果：ffplay 这条路必须被关掉，否则 SetOutputDevice 是假的。
func TestApplyRouteDropsFfplayWhenRouted(t *testing.T) {
	p := &Player{bin: "C:/somewhere/ffplay.exe", native: true}
	setRouted(true)
	defer setRouted(false)
	ApplyRoute(p)
	if got := p.exitBin(); got != "" {
		t.Fatalf("路由生效却仍留着 ffplay 出口: %q", got)
	}
}

func TestApplyRouteNoopWhenNotRouted(t *testing.T) {
	p := &Player{bin: "C:/somewhere/ffplay.exe", native: true}
	setRouted(false)
	ApplyRoute(p)
	if got := p.exitBin(); got == "" {
		t.Fatal("没要求路由却把 ffplay 关了")
	}
}
