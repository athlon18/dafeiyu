// Command dafeiyu 是抖音直播间语音播报边车：收帧 → 规则 → 语音 → 本机声卡。
// 本文件只做装配，不放业务判断——业务在 rules / host / brain / guard / tts 各自包里。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	live "github.com/athlon18/douyin-live-client/live"

	"github.com/athlon18/dafeiyu/brain"
	"github.com/athlon18/dafeiyu/config"
	"github.com/athlon18/dafeiyu/guard"
	"github.com/athlon18/dafeiyu/host"
	"github.com/athlon18/dafeiyu/replay"
	"github.com/athlon18/dafeiyu/rules"
	"github.com/athlon18/dafeiyu/server"
	"github.com/athlon18/dafeiyu/source"
	"github.com/athlon18/dafeiyu/tts"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "配置文件路径")
	replayPath := flag.String("replay", "", "回放 JSONL 帧序列：不开房间、不出声，只验规则会不会开口")
	listDev := flag.Bool("list-devices", false, "列出本机音频输出设备")
	sayText := flag.String("say", "", "试听一句话后退出")
	quiet := flag.Bool("quiet", false, "只合成不出声，用来验 TTS 链路通不通")
	serve := flag.Bool("serve", false, "起后台接口（设计图上的试听要用它）")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	tcfg := tts.Config{
		Kind: tts.Kind(cfg.TTS.Kind), BaseURL: cfg.TTS.BaseURL, APIKey: cfg.TTS.APIKey,
		Model: cfg.TTS.Model, Voice: cfg.TTS.Voice, Speed: cfg.TTS.Speed, TimeoutSec: cfg.TTS.TimeoutSec,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *listDev {
		devs := tts.OutputDevices()
		if len(devs) == 0 {
			fmt.Println("没枚举到任何输出设备")
			return
		}
		for _, d := range devs {
			fmt.Printf("%d\t%s\n", d.ID, d.Name)
		}
		return
	}

	if *serve {
		if err := server.New(cfg, newSpeaker(cfg, tcfg).player).Serve(ctx, cfg.Addr); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *sayText != "" {
		if *quiet {
			if _, err := tts.Synthesize(context.Background(), tcfg, *sayText); err != nil {
				log.Fatal(err)
			}
			fmt.Println("合成成功（未出声）")
			return
		}
		if err := newSpeaker(cfg, tcfg).Say(context.Background(), *sayText); err != nil {
			log.Fatal(err)
		}
		return
	}

	rl, err := rules.New(rulesFrom(cfg))
	if err != nil {
		log.Fatal(err)
	}

	var th host.Thinker
	if b := brain.New(brain.Config{
		APIKey: cfg.LLM.APIKey, BaseURL: cfg.LLM.BaseURL, Model: cfg.LLM.Model,
		Persona: cfg.LLM.Persona, TimeoutSec: cfg.Gate.LLMTimeout,
	}); b != nil {
		th = b
		log.Printf("LLM 已接入 model=%s", cfg.LLM.Model)
	} else {
		log.Printf("未配 LLM：弹幕回话这一段会安静，其余靠配好的话术")
	}

	sp := newSpeaker(cfg, tcfg)
	if !sp.player.Available() {
		log.Printf("警告：%s", tts.NotFoundHint())
	}

	if *replayPath != "" {
		runReplay(ctx, cfg, *replayPath)
		return
	}

	h := newHost(cfg, th, sp)
	h.Start()
	defer h.Stop()

	src := source.New(source.Config{RoomNo: cfg.RoomID, AtNames: cfg.AtNames, Sink: func(ev rules.Event) {
		in, ok := rl.Match(ev, time.Now())
		if !ok {
			return
		}
		h.Handle(in)
	}})
	if strings.TrimSpace(cfg.RoomID) == "" {
		log.Fatal("config.yaml 的 roomId 为空：边车不知道该连哪个直播间")
	}

	go func() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tk.C:
				if in, ok := rl.NextGuide(now); ok {
					h.Handle(in)
				}
				if in, ok := rl.NextIdle(now, src.LastFrame()); ok {
					h.Handle(in)
				}
			}
		}
	}()

	log.Printf("连直播间 %s 中…", cfg.RoomID)
	c, err := live.New(live.Config{
		RoomID:  cfg.RoomID,
		Handler: src.Handler(),
		Cookie:  cfg.Cookie,
		Logger:  func(f string, a ...any) { log.Printf("[sdk] "+f, a...) },
	})
	if err != nil {
		log.Fatal(err)
	}
	if err := c.Start(ctx); err != nil {
		log.Printf("连接结束：%v", err)
	}
}

