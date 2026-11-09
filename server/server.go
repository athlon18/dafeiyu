// Package server 第一刀只要一件事：让后台页上的"试听一句"真能出声。
// 第二刀再挂配置读写；这里不发明结构，只开 /tts/preview 与 /tts/devices 两个口。
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/athlon18/dafeiyu/config"
	"github.com/athlon18/dafeiyu/tts"
)

type Server struct {
	cfg    config.Config
	player *tts.Player
}

func New(cfg config.Config, player *tts.Player) *Server { return &Server{cfg: cfg, player: player} }

// Register 只登记这一刀已有的两个口。
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("/tts/preview", s.preview)
	mux.HandleFunc("/tts/devices", s.devices)
}

// cors 放开本机跨源：后台页可能由静态服务（8421）打开，接口在 8420。
// 只接受来自 127.0.0.1/localhost 的 Origin，其他一律 403——这是给主播机用的口，不该对全网开。
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	return true
}

type previewResp struct {
	OK      bool   `json:"ok"`
	SynthMs int64  `json:"synthMs"`
	PlayMs  int64  `json:"playMs"`
	Device  string `json:"device"`
	Err     string `json:"err"`
}

// preview 合成并播出。**播出失败必须带回来**：waveOut 打不开某台设备时以前是静默"播完"，
// 于是"试听"点了什么信号都没有，用户只能猜是没声音还是没点着。
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) { return }
	text := strings.TrimSpace(r.URL.Query().Get("text"))
	if text == "" {
		text = "这是一句试听"
	}
	q := r.URL.Query()
	dev := strings.TrimSpace(q.Get("device"))
	voice := strings.TrimSpace(q.Get("voice"))
	res := previewResp{Device: "系统默认"}
	tcfg := s.ttsConfig()
	if voice != "" {
		tcfg.Voice = voice
	}
	t0 := time.Now()
	out, err := tts.Synthesize(r.Context(), tcfg, text)
	if err != nil {
		res.Err = "合成失败：" + err.Error()
		s.write(w, res)
		return
	}
	res.SynthMs = time.Since(t0).Milliseconds()
	if dev != "" {
		d, ok := tts.RouteDevice(dev)
		if !ok {
			res.Err = fmt.Sprintf("没匹配到输出设备 %q（用 /tts/devices 看可选清单）", dev)
			s.write(w, res)
			return
		}
		tts.ApplyRoute(s.player)
		res.Device = d.Name
	}
	t1 := time.Now()
	if err := s.player.Play(r.Context(), out.Audio, out.MIME); err != nil {
		res.Err = "播出失败：" + err.Error()
		s.write(w, res)
		return
	}
	res.PlayMs = time.Since(t1).Milliseconds()
	res.OK = true
	s.write(w, res)
}

// devices 给后台页填输出设备清单。
func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	if !s.cors(w, r) { return }
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(tts.OutputDevices())
}

func (s *Server) ttsConfig() tts.Config {
	c := s.cfg.TTS
	return tts.Config{Kind: tts.Kind(c.Kind), BaseURL: c.BaseURL, APIKey: c.APIKey,
		Model: c.Model, Voice: c.Voice, Speed: c.Speed, TimeoutSec: c.TimeoutSec}
}

func (s *Server) write(w http.ResponseWriter, res previewResp) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if res.Err != "" {
		log.Printf("试听失败：%s", res.Err)
	}
	_ = json.NewEncoder(w).Encode(res)
}

// Serve 阻塞起 HTTP。addr 空则用配置里的。
func (s *Server) Serve(ctx context.Context, addr string) error {
	if addr == "" {
		addr = s.cfg.Addr
	}
	srv := &http.Server{Addr: addr, Handler: s.mux(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		dead, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(dead)
	}()
	log.Printf("后台接口 listening on %s", addr)
	return srv.ListenAndServe()
}

func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	s.Register(mux)
	return mux
}
