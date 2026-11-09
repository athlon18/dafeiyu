// Package server 后台页的接口：配置读写、闭麦开关、试听、设备清单。
//
// 只开页面看得见的东西。每个口都必须"要么真做成，要么明确报错"——
// 上一版页面上挂着 保存/闭麦/加一条 三个没有后端的按钮，点了 0 请求，那是假控件。
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/athlon18/dafeiyu/stage"
	"github.com/athlon18/dafeiyu/store"
	"github.com/athlon18/dafeiyu/tts"
)

type Server struct {
	st     *store.Store
	player *tts.Player
	stage  *stage.Stage
	assets string
	// apply 把刚存下的配置推给**正在跑**的边车。没有它，"保存"只是改库，
	// 主播现场听到的还是旧话术——那页面就是个摆设。
	apply func(store.Settings)
}

func New(st *store.Store, player *tts.Player) *Server { return &Server{st: st, player: player} }

// OnChange 注册热生效回调（注册了才热，没注册就退回"下次启动生效"）。
func (s *Server) OnChange(fn func(store.Settings)) *Server { s.apply = fn; return s }

func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/config", s.config)
	mux.HandleFunc("/mute", s.mute)
	mux.HandleFunc("/tts/preview", s.preview)
	mux.HandleFunc("/tts/devices", s.devices)
	s.registerStatic(mux)
}

// cors 放开本机跨源：后台页可能由静态服务打开，接口在另一个端口。
// 只接受来自 127.0.0.1/localhost 的 Origin——这个口能改主播的声音和开关，不该对全网开。
func (s *Server) cors(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" && !strings.HasPrefix(origin, "http://127.0.0.1") && !strings.HasPrefix(origin, "http://localhost") {
		http.Error(w, "forbidden origin", http.StatusForbidden)
		return false
	}
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	if r.Method == http.MethodOptions {
		// 带 JSON 的 POST 会先发预检；少了 Allow-Headers，浏览器会静默拦掉真请求，
		// 页面只会显示"服务没起"——比报错难查得多。
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	return true
}

func jsonOut(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) config(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		return
	}
	switch r.Method {
	case http.MethodGet:
		cfg, err := s.st.Load()
		if err != nil {
			jsonOut(w, http.StatusInternalServerError, map[string]string{"err": err.Error()})
			return
		}
		jsonOut(w, http.StatusOK, cfg)
	case http.MethodPost:
		var in store.Settings
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			jsonOut(w, http.StatusBadRequest, map[string]string{"err": "配置读不进来：" + err.Error()})
			return
		}
		if err := s.st.Save(in); err != nil {
			log.Printf("保存失败：%v", err)
			jsonOut(w, http.StatusBadRequest, map[string]string{"err": err.Error()})
			return
		}
		if s.apply != nil {
			// 必须推**库里的**那份，不能推请求体：页面 POST 的 body 里没有 muted，
			// 拿 body 去 apply 会在直播中点一次保存就悄悄把麦开回去。
			if fresh, err := s.st.Load(); err == nil {
				s.apply(fresh)
			} else {
				log.Printf("保存后读库失败，现场未更新：%v", err)
			}
		}
		jsonOut(w, http.StatusOK, map[string]any{"ok": true, "rules": len(in.Rules), "applied": s.apply != nil})
	default:
		jsonOut(w, http.StatusMethodNotAllowed, map[string]string{"err": "只支持 GET/POST"})
	}
}

