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
	"path/filepath"
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
	"github.com/athlon18/dafeiyu/skin"
	"github.com/athlon18/dafeiyu/source"
	"github.com/athlon18/dafeiyu/stage"
	"github.com/athlon18/dafeiyu/store"
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
	// YAML 只当**出厂种子**：库空时灌一次，之后一切现场改动都在库里（后台页改的就是库）。
	// 反过来让 YAML 覆盖库，会出现"页面改了、重启又变回去"。
	db, err := store.Open(filepath.Join(cfg.DataDir, "dafeiyu.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if empty, err := db.Empty(); err != nil {
		log.Fatal(err)
	} else if empty {
		if err := db.Save(seed(cfg)); err != nil {
			log.Fatal(err)
		}
		log.Printf("库里没有配置，已按 %s 的出厂值播种", *cfgPath)
	}
	st, err := db.Load()
	if err != nil {
		log.Fatal(err)
	}
	tcfg := tts.Config{
		Kind: tts.Kind(st.Voice.Engine), BaseURL: st.Voice.BaseURL, APIKey: st.Voice.APIKey,
		Model: st.Voice.Model, Voice: st.Voice.Timbre, Speed: st.Voice.Speed, TimeoutSec: cfg.TTS.TimeoutSec,
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
		stg := stage.New(cfg.HostModel, skin.Credit(cfg.HostModel), skin.Params(cfg.HostModel))
		sp := &speaker{player: tts.NewPlayer(), cfg: tcfg, stage: stg}
		if err := server.New(db, sp.player).WithStage(stg).WithAssets(filepath.Join(cfg.DataDir, "host")).Serve(ctx, cfg.Addr); err != nil {
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
		if err := newSpeaker(st.Voice.Device, tcfg).Say(context.Background(), *sayText); err != nil {
			log.Fatal(err)
		}
		return
	}

	rl, err := rules.New(rulesFrom(st, cfg))
	if err != nil {
		log.Fatal(err)
	}
	// 后台页保存后要换一张新表，所以线上用的是一张"可替换"的表，不是固定指针。
	lr := &liveRules{rl: rl}

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

	stg := stage.New(cfg.HostModel, skin.Credit(cfg.HostModel), skin.Params(cfg.HostModel))
	sp := newSpeaker(st.Voice.Device, tcfg)
	sp.stage = stg
	if !sp.player.Available() {
		log.Printf("警告：%s", tts.NotFoundHint())
	}

	if *replayPath != "" {
		runReplay(ctx, st, cfg, *replayPath)
		return
	}

	h := newHost(st, cfg, th, sp)
	h.Start()
	defer h.Stop()

	go func() {
		srv := server.New(db, sp.player).
			WithStage(stg).
			WithAssets(filepath.Join(cfg.DataDir, "host")).
			OnChange(func(next store.Settings) { applyLive(next, cfg, lr, h) })
		if err := srv.Serve(ctx, cfg.Addr); err != nil {
			log.Printf("后台接口退出：%v", err)
		}
	}()

	src := source.New(source.Config{RoomNo: st.Conn.RoomID, AtNames: cfg.AtNames, Sink: func(ev rules.Event) {
		in, ok := lr.Get().Match(ev, time.Now())
		if !ok {
			return
		}
		h.Handle(in)
	}})
	if strings.TrimSpace(st.Conn.RoomID) == "" {
		log.Fatal("roomId 为空：边车不知道该连哪个直播间（后台页「连哪儿」里填）")
	}

	go func() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tk.C:
				if in, ok := lr.Get().NextGuide(now); ok {
					h.Handle(in)
				}
				if in, ok := lr.Get().NextIdle(now, src.LastFrame()); ok {
					h.Handle(in)
				}
			}
		}
	}()

	log.Printf("连直播间 %s 中…", st.Conn.RoomID)
	// 房间未开播/被风控**不许把整个进程带走**：后台接口和 OBS 叠层还得活着。
	// 之前这里 Start 一返回 main 就结束了，守护每 45 秒重拉一次，
	// OBS 在那个空档里连不上页面就渲染一张白色错误页——看着就像"叠层背景不透明"。
	for ctx.Err() == nil {
		c, err := live.New(live.Config{
			RoomID:  st.Conn.RoomID,
			Handler: src.Handler(),
			Cookie:  st.Conn.Cookie,
			Logger:  func(f string, a ...any) { log.Printf("[sdk] "+f, a...) },
		})
		if err != nil {
			log.Printf("建连接失败：%v", err)
			return
		}
		if err := c.Start(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("连接结束：%v；叠层与后台继续提供，%s 后重试房间", err, retryAfter)
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryAfter):
			}
			continue
		}
		return
	}
}