func newHost(cfg config.Config, th host.Thinker, sp host.Speaker) *host.Host {
	hcfg := host.DefaultConfig()
	hcfg.Enabled = true
	hcfg.RoomNo = cfg.RoomID
	hcfg.CooldownSec = cfg.Gate.CooldownSec
	hcfg.GlobalGapSec = cfg.Gate.GlobalGapSec
	hcfg.QueueSize = cfg.Gate.QueueSize
	hcfg.MaxSayRunes = cfg.Gate.MaxSayRunes
	hcfg.LLMTimeout = cfg.Gate.LLMTimeout
	hcfg.HistoryTurns = cfg.Gate.HistoryTurns
	hcfg.Persona = cfg.LLM.Persona
	// 进场/暖场的话术都由 rules 渲好再递进来，编排器那两个开关只负责"是否允许这类事件开口"。
	hcfg.WelcomeEnabled = cfg.EnterLine != ""
	hcfg.WelcomeGapSec = cfg.Gate.WelcomeGapSec
	hcfg.IdleTalkEnabled = false // 冷场兜底由 rules 驱动，不另开一条会重复出声的路
	return host.New(hcfg, th, guard.New(cfg.GuardWords), sp)
}

// rulesFrom 把 YAML 的闸门秒数换成 rules 的时长口径。
// 进场冷却沿用 welcomeGapSec：同一个人在两场配置里不该有两个"多久再欢迎一次"。
func rulesFrom(cfg config.Config) rules.Config {
	rc := rules.Config{
		RoomNo:        cfg.RoomID,
		EnterCooldown: time.Duration(cfg.Gate.WelcomeGapSec) * time.Second,
		GiftCooldown:  time.Duration(cfg.Gate.CooldownSec) * time.Second,
		ChatCooldown:  time.Duration(cfg.Gate.CooldownSec) * time.Second,
		EnterLine:     cfg.EnterLine,
		Guide:         cfg.GuideLines,
		GuideEvery:    time.Duration(cfg.Gate.GuideEverySec) * time.Second,
		IdleLines:     cfg.IdleLines,
		IdleAfter:     time.Duration(cfg.Gate.IdleAfterSec) * time.Second,
		OfflineLines:  cfg.OfflineLines,
	}
	for _, l := range cfg.GiftLines {
		rc.GiftLines = append(rc.GiftLines, rules.Line{Match: l.Match, Stage: l.Stage, Lines: l.Lines})
	}
	return rc
}

