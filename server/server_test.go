package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"path/filepath"
	"testing"

	"github.com/athlon18/dafeiyu/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := New(st, nil)
	mux := http.NewServeMux()
	srv.Register(mux)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return hs, st
}

// 页面上"保存"必须真落到库里：先 POST 再 GET 回来，值要一模一样。
func TestConfigRoundTrip(t *testing.T) {
	hs, _ := newTestServer(t)
	body := `{"roomId":"7319","cookie":"c","engine":"openai","baseUrl":"http://127.0.0.1:9880/v1",
	  "apiKey":"","model":"tts-1","timbre":"zh_CN-huayan-medium","speed":1.0,"device":"CABLE Input",
	  "rules":[{"moment":"有人送嘉年华","line":"感谢 {user}","enabled":true}]}`
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/config", bytes.NewBufferString(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存返回 %d", resp.StatusCode)
	}
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != true {
		t.Fatalf("保存没成功: %v", got)
	}

	rr, err := http.Get(hs.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	defer rr.Body.Close()
	var cfg map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["roomId"] != "7319" {
		t.Fatalf("读回的房间号不对: %v", cfg["roomId"])
	}
	rules, _ := cfg["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("规则读回 %d 条", len(rules))
	}
}

// GET 之后拿到的是"库里现在的值"，不是出厂值——否则页面每次进来都变回默认。
func TestGetReflectsSavedDevice(t *testing.T) {
	hs, st := newTestServer(t)
	if err := st.Save(store.Settings{
		Conn:  store.Conn{RoomID: "r1"},
		Voice: store.Voice{Device: "Voicemeeter Input"},
		Rules: []store.Rule{{Moment: "有人进直播间", Line: "欢迎 {user}", Enabled: true}},
	}); err != nil {
		t.Fatal(err)
	}
	rr, err := http.Get(hs.URL + "/config")
	if err != nil {
		t.Fatal(err)
	}
	defer rr.Body.Close()
	var cfg map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&cfg)
	if cfg["device"] != "Voicemeeter Input" {
		t.Fatalf("设备没反映库里的值: %v", cfg["device"])
	}
}

// 空话术要在保存时就挡住，并把原因带回页面——静默收下等于让她在该说话时说出空气。
func TestSaveRejectsBlankLineWithReason(t *testing.T) {
	hs, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/config",
		bytes.NewBufferString(`{"rules":[{"moment":"有人送嘉年华","line":"  ","enabled":true}]}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("空话术被存下了")
	}
	var got map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&got)
	if e, _ := got["err"].(string); e == "" {
		t.Fatalf("挡住了但没给原因: %v", got)
	}
}

// 闭麦是现场开关，单独一个口，且必须落到库里（重启不许自动开麦）。
func TestMutePersists(t *testing.T) {
	hs, st := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/mute", bytes.NewBufferString(`{"muted":true}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	got, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !got.Muted {
		t.Fatal("闭麦没存住")
	}
}

// 这接口能改主播的声音和开关，只接受本机来源；别的机器不能来遥控。
func TestRejectsForeignOrigin(t *testing.T) {
	hs, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, hs.URL+"/config", nil)
	req.Header.Set("Origin", "http://10.0.0.9:8421")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("外部来源应当被拒，实际 %d", resp.StatusCode)
	}
}

// 带 JSON 的 POST 会先发 CORS 预检；漏了 Allow-Headers，浏览器会直接拦掉请求，
// 页面表现为"服务没起"——比报错更难查。GET 不带 JSON 所以照不出这个洞。
func TestPreflightAllowsJsonPost(t *testing.T) {
	hs, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodOptions, hs.URL+"/config", nil)
	req.Header.Set("Origin", "http://127.0.0.1:8421")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("预检应回 204，实际 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(strings.ToLower(got), "content-type") {
		t.Fatalf("预检没放行 content-type: %q", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:8421" {
		t.Fatalf("预检没回显来源: %q", got)
	}
}

// 预检过了之后，真 POST 也得带着 CORS 头，否则浏览器仍会拦。
func TestPostCarriesCORSHeaders(t *testing.T) {
	hs, _ := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/config",
		bytes.NewBufferString(`{"roomId":"r","rules":[{"moment":"有人送嘉年华","line":"谢谢","enabled":true}]}`))
	req.Header.Set("Origin", "http://127.0.0.1:8421")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("保存返回 %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "http://127.0.0.1:8421" {
		t.Fatalf("响应没带 Allow-Origin: %q", got)
	}
}

// 保存必须把新配置推给正在跑的边车：只改库不生效，主播现场听到的还是旧话术。
func TestOnChangeFiresOnSave(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var got []store.Settings
	srv := New(st, nil).OnChange(func(s store.Settings) { got = append(got, s) })
	mux := http.NewServeMux()
	srv.Register(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/config",
		bytes.NewBufferString(`{"roomId":"r9","rules":[{"moment":"有人送嘉年华","line":"谢谢 {user}","enabled":true}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(got) != 1 {
		t.Fatalf("保存后没推给现场（回调触发 %d 次）", len(got))
	}
	if got[0].Conn.RoomID != "r9" || len(got[0].Rules) != 1 {
		t.Fatalf("推下去的不是刚存的配置: %+v", got[0])
	}
}

// 闭麦同理：点完就该当场闭，不能等重启。
func TestOnChangeFiresOnMute(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var muted []bool
	srv := New(st, nil).OnChange(func(s store.Settings) { muted = append(muted, s.Muted) })
	mux := http.NewServeMux()
	srv.Register(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/mute", bytes.NewBufferString(`{"muted":true}`))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(muted) != 1 || !muted[0] {
		t.Fatalf("闭麦没推给现场: %v", muted)
	}
}

// 直播中点"保存"不许把麦开回去：请求体里没有 muted，apply 必须拿库里那份带状态的配置。
func TestSaveKeepsMuteState(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetMuted(true); err != nil {
		t.Fatal(err)
	}
	var applied []store.Settings
	srv := New(st, nil).OnChange(func(s store.Settings) { applied = append(applied, s) })
	mux := http.NewServeMux()
	srv.Register(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	// 页面保存时发的就是这份：没有 muted 字段。
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/config",
		bytes.NewBufferString(`{"roomId":"r","rules":[{"moment":"有人送嘉年华","line":"谢谢","enabled":true}]}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(applied) != 1 {
		t.Fatalf("没推给现场: %d 次", len(applied))
	}
	if !applied[0].Muted {
		t.Fatalf("保存把闭麦状态冲掉了——直播中会当场开麦: %+v", applied[0])
	}
}
