# 大肥鱼边车（dafeiyu）设计

把抖音直播间的实时事件变成"人在说话 + 人在动"，专门服务小玩法直播。独立仓库、独立进程、独立后台配置，
与 `douyinlive` 主仓零运行时耦合。输入端复用已抽出的纯传输 SDK `github.com/athlon18/douyin-live-client`。

拍定的四个前提（不再讨论）：事件源只吃直播帧（不接官方互动玩法协议）；代码从 `douyinlive` 蒸馏而非另起炉灶；
配置落 SQLite 单文件；文案两条腿——模板兜底 + 可选 LLM。

## 进程拓扑

```
抖音直播间 --WS--> [SDK douyin-live-client] --typed event--> 规则/闸门/队列 --> 文案 --> TTS --> 本机虚拟声卡 --> OBS
                                                                        |
                                                   HTTP :8420  ├─ 后台配置页（规则/声音/闸门/互动/点歌）
                                                               ├─ /overlay  大肥鱼 Live2D 浏览器源
                                                               └─ /state    叠层轮询（口型包络/字幕/事件流）
```

- **声音只走本机声卡**，不对外提供 TTS HTTP 接口。OBS 抓虚拟音频线（VB-Cable / VoiceMeeter）。
  沿用主仓已验证的播放栈，不做改动。
- **画面靠 OBS 浏览器源**指到 `http://127.0.0.1:8420/overlay`。叠层页不再塞成 Go 里的内联 HTML 常量，
  改成 `go:embed` 的真实文件（html/css/js 分文件），一块一块单独改。
- **叠层页整页必须透明**：`html, body { background: transparent }`，形象、字幕、署名都不带底色。
  任何一层底色在 OBS 里都会变成角色背后一块板。后台预览用棋盘格表示"此处透明"，棋盘格只属于后台页面。
- 后台接口要 token；叠层走 `?token=`（主仓验过的做法）。

## 包结构与搬迁边界

```
D:\go\dafeiyu\
├── cmd\dafeiyu\main.go        # 只装配，不写业务
├── host\                      # 搬：播报编排（队列/冷却/优先级）——依赖只有 ctx/log/sync/time + tts
├── tts\                       # 搬：合成 + 声卡播放——主仓里已核实只被 host 用，是它的专属包
├── rules\                     # 新写：事件 → 命中哪条播报动作（后台可编辑的就是这张表）
├── source\                    # 新写：SDK typed event → rules 输入
├── store\                     # 新写：SQLite（规则 / 声音 / 闸门 / 点歌队列）
├── brain\                     # 新写：模板兜底 + 可选 LLM；无 key 时走模板
├── guard\                     # 新写：开口与发评论共用的红线闸
├── server\                    # 新写：后台 API + 叠层托管 + /state
├── web\overlay\               # 新写：大肥鱼 Live2D 页
├── web\admin\                 # 新写：配置页
├── data\host\model\大肥鱼\     # 皮，从主仓拷；不入库（授权口径见下）
├── tools\tts-server\          # 搬：Piper 9880 / Kokoro 9884 / MiniMax 9883 三个 OpenAI 兼容网关
└── config.yaml                # 只放端口 / token / 房间号 / 数据目录
```

依赖单向：`source → rules → host → {brain, tts}`；`store` 被 `server` 与 `rules` 读；`host` 不反向依赖 `server`。

**搬**：`modules/ai/host`（660 行）、`modules/ai/tts`（1582 行）、`tools/tts-server`。

**重写**（主仓里这些是焊在别的业务上的胶水，搬过来等于把整个 monorepo 拖进来）：

| 边界 | 主仓现状 | 新仓做法 |
|---|---|---|
| 帧入口 | `service/host_bridge.go` 从 `Hub.Subscribe()` 读 `contract.DyMessage`（几十列宽 JSON） | SDK typed 事件直接映射成 rules 输入，不带 `DyMessage`/`Hub`/`SnapshotRooms` |
| 存储 | MySQL 单行表 `host_config`（40+ 列） | SQLite，按配置面拆表 |
| 叠层 | `controller/host.go` 内 ~1400 行 HTML 常量 | `go:embed` 独立文件 |
| 胶水 | `service/host_glue.go`（1057 行，混 dao/model/llm/redline/airuntime） | 拆进 `rules/brain/guard/store`，每块一个职责 |

**明确不搬**：`agent/`（那是 CDP 抓包 Cookie agent，跟语音无关）、Minecraft "andy" 桥（`DY_MC_BRIDGE`）、
OBS WebSocket 控制、`liveclient_adapter.go` 那层把 raw frame 回喂 `handleMessage` 的过渡垫片、
主仓 `modules/ai/redline` 整包（只在本仓 `guard` 里放一条精简词表闸）。

