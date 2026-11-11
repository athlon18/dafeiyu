// Package config 边车的进程级配置。第一刀只从 YAML 读，第二刀换成 SQLite 后本包只留端口/token/路径。
package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	RoomID     string   `yaml:"roomId"`
	Cookie     string   `yaml:"cookie"` // 留空=游客 ttwid
	Addr       string   `yaml:"addr"`
	DataDir    string   `yaml:"dataDir"`
	HostModel  string   `yaml:"hostModel"` // 画面上那张皮；授权口径见 skin 包
	AtNames    []string `yaml:"atNames"`    // 主播昵称别名，判 @我 用
	GuardWords []string `yaml:"guardWords"` // 本地词表闸

	LLM        LLM    `yaml:"llm"`
	TTS        TTS    `yaml:"tts"`
	Gate       Gate   `yaml:"gate"`
	GiftLines  []Rule `yaml:"giftLines"`
	EnterLine  string `yaml:"enterLine"`
	IdleLines  []string `yaml:"idleLines"`
	GuideLines []string `yaml:"guideLines"`
	OfflineLines []string `yaml:"offlineLines"`
	Register   Register `yaml:"register"` // 作为外部插槽自注册进基座（见 slotreg）
}

type LLM struct {
	APIKey  string `yaml:"apiKey"`
	BaseURL string `yaml:"baseUrl"`
	Model   string `yaml:"model"`
	Persona string `yaml:"persona"` // 人设 prompt，只配不改写口径
}

type TTS struct {
	Kind       string  `yaml:"kind"` // openai / edge / sapi
	BaseURL    string  `yaml:"baseUrl"`
	APIKey     string  `yaml:"apiKey"`
	Model      string  `yaml:"model"`
	Voice      string  `yaml:"voice"`
	Speed      float64 `yaml:"speed"`
	TimeoutSec int     `yaml:"timeoutSec"`
	DeviceName string  `yaml:"deviceName"` // 非空=强制本机设备路由，放弃 ffplay
}

// Gate 闸门参数。默认值照搬 douyinlive/modules/ai/host/host.go:66 DefaultConfig()，不许自创；
// 只有引导/冷场两个周期是新的（主仓没有这两段，本仓靠它们保证冷场有声）。
type Gate struct {
	CooldownSec   int `yaml:"cooldownSec"`
	GlobalGapSec  int `yaml:"globalGapSec"`
	QueueSize     int `yaml:"queueSize"`
	MaxSayRunes   int `yaml:"maxSayRunes"`
	LLMTimeout    int `yaml:"llmTimeout"`
	HistoryTurns  int `yaml:"historyTurns"`
	WelcomeGapSec int `yaml:"welcomeGapSec"`
	IdleGapSec    int `yaml:"idleGapSec"`
	GuideEverySec int `yaml:"guideEverySec"`
	IdleAfterSec  int `yaml:"idleAfterSec"`
}

type Rule struct {
	Match string   `yaml:"match"` // 礼物名关键词，包含匹配
	Stage uint8    `yaml:"stage"` // 0=任意连击段；1~4=只对这一档及以上
	Lines []string `yaml:"lines"`
}

// Register 把本边车作为【外部插槽】自注册进 douyinlive 基座：capability=daemon.observe，
// 只心跳供管理面观测（在线/降级），不占 /api 前缀、不进转发快照。Enabled=false 就完全不出门——
// 与"不接基座也能独立跑语音链路"同口径（注册失败也只记日志、不带走播报）。
type Register struct {
	Enabled     bool   `yaml:"enabled"`
	APIBase     string `yaml:"apiBase"`  // 基座 api 可达地址，如 http://127.0.0.1:9599
	Token       string `yaml:"token"`    // 内部口令；留空则回落环境变量 DY_INTERNAL_TOKEN
	ModuleID    string `yaml:"moduleID"` // 上报的 moduleId
	Name        string `yaml:"name"`
	Version     string `yaml:"version"`
	Group       string `yaml:"group"`    // 基座出厂拖线板名，须已登记（先板后槽：报未登记的组会 group_not_registered 被拒）
	BaseURL     string `yaml:"baseUrl"`  // 本边车对外地址，供基座读时探活；留空由 addr 推
	Provider    string `yaml:"provider"` // 归属标签（管理面「归属地图」分组用）
	Description string `yaml:"description"`
}

// DefaultOfflineLines 下播收场话术。刻意不叫"告别语"——它是收场，不是催关注。
var DefaultOfflineLines = []string{
	"今天就到这里，先下了",
	"这轮结束了，下次接着玩",
}

func Default() Config {
	return Config{
		Addr:         ":8420",
		DataDir:      "data",
		HostModel:    "大肥鱼",
		EnterLine:    "欢迎 {user} 进入直播间",
		OfflineLines: append([]string(nil), DefaultOfflineLines...),
		LLM: LLM{BaseURL: "https://api.openai.com/v1", Model: "gpt-4o-mini"},
		TTS: TTS{
			Kind: "openai", BaseURL: "http://127.0.0.1:9880/v1",
			Model: "tts-1", Voice: "zh_CN-huayan-medium", Speed: 1.0, TimeoutSec: 30,
		},
		Gate: Gate{
			CooldownSec: 30, GlobalGapSec: 6, QueueSize: 2, MaxSayRunes: 60,
			LLMTimeout: 20, HistoryTurns: 3, WelcomeGapSec: 120,
			IdleGapSec: 90, GuideEverySec: 180, IdleAfterSec: 60,
		},
		Register: Register{
			Enabled: true, APIBase: "http://127.0.0.1:9599", ModuleID: "dafeiyu",
			Name: "大肥鱼语音播报边车", Version: "1.0.0", Group: "voice", Provider: "github",
			Description: "抖音直播间语音播报边车：收帧→规则→合成→本机声卡；只心跳供基座观测，不占前缀",
		},
	}
}

func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("读配置 %s: %w", path, err)
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	return cfg, nil
}