// retryAfter 是房间判死后的重试间隔。太短会被抖音当爬虫，太长主播开了播这边还哑着。
const retryAfter = 30 * time.Second

// applyLive 把刚存下的配置推到正在跑的边车上：换表 + 按库里状态开闭麦。
func applyLive(next store.Settings, cfg config.Config, lr *liveRules, h *host.Host) {
	rc := rulesFrom(next, cfg)
	nrl, err := rules.New(rc)
	if err != nil {
		log.Printf("新配置没生效（保留旧表）：%v", err)
		return
	}
	lr.Set(nrl)
	h.SetEnabled(!next.Muted)
	log.Printf("配置已热生效：%d 条规则，%s", len(next.Rules), map[bool]string{true: "闭麦中", false: "开口中"}[next.Muted])
}

// liveRules 一张可替换的规则表：读多写少，锁够用即可。
type liveRules struct {
	mu sync.Mutex
	rl *rules.Rules
}

func (l *liveRules) Get() *rules.Rules {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rl
}

func (l *liveRules) Set(rl *rules.Rules) {
	l.mu.Lock()
	l.rl = rl
	l.mu.Unlock()
}

func newHost(st store.Settings, cfg config.Config, th host.Thinker, sp host.Speaker) *host.Host {
	hcfg := host.DefaultConfig()
	// 闭麦状态来自库：重启把主播的开关悄悄改回"开"，比不响更糟。
	hcfg.Enabled = !st.Muted
	hcfg.RoomNo = st.Conn.RoomID
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
func rulesFrom(st store.Settings, cfg config.Config) rules.Config {
	rc := rules.Config{
		RoomNo:        st.Conn.RoomID,
		EnterCooldown: time.Duration(cfg.Gate.WelcomeGapSec) * time.Second,
		GiftCooldown:  time.Duration(cfg.Gate.CooldownSec) * time.Second,
		ChatCooldown:  time.Duration(cfg.Gate.CooldownSec) * time.Second,
		GuideEvery:    time.Duration(cfg.Gate.GuideEverySec) * time.Second,
		IdleAfter:     time.Duration(cfg.Gate.IdleAfterSec) * time.Second,
	}
	// 页面上那一列"什么时候 → 说什么"就是这张表：按行序当优先级。
	// 引导/兜底/收场这三类由行序里的 moment 认出来，其余当礼物/进场/弹幕的答谢话术。
	for i, r := range st.Rules {
		if !r.Enabled {
			continue
		}
		switch {
		case strings.Contains(r.Moment, "每"):
			rc.Guide = append(rc.Guide, r.Line)
		case strings.Contains(r.Moment, "安静"):
			rc.IdleLines = append(rc.IdleLines, r.Line)
		case strings.Contains(r.Moment, "下播"):
			rc.OfflineLines = append(rc.OfflineLines, r.Line)
		case strings.Contains(r.Moment, "进"):
			if i == 0 || rc.EnterLine == "" {
				rc.EnterLine = r.Line
			}
		default:
			rc.GiftLines = append(rc.GiftLines, rules.Line{Match: giftKey(r.Moment), Lines: []string{r.Line}})
		}
	}
	return rc
}

// giftKey 从"有人送嘉年华"这类中文行名里取出礼物名。取不出来就返回空串——
// 空 Match 在 rules 里等于不命中，宁可这条哑掉，也不要拿整句中文去当匹配串。
func giftKey(moment string) string {
	i := strings.Index(moment, "送")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(moment[i+1:])
}

// seed 把 YAML 出厂值灌进库（只在库空时调用一次）。
func seed(cfg config.Config) store.Settings {
	st := store.Settings{
		Conn: store.Conn{RoomID: cfg.RoomID, Cookie: cfg.Cookie},
		Voice: store.Voice{Engine: cfg.TTS.Kind, BaseURL: cfg.TTS.BaseURL, APIKey: cfg.TTS.APIKey,
			Model: cfg.TTS.Model, Timbre: cfg.TTS.Voice, Speed: cfg.TTS.Speed, Device: cfg.TTS.DeviceName},
	}
	if cfg.EnterLine != "" {
		st.Rules = append(st.Rules, store.Rule{Moment: "有人进直播间", Line: cfg.EnterLine, Enabled: true})
	}
	for _, l := range cfg.GiftLines {
		for _, line := range l.Lines {
			st.Rules = append(st.Rules, store.Rule{Moment: "有人送" + l.Match, Line: line, Enabled: true})
		}
	}
	for _, g := range cfg.GuideLines {
		st.Rules = append(st.Rules, store.Rule{Moment: "没人说话时每 3 分钟", Line: g, Enabled: true})
	}
	for _, i := range cfg.IdleLines {
		st.Rules = append(st.Rules, store.Rule{Moment: "安静满 1 分钟", Line: i, Enabled: true})
	}
	for _, o := range cfg.OfflineLines {
		st.Rules = append(st.Rules, store.Rule{Moment: "主播下播", Line: o, Enabled: true})
	}
	return st
}

// runReplay 用同一套 rules，把帧文件打在虚拟钟上跑一遍。
// 目的只有一个：在没网没声卡的机器上证明"冷场时她会开口"，并把整条链走到播出那一步。
//
// 刻意不复用线上那个 host：一是回放不该抢主播的声卡，二是全局间隔在虚拟钟下会
// 把绝大多数句子挡掉（一场虚拟 7 分钟只放得进十几句），那样"收下几句"就不是链路结论了。
func runReplay(ctx context.Context, st store.Settings, cfg config.Config, path string) {
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

	// 回放的表 = 线上那张表（库里现在存的），不是 YAML：验收必须打在真配置上。
	rc := rulesFrom(st, cfg)
	res := replay.Run(ctx, replay.Session{
		Frames:       frames,
		Step:         time.Millisecond,
		GuideEvery:   rc.GuideEvery,
		IdleAfter:    rc.IdleAfter,
		GuideLines:   rc.Guide,
		IdleLines:    rc.IdleLines,
		OfflineLines: rc.OfflineLines,
		EnterLine:    rc.EnterLine,
		GiftLines:    rc.GiftLines,
		Sink:         hostFeeder(h, rc),
	})
	time.Sleep(300 * time.Millisecond) // 让编排器把队列里剩下的句子走完
	stats := h.Stats()
	log.Printf("回放结束 递到编排器=%d 分布=%v 最后观众帧=%s",
		res.Emitted, res.ByKind, res.LastFrame.UTC().Format("15:04:05"))
	log.Printf("编排器：收下=%v 播出=%v 被规则挡=%v 队列满丢=%v 静音出口收到=%d 句",
		stats["queued"], stats["said"], stats["skipped"], stats["dropped"], sp.count())
	if n, _ := stats["said"].(int64); n == 0 {
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
	stage  *stage.Stage // nil=不登记口型（回放/纯合成自检）
}

func newSpeaker(device string, tcfg tts.Config) *speaker {
	p := tts.NewPlayer()
	// 选了设备就必须走认设备的原生通道：装了 ffplay 时它只认系统默认设备，
	// 声音会悄悄跑到别的轨上，这是主仓踩过的坑。
	if dev := strings.TrimSpace(device); dev != "" {
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
	// 口型包络登记在"每段真正出声前"：合成那两秒不该提前张嘴，被打断也要成对收尾。
	opts := tts.SpeakOptions{}
	if s.stage != nil {
		s.stage.Begin(text)
		opts.OnPlayStart = func(res tts.Result) { s.stage.Segment(res) }
		opts.OnPlayEnd = s.stage.End
	}
	_, err := tts.Speak(ctx, s.player, s.cfg, text, opts)
	return err
}