## 触发条件表

上一版被批过"接了之后她也不会说话"——八段口播只有两段是定时器驱动，冷场整轮只蹦两句。
所以每条播报段都必须能回答"没有帧的时候它说不说"：

| 段 | 触发 | 空场（无帧）会不会说 |
|---|---|---|
| 礼物答谢 | `OnGift`，按 `UnitPrice*Count` 分档 | 否（本来就该等礼） |
| 进场欢迎 | `OnMember` | 否 |
| 弹幕回应 | `OnChat` 命中关键词 / @ | 否 |
| 点赞冲量 | `OnLike` 累计阈值 | 否 |
| 关注/分享答谢 | `OnSocial` action 1/3 | 否 |
| 小玩法引导 | **定时器**：每 N 秒轮播一条引导话术 | **会** |
| 冷场兜底 | **定时器**：连续 M 秒无帧 → 播兜底句 | **会** |
| 下播收场 | `OnControl` status=3 | 事件驱动 |

后两段故意不依赖帧，保证冷场也有声。N/M 与话术池全在后台可配。

**第一刀落地范围裁定**：点赞冲量、关注/分享答谢两段**未实现**，推到第四刀。原因是它们在 `host.ChatInput`
上没有诚实的表达形态——硬把"128 个赞"塞进 `Gift` 字段，叠层弹幕流会显示成「xx 送出 点赞128」这种假话。
宁可晚上线，不造假语义。

**闸门**（全部后台可调，默认值照搬主仓已验证的口径，不自创）：单条最长字数、同类冷却秒、
全局最小间隔秒、队列上限（超了丢低优先级，不堆音）。

## 互动回话与发评论的红线

SDK README 明写：`SendComment` 是"能力"不是"自动化"，只在受控交互里单条、限频地发，绝不在无人值守里循环。落地成：

- **默认只开口播，不发评论**。发评论是后台显式开关，打开后仍须同时满足：只回 @主播 或命中触发词的弹幕、
  单条最小间隔、当日总条数上限、`guard` 词表闸通过。任一不满足就只播不写。
- `guard` 独立成包，**开口播和发评论共用同一道闸**，不给某条路径开后门。

## 后台配置面（v1 就这 6 组）

1. **连接**：房间号、Cookie（留空=游客）、重连与风控退避
2. **声音**：TTS 引擎（Piper / Kokoro / MiniMax / edge-tts / SAPI）、base+key、音色、语速音量、输出设备（列本机声卡）、试听
3. **播报规则**：上面那张触发表，每行 = 事件类型 + 档位/关键词 + 话术模板（多条轮播）+ 优先级 + 冷却 + 启用
4. **闸门**：字数上限、同类冷却、全局间隔、队列上限、引导 N 秒、冷场 M 秒
5. **互动**：是否发评论、触发词/@、单条间隔、当日上限
6. **点歌**：是否启用、搜索/队列/音量、与播报的闪避（ducking）、触发关键词

形象（Live2D 皮）只读列出，切换靠配置文件，不在后台做上传——皮和授权口径不搞成动态可写。
`大肥鱼` 这张皮是 B站 @Mao绒绒家园 免费公皮，直播须署名，禁搬运倒卖：署名文案随模型走，写死在形象清单里。

## 本机实测：waveOut 开不了物理喇叭（2026-11-10）

逐设备播同一句 wav 实测：12 个输出设备里**只有"扬声器/听筒 (Realtek(R) Audio)"打不开**
（`waveOutOpen` 返回码 4 = MMSYSERR_ALLOCATED，设备被独占或驱动不认老接口），
其余全是 Voicemeeter / CABLE 这类**喂 OBS 的输入口**——能开、能"播完无错"，但你耳朵听不到。

⇒ "听不到声音"不是合成坏了、也不是没播：`/tts/preview` 实测合成 109ms、播出 1330ms 成功。
⇒ 要本机监听到，得加一条走 **WASAPI 共享模式**的播放引擎（PowerShell MediaPlayer 同路径已验证能出声）；
   或者把 Windows 默认输出切到喇叭、并释放独占。
⇒ 硬规矩：**设备打不开必须显式报错**，不许静默"播完"。试听按钮已按这条实现（选喇叭就显示失败原因）。

## 已知坑（搬的时候要处理，不是重新发现）

- 主仓 TTS 播放装了 ffplay 时会走 ffplay，而 **ffplay 忽略输出设备**，只有 Windows waveOut 路径认 `SetOutputDevice`。
  边车要选设备，就不能让 ffplay 静默夺路：显式二选一，配了设备名就走 waveOut。
- 主仓 TTS 与点歌是两套音频栈（`tts.Player` ffplay/waveOut vs `music.go` oto）。点歌要暂停/seek，
  保留 oto，不强行合并成一套。
