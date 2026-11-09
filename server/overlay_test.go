package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"net/url"
	"testing"

	"github.com/athlon18/dafeiyu/stage"
	"github.com/athlon18/dafeiyu/store"
	"github.com/athlon18/dafeiyu/tts"
)

func newOverlayServer(t *testing.T) (*httptest.Server, *stage.Stage, string) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 皮不入库，测试里造一个最小目录树当 data/host。
	root := t.TempDir()
	modelDir := filepath.Join(root, "model", "大肥鱼")
	if err := os.MkdirAll(filepath.Join(modelDir, "大肥鱼.8192"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "大肥鱼.model3.json"), []byte(`{"Version":3}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modelDir, "大肥鱼.8192", "texture_00.png"), []byte("PNG"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "oml.min.js"), []byte("window.OML2D={}"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 目录外放一个"机密"，专门用来证明穿越拿不到它
	if err := os.WriteFile(filepath.Join(filepath.Dir(root), "secret.txt"), []byte("COOKIE"), 0o644); err != nil {
		t.Fatal(err)
	}

	st := stage.New("大肥鱼", "模型：Mao绒绒家园", map[string]float64{"Paramcol_3": 1})
	srv := New(db, nil).WithStage(st).WithAssets(root)
	mux := http.NewServeMux()
	srv.Register(mux)
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	return hs, st, root
}

// 叠层每秒取一次状态：说话中必须带着起点时刻和包络，否则嘴只会僵着。
func TestStateServesSpeakingEnvelope(t *testing.T) {
	hs, st, _ := newOverlayServer(t)
	st.Begin("感谢老板的嘉年华")
	st.Segment(tts.Result{Audio: wavForTest(), MIME: "audio/wav"})

	resp, err := http.Get(hs.URL + "/state")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body := readAll(t, resp)
	for _, want := range []string{`"speaking":true`, "感谢老板的嘉年华", `"speakAt":1`, `"env":[`} {
		if want == `"speakAt":1` {
			if !strings.Contains(body, `"speakAt":1`) {
				t.Fatalf("speakAt 没带出来: %s", body)
			}
			continue
		}
		if !strings.Contains(body, want) {
			t.Fatalf("state 里缺 %s，实际 %s", want, body)
		}
	}
}

// OBS 挂的就是这个页面：必须能取到，且是 HTML。
func TestOverlayPageServed(t *testing.T) {
	hs, _, _ := newOverlayServer(t)
	resp, err := http.Get(hs.URL + "/overlay")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("叠层页取不到: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("Content-Type 不对: %s", ct)
	}
	body := readAll(t, resp)
	if !strings.Contains(body, "oml.min.js") {
		t.Fatal("叠层页没引渲染库")
	}
}

// 叠层要真透明：页面里不许有 body 背景色，否则 OBS 里角色背后一块板。
func TestOverlayCSSIsTransparent(t *testing.T) {
	hs, _, _ := newOverlayServer(t)
	resp, err := http.Get(hs.URL + "/overlay.css")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("叠层样式取不到: %d", resp.StatusCode)
	}
	css := readAll(t, resp)
	if strings.Contains(css, "background: radial-gradient") || strings.Contains(css, "background-color: #") {
		t.Fatalf("叠层样式里有底色，OBS 会出现一块板: %s", css)
	}
	if !strings.Contains(css, "transparent") {
		t.Fatal("叠层样式没声明透明")
	}
}

// 叠层脚本也得由二进制给，不能让主播再开一个静态服务。
func TestOverlayJSServed(t *testing.T) {
	hs, _, _ := newOverlayServer(t)
	resp, err := http.Get(hs.URL + "/overlay.js")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("叠层脚本取不到: %d", resp.StatusCode)
	}
	if !strings.Contains(readAll(t, resp), "/state") {
		t.Fatal("叠层脚本没去取状态")
	}
}

// 模型文件从磁盘取（皮不入库），但必须锁在 data/host 里。
func TestModelAssetServed(t *testing.T) {
	hs, _, _ := newOverlayServer(t)
	resp, err := http.Get(hs.URL + "/model/" + urlEnc("大肥鱼") + "/" + urlEnc("大肥鱼") + ".model3.json")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("模型清单取不到: %d", resp.StatusCode)
	}
}

// 路径穿越必须挡住：/model/../../secret.txt 不能把目录外的东西送出去。
func TestAssetTraversalBlocked(t *testing.T) {
	hs, _, _ := newOverlayServer(t)
	for _, p := range []string{
		"/model/../../secret.txt",
		"/model/%2e%2e%2f%2e%2e%2fsecret.txt",
		"/model/..%2f..%2fsecret.txt",
		"/lib/../secret.txt",
	} {
		resp, err := http.Get(hs.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		body := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK && strings.Contains(body, "COOKIE") {
			t.Fatalf("%s 把目录外的文件送出去了", p)
		}
	}
}

// 渲染库要给出去，且只给 .js/.css 这类该给的扩展名。
func TestAssetExtensionWhitelist(t *testing.T) {
	hs, _, root := newOverlayServer(t)
	if err := os.WriteFile(filepath.Join(root, "model", "大肥鱼", "dump.db"), []byte("SQLITE"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(hs.URL + "/model/" + urlEnc("大肥鱼") + "/dump.db")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("白名单外的扩展名被送出去了")
	}
}

// 试听必须走"登记口型 → 合成 → 出声"这条和直播一样的路：
// 只出声不登记的话，后台预览里她的嘴是僵的。合成挂掉时字幕也该已经上去了。
func TestPreviewRegistersCaptionOnStage(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 故意把地址指到一个没人听的端口：合成必然失败，也不会在本机真出声。
	if err := db.Save(store.Settings{
		Voice: store.Voice{Engine: "openai", BaseURL: "http://127.0.0.1:1/v1", Model: "tts-1", Timbre: "x", Speed: 1},
	}); err != nil {
		t.Fatal(err)
	}
	st := stage.New("大肥鱼", "", nil)
	srv := New(db, tts.NewPlayer()).WithStage(st)
	mux := http.NewServeMux()
	srv.Register(mux)
	hs := httptest.NewServer(mux)
	defer hs.Close()

	resp, err := http.Get(hs.URL + "/tts/preview?text=测试一句")
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	resp.Body.Close()
	if !strings.Contains(body, `"err"`) {
		t.Fatalf("合成挂掉却报了成功: %s", body)
	}
	if got := st.Snapshot(); got.Caption != "测试一句" {
		t.Fatalf("试听没把字幕登记到叠层状态上: %+v", got)
	}
	if got := st.Snapshot(); got.Speaking {
		t.Fatal("没真出声却标着在说话，叠层会张着嘴等")
	}
}

func wavForTest() []byte {
	n := 22050 / 4
	raw := make([]byte, n*2)
	for i := 0; i < n; i++ {
		v := uint16(8000)
		raw[i*2], raw[i*2+1] = byte(v), byte(v>>8)
	}
	out := []byte("RIFF")
	le32 := func(v uint32) []byte { return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)} }
	out = append(out, le32(uint32(36+len(raw)))...)
	out = append(out, []byte("WAVEfmt ")...)
	out = append(out, le32(16)...)
	out = append(out, []byte{1, 0, 1, 0}...)
	out = append(out, le32(22050)...)
	out = append(out, le32(44100)...)
	out = append(out, []byte{2, 0, 16, 0}...)
	out = append(out, []byte("data")...)
	out = append(out, le32(uint32(len(raw)))...)
	return append(out, raw...)
}

func readAll(t *testing.T, r *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	return string(b)
}

func urlEnc(s string) string { return url.QueryEscape(s) }

// 上一轮那条 HTTP 层穿越测试是假绿：Go 的 http client 会先把 .. 规范化掉，
// 请求根本走不到我的校验。这里直接打守卫本身，它必须能变红。
func TestSafeRelRejectsDotDot(t *testing.T) {
	bad := []string{
		"../../secret.txt",
		"大肥鱼/../../secret.txt",
		"..%2f..%2fsecret.txt",
		"./../x.json",
		"/absolute/x.json",
		`大肥鱼\..\..\secret.txt`,
	}
	for _, p := range bad {
		if _, ok := safeRel(p); ok {
			t.Errorf("safeRel 放过了 %q", p)
		}
	}
	good := []string{"大肥鱼/大肥鱼.model3.json", "大肥鱼/大肥鱼.8192/texture_00.png"}
	for _, p := range good {
		if got, ok := safeRel(p); !ok {
			t.Errorf("safeRel 误杀了正常路径 %q", p)
		} else if strings.Contains(got, "..") {
			t.Errorf("清洗后仍带 ..: %q", got)
		}
	}
}
