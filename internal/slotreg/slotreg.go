// Package slotreg 让 dafeiyu 作为【外部插槽】向 douyinlive 基座自注册——
// 只心跳供管理面观测（capability=daemon.observe），不占 /api 前缀、不进转发快照。
//
// 为什么不在 douyinlive 主仓里代登：AGENTS 的灵魂原则是「凡运行的必注册」，注册方必须是那个
// 真在跑的进程自己，而不是内核替它编一份清单——否则 dafeiyu 掉没掉线、跑的是哪版码，基座无从核对。
// 参照 cmd/noderunner 的同款形态：独立二进制，开机反向 POST /internal/module/register。
//
// 依赖铁律：本包只 import 标准库。dafeiyu 是独立模块 github.com/athlon18/dafeiyu，
// 不能 import douyinlive 的 internal/slotclient（Go 的 internal 规则跨模块封死），
// 故这里复刻它那一小段线上协议——字段 JSON 标签逐字对齐 contract.ModuleManifest / SlotHeartbeat / SlotDeregister，
// 一旦基座改了这些标签，本包按同一形状跟着改即可（契约只加不改，兼容窗口内旧客户端照常受理）。
package slotreg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ContractVersion 与内核 internal/contract 的 SlotContractVersion 对齐。高于内核会被拒，
// 这里钉 1（内核现值也是 1）；内核升到 2 时同步抬这一格。
const ContractVersion = 1

// defaultHeartbeat 心跳周期，须显著小于内核存活地板（model.ModuleHeartbeatStale=30s）。
// 与 slotclient 同取 10s：偶发一次失败也不至于被误判为掉线。内部回环心跳不是出网请求。
const defaultHeartbeat = 10 * time.Second

// ErrNeedRegister 表示内核不认识本 moduleId（多半是内核重启丢了活态）。
// Heartbeat 用它做哨兵，Run 收到就重发清单。
var ErrNeedRegister = errors.New("dafeiyu slot: 内核未登记本模块，需重发清单")

// Manifest 是上报给内核的自描述清单，字段取 contract.ModuleManifest 的【子集】。
// 只发这份边车申报得起的几项：没有 apiPrefix/slots/secrets/provenance，故一律不填
// （omitempty 保证这些字段在 JSON 里干脆缺席，与"未申报"语义一致）。
type Manifest struct {
	ModuleID        string   `json:"moduleId"`
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	BaseURL         string   `json:"baseUrl,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty"`
	ContractVersion int      `json:"contractVersion,omitempty"`
	Provider        string   `json:"provider,omitempty"`
	Description     string   `json:"description,omitempty"`
	Group           string   `json:"group,omitempty"`
}

// Config 描述一次插槽注册会话所需的全部外部信息。
type Config struct {
	// APIBase 基座（api 角色）可达基址，如 http://127.0.0.1:9599；不带尾斜杠。
	APIBase string
	// Token 内部口令，须与 api 的 internalToken 一致；空串则不发该头（本机单用户场景）。
	Token string
	// Manifest 自描述清单（含 moduleId / capabilities / group 等）。
	Manifest Manifest
	// HeartbeatEvery 心跳周期；<=0 用 defaultHeartbeat。
	HeartbeatEvery time.Duration
	// HTTP 自定义客户端；nil 用带 8s 超时的默认客户端（都是内网短请求）。
	HTTP *http.Client
	// Logf 事件钩子（注册/心跳/注销）；nil 则静默。
	Logf func(format string, args ...any)
}

// Client 是本进程对基座的一次注册会话。非并发安全：一个进程一个，Run 在单 goroutine 里转。
type Client struct {
	cfg  Config
	http *http.Client
	logf func(string, ...any)
}

// New 校验最小必填项并造客户端：APIBase 与 Manifest.ModuleID 任一为空都当场报错，
// 免得「看着跑起来了、其实基座注册中心永远收不下它」这种哑故障留到运行期。
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.APIBase) == "" {
		return nil, errors.New("slotreg: APIBase 不能为空")
	}
	if strings.TrimSpace(cfg.Manifest.ModuleID) == "" {
		return nil, errors.New("slotreg: manifest.moduleId 不能为空")
	}
	hc := cfg.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 8 * time.Second}
	}
	logf := cfg.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	period := cfg.HeartbeatEvery
	if period <= 0 {
		period = defaultHeartbeat
	}
	return &Client{cfg: cfg, http: hc, logf: logf}, nil
}