- 主仓 `service/host_glue.go` 头部注释仍写"env 是配置来源""ffplay 出到系统默认设备"，与 DB 驱动的实况矛盾——
  搬逻辑，不搬这段过期散文。
- 主仓 `controller/host.go` 里除叠层外还内联了 panel / test 两个旧运维页，功能已被 React 后台覆盖：不带过来。

## 测试与验收

- 单测：`rules` 命中表（给定事件序列 → 期望动作，含冷却/优先级/丢弃）、`host` 队列与闸门（搬主仓已有断言）、
  `store` 迁移与读写、`guard` 词表闸。
- 回放：`--replay frames.jsonl` 喂帧序列，不开真房间就能验"冷场时引导/兜底两段是否真出声"。
- 联调：真房间只验 SDK 连接 + 声卡出声一次，不做常驻。
- 闸门必须能变红：每条验收脚本先证明它在错误输入下会失败，避免"路径不存在→整项跳过→仍印 GREEN"。
- 画面不由我截图自证；只跑 `go vet` / `go test` / 前端 `tsc`。

## 交付实况（第二刀核心已提前落地，2026-11-10）

后台设计图上挂着 保存 / 闭麦 / 加一条 三个没有后端的按钮（点了 0 请求）——那是假控件，
所以把第二刀的核心提前做了：SQLite 单文件 + `/config` 读写 + `/mute` + 热生效。

两条踩出来的硬规矩：

1. **保存不许冲掉闭麦**。页面 POST 的 body 里没有 `muted`，若拿请求体去推现场，
   直播中点一次保存就悄悄把麦开回去（库里还是闭麦，现场已开）。现在 apply 一律推**库里**那份。
2. **带 JSON 的 POST 会先发 CORS 预检**，漏 `Access-Control-Allow-Headers` 时浏览器静默拦掉真请求，
   页面只显示"服务没起"——比报错难查得多。GET 不带 JSON，照不出这个洞。

## 交付顺序（四刀，每刀能上屏）

1. **能听**：SDK 收帧 → 规则 → TTS → 本机出声（配置走 YAML，先不建库）
2. **能配**：SQLite + 后台 6 组配置 + 试听 + 设备选择
3. **能看**：Live2D 叠层页（大肥鱼 + 口型 + 字幕）
4. **能互动**：关键词/@ 回话 + 受闸的 SendComment + 点歌闪避

## 蒸馏实况（第一刀落地时回写，2026-11-10）

搬完之后发现三件设计时不知道的事，都已改掉：

1. **主仓的 `host.compose` 只有 LLM 一条路**，没有任何模板兜底。所谓"模板 + 可选 LLM"两条腿，
   模板那条在主仓根本不存在——它是本仓新造的。落法：`ChatInput` 增两个字段，
   `Line`（背后没有观众的话：引导/兜底/收场）与 `Speak`（因某事而说、但话术已配好：礼物答谢/进场欢迎）。
   两者都原样播出、不惊动 LLM；`Speak` 仍让事件进叠层弹幕流，所以画面照样显示"谁送了什么"。
2. **`Start→Stop→Start` 会 panic**：老协程退出时执行 `close(h.done)` 读的是字段，
   第二次 Stop 已把字段置 nil（close of nil channel），或与新一代的 done 撞车（二次 close）。
   改成进协程时取定自己那一代的通道。主仓没有二次 Start，所以这个洞一直没被踩到。
3. **主播自己的话不许给冷场计时续命**：`Handle` 原本除 `Idle` 外一律刷新 `lastEventAt`，
   引导句一进来就把"安静"重置了，越冷越说却永远判不出冷场。改成只有观众的事算动静。

另外确认了一个已知坑仍在：装了 ffplay 时 `SetOutputDevice` 会被静默忽略，只有 Windows 原生通道认设备选择。
本仓用 `tts.RouteDevice` + `ApplyRoute` 收口——选了设备就关掉 ffplay 出口，不选就保持原行为。

## 已拍的默认（可推翻，附代价）

- 仓库名 `dafeiyu`，模块路径 `github.com/athlon18/dafeiyu`。改名要连远程与 README 一起动。
- v1 **单房间单进程**。要挂多直播间就再起一个进程（端口与数据目录已在 YAML 参数化，不改代码）。
- v1 **Windows-only**（声卡设备路由和 SAPI 是 Windows 路径）。非 Windows 退回 ffplay，只保合成不保选设备。

## 明确不做

- 官方互动玩法协议（service token / 玩法数据帧）——拿不到局内数值，只按礼物/弹幕档位推断。
- 多房间、观众端画面、玩法本体逻辑。
- 无人值守批量发评论。