func (s *Server) mute(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		return
	}
	if r.Method != http.MethodPost {
		jsonOut(w, http.StatusMethodNotAllowed, map[string]string{"err": "只支持 POST"})
		return
	}
	var in struct {
		Muted bool `json:"muted"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		jsonOut(w, http.StatusBadRequest, map[string]string{"err": err.Error()})
		return
	}
	if err := s.st.SetMuted(in.Muted); err != nil {
		jsonOut(w, http.StatusInternalServerError, map[string]string{"err": err.Error()})
		return
	}
	// 闭麦要立刻作用到现场：重新读一份带 muted 的配置推下去。
	if s.apply != nil {
		if cfg, err := s.st.Load(); err == nil {
			s.apply(cfg)
		}
	}
	jsonOut(w, http.StatusOK, map[string]any{"ok": true, "muted": in.Muted, "applied": s.apply != nil})
}

type previewResp struct {
	OK      bool   `json:"ok"`
	SynthMs int64  `json:"synthMs"`
	PlayMs  int64  `json:"playMs"`
	Device  string `json:"device"`
	Err     string `json:"err"`
}

// preview 用**库里现在存的声音设置**合成并播出：页面上改了音色/设备，试听就得跟着变，
// 否则"试听到的"和"直播里播的"是两套东西，试听的反馈毫无意义。
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		return
	}
	res := previewResp{Device: "系统默认"}
	if s.player == nil {
		res.Err = "服务没接播放器"
		jsonOut(w, http.StatusOK, res)
		return
	}
	cfg, err := s.st.Load()
	if err != nil {
		res.Err = "读配置失败：" + err.Error()
		jsonOut(w, http.StatusOK, res)
		return
	}
	text := strings.TrimSpace(r.URL.Query().Get("text"))
	if text == "" {
		text = "这是一句试听"
	}
	tcfg := tts.Config{
		Kind: tts.Kind(cfg.Voice.Engine), BaseURL: cfg.Voice.BaseURL, APIKey: cfg.Voice.APIKey,
		Model: cfg.Voice.Model, Voice: cfg.Voice.Timbre, Speed: cfg.Voice.Speed, TimeoutSec: 30,
	}
	t0 := time.Now()

	dev := cfg.Voice.Device
	if q := strings.TrimSpace(r.URL.Query().Get("device")); q != "" {
		dev = q
	}
	if strings.TrimSpace(dev) != "" {
		d, ok := tts.RouteDevice(dev)
		if !ok {
			res.Err = fmt.Sprintf("没匹配到输出设备 %q（用 /tts/devices 看可选清单）", dev)
			jsonOut(w, http.StatusOK, res)
			return
		}
		tts.ApplyRoute(s.player)
		res.Device = d.Name
	}
	// 试听走和直播同一条出口：登记口型包络 → 分段合成 → 出声。
	// 只调 Synthesize+Play 的话，后台预览里她的嘴不会动，"试听"就成了半条链路。
	opts := tts.SpeakOptions{}
	if s.stage != nil {
		s.stage.Begin(text)
		opts.OnPlayStart = func(out tts.Result) { s.stage.Segment(out) }
		opts.OnPlayEnd = s.stage.End
	}
	t1 := time.Now()
	// 播出失败必须带回来：waveOut 打不开某台设备时以前是静默"播完"，于是"试听"点了没任何信号。
	if _, err := tts.Speak(r.Context(), s.player, tcfg, text, opts); err != nil {
		res.Err = "播出失败：" + err.Error()
		jsonOut(w, http.StatusOK, res)
		return
	}
	res.SynthMs = time.Since(t0).Milliseconds()
	res.PlayMs = time.Since(t1).Milliseconds()
	res.OK = true
	jsonOut(w, http.StatusOK, res)
}

// devices 给后台填输出设备清单。设备名不许页面手写：手写的和真设备差一个括号就匹配不上。
func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) {
		return
	}
	jsonOut(w, http.StatusOK, tts.OutputDevices())
}

// Serve 阻塞起 HTTP。
func (s *Server) Serve(ctx context.Context, addr string) error {
	if addr == "" {
		return errors.New("没给监听地址")
	}
	srv := &http.Server{Addr: addr, Handler: s.mux(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		dead, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(dead)
	}()
	log.Printf("后台接口 listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	s.Register(mux)
	return mux
}