// runReplay 用同一套 rules，把帧文件打在虚拟钟上跑一遍。
// 目的只有一个：在没网没声卡的机器上证明"冷场时她会开口"，并把整条链走到播出那一步。
//
// 刻意不复用线上那个 host：一是回放不该抢主播的声卡，二是全局间隔在虚拟钟下会
// 把绝大多数句子挡掉（一场虚拟 7 分钟只放得进十几句），那样"收下几句"就不是链路结论了。
func runReplay(ctx context.Context, cfg config.Config, path string) {
	frames, err := replay.Load(path)
	if err != nil {
		log.Fatal(err)
	}
	sp := &recordingSpeaker{}
	hcfg := host.DefaultConfig()
	hcfg.Enabled = true
	hcfg.RoomNo = "replay"
	hcfg.GlobalGapSec = 0
	hcfg.QueueSize = 8
	h := host.New(hcfg, nil, nil, sp)
	h.Start()
	defer h.Stop()

	res := replay.Run(ctx, replay.Session{
		Frames:       frames,
		Step:         time.Millisecond,
		GuideEvery:   time.Duration(cfg.Gate.GuideEverySec) * time.Second,
		IdleAfter:    time.Duration(cfg.Gate.IdleAfterSec) * time.Second,
		GuideLines:   cfg.GuideLines,
		IdleLines:    cfg.IdleLines,
		OfflineLines: cfg.OfflineLines,
		EnterLine:    cfg.EnterLine,
		Sink:         hostFeeder(h, rulesFrom(cfg)),
	})
	time.Sleep(300 * time.Millisecond) // 让编排器把队列里剩下的句子走完
	st := h.Stats()
	log.Printf("回放结束 递到编排器=%d 分布=%v 最后观众帧=%s",
		res.Emitted, res.ByKind, res.LastFrame.UTC().Format("15:04:05"))
	log.Printf("编排器：收下=%v 播出=%v 被规则挡=%v 队列满丢=%v 静音出口收到=%d 句",
		st["queued"], st["said"], st["skipped"], st["dropped"], sp.count())
	if n, _ := st["said"].(int64); n == 0 {
		log.Printf("结论：一句都没播出去 —— 这不是交付，是静音贴纸")
	}
}

// hostFeeder 把回放引擎交出的事件喂给编排器。引导/兜底/收场本身没有观众身份，
// 走 Line；观众事件由 rules 判过之后才到这儿，这里不再判第二遍。
func hostFeeder(h *host.Host, rc rules.Config) func(rules.Event) {
	rc.RoomNo = "replay"
	rl, err := rules.New(rc)
	if err != nil {
		log.Printf("回放用的规则表配歪了：%v", err)
		return func(rules.Event) {}
	}
	return func(ev rules.Event) {
		switch ev.Kind {
		case rules.KindGuide, rules.KindOffline, rules.KindIdle:
			h.Handle(host.ChatInput{RoomNo: "replay", Line: ev.Content, Idle: ev.Kind == rules.KindIdle})
		default:
			if in, ok := rl.Match(ev, time.Now()); ok {
				h.Handle(in)
			}
		}
	}
}

// recordingSpeaker 收句不出声，用来把"编排器到底说了几句"变成可数的东西。
type recordingSpeaker struct {
	mu   sync.Mutex
	said []string
}

func (s *recordingSpeaker) Say(_ context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.said = append(s.said, text)
	return nil
}

func (s *recordingSpeaker) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.said)
}

// speaker 把 tts 包适配成 host.Speaker。
type speaker struct {
	player *tts.Player
	cfg    tts.Config
}

func newSpeaker(cfg config.Config, tcfg tts.Config) *speaker {
	p := tts.NewPlayer()
	// 选了设备就必须走认设备的原生通道：装了 ffplay 时它只认系统默认设备，
	// 声音会悄悄跑到别的轨上，这是主仓踩过的坑。
	if dev := strings.TrimSpace(cfg.TTS.DeviceName); dev != "" {
		if d, ok := tts.RouteDevice(dev); ok {
			tts.ApplyRoute(p)
			log.Printf("输出设备=%s", d.Name)
		} else {
			log.Printf("警告：没匹配到输出设备 %q，改用默认出口（OBS 可能抓不到）", dev)
		}
	}
	return &speaker{player: p, cfg: tcfg}
}

func (s *speaker) Say(ctx context.Context, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if !s.player.Available() {
		return fmt.Errorf("%s", tts.NotFoundHint())
	}
	_, err := tts.Speak(ctx, s.player, s.cfg, text, tts.SpeakOptions{})
	return err
}