// post 发一个带内部口令的 JSON 请求，返回 HTTP 状态码与已解码的信封体（{Code,Data}）。
// body 为 nil 时发空对象。出错信息里保留状态码但绝不带口令串。
func (c *Client) post(ctx context.Context, path string, body any) (int, json.RawMessage, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.cfg.APIBase, "/")+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tk := strings.TrimSpace(c.cfg.Token); tk != "" {
		req.Header.Set("X-Internal-Token", tk)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("请求 %s 失败: %w", path, err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var env struct {
		Code int             `json:"Code"`
		Data json.RawMessage `json:"Data"`
	}
	_ = json.Unmarshal(data, &env)
	return resp.StatusCode, env.Data, nil
}

// rejectedError 是注册被内核协商拒绝（HTTP 409）的结构化原因，把 reason 与逐条 errors 端出来。
type rejectedError struct {
	ModuleID string
	Status   string
	Reason   string
	Errors   []string
}

func (e *rejectedError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "dafeiyu 注册被拒（%s）：%s", e.Status, e.Reason)
	for _, s := range e.Errors {
		fmt.Fprintf(&b, "\n  - %s", s)
	}
	return b.String()
}

// Register 向基座发清单、跑一次协商。409→*rejectedError；其它非 200→error。
// 本进程无验签私钥（不申请强验），以未签名清单注册——基座走口令准入路径 A，与 slotclient 无种子时同口径。
func (c *Client) Register(ctx context.Context) error {
	status, data, err := c.post(ctx, "/internal/module/register", c.cfg.Manifest)
	if err != nil {
		return err
	}
	if status == http.StatusConflict {
		rej := &rejectedError{ModuleID: c.cfg.Manifest.ModuleID, Status: "rejected"}
		var d struct {
			Status string   `json:"status"`
			Reason string   `json:"reason"`
			Errors []string `json:"errors"`
		}
		_ = json.Unmarshal(data, &d)
		if d.Status != "" {
			rej.Status = d.Status
		}
		rej.Reason, rej.Errors = d.Reason, d.Errors
		if rej.Reason == "" {
			rej.Reason = "内核拒绝注册"
		}
		return rej
	}
	if status != http.StatusOK {
		return fmt.Errorf("注册返回 %d: %s", status, trim(string(data)))
	}
	c.logf("[slot] 已注册 moduleId=%s 边车=daemon.observe（只供基座观测，不占前缀）", c.cfg.Manifest.ModuleID)
	return nil
}

// heartbeatBody 只报 moduleId（本进程起了 :8420 HTTP，基座读时探它比自报更新鲜，故不填 runtime 快照）。
func (c *Client) heartbeatBody() map[string]string {
	return map[string]string{"moduleId": c.cfg.Manifest.ModuleID}
}

// Heartbeat 报一次活。基座不认识本 moduleId 时返回 ErrNeedRegister（调用方据此重注册）。
func (c *Client) Heartbeat(ctx context.Context) error {
	status, data, err := c.post(ctx, "/internal/module/heartbeat", c.heartbeatBody())
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrNeedRegister
	default:
		return fmt.Errorf("心跳返回 %d: %s", status, trim(string(data)))
	}
}

// Deregister 主动注销（干净下线，落墓碑可复活）；只有正常退出走到这里，掉线由基座心跳超时兜底降级。
func (c *Client) Deregister(ctx context.Context) error {
	status, data, err := c.post(ctx, "/internal/module/deregister", map[string]string{"moduleId": c.cfg.Manifest.ModuleID})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("注销返回 %d: %s", status, trim(string(data)))
	}
	c.logf("[slot] 已注销 moduleId=%s", c.cfg.Manifest.ModuleID)
	return nil
}

// Run 是注册主循环：注册 → 周期心跳（遇基座丢失就重注册）→ ctx 取消时干净注销。
//
//   - 启动注册失败（协商被拒 / 连不上）直接返回错误：让进程当场报错而不是「假装在被人看着」。
//   - 运行期心跳失败不终止进程：api 短暂重启/网络抖一下是常态，基座侧靠 30s 超时自然降级、恢复即回升。
//   - ctx 取消（正常退出 / SIGTERM）用脱离 ctx 的短超时补一次注销，别把退出卡住。
func (c *Client) Run(ctx context.Context) error {
	if err := c.Register(ctx); err != nil {
		return err
	}
	period := c.cfg.HeartbeatEvery
	if period <= 0 {
		period = defaultHeartbeat
	}
	tick := time.NewTicker(period)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			c.deregisterQuietly()
			return ctx.Err()
		case <-tick.C:
			err := c.Heartbeat(ctx)
			if errors.Is(err, ErrNeedRegister) {
				c.logf("[slot] 基座不认识我（多半重启过），重发清单")
				if rerr := c.Register(ctx); rerr != nil {
					c.logf("[slot] 重注册失败（下个周期继续试）: %v", rerr)
				}
				continue
			}
			if err != nil {
				c.logf("[slot] 心跳失败（不终止，下周期重试）: %v", err)
			}
		}
	}
}

func (c *Client) deregisterQuietly() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Deregister(ctx); err != nil {
		c.logf("[slot] 退出注销失败（将由基座心跳超时兜底降级）: %v", err)
	}
}

// trim 出错信息里只带一小段响应体，免得整页 HTML 冲进日志。
func trim(s string) string {
	if len(s) > 120 {
		return s[:120]
	}
	return s
}
