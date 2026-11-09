# dafeiyu

抖音直播间语音播报边车：读直播间实时事件 → 规则判定 → 合成语音 → 播到本机声卡给 OBS 抓。
独立进程、独立配置，不依赖 douyinlive 主仓运行时。输入端是 [douyin-live-client](https://github.com/athlon18/douyin-live-client) 的 typed 事件。

设计口径见 [docs/superpowers/specs/2026-11-10-dafeiyu-sidecar-design.md](docs/superpowers/specs/2026-11-10-dafeiyu-sidecar-design.md)。

## 装

- Windows（音频设备路由和 SAPI 是 Windows 路径）
- Go 1.25+；本机用 `GOTOOLCHAIN=local`，取模块走 `GOPROXY=https://goproxy.cn,direct`
- 声音：起一个 OpenAI 兼容的 TTS 网关，仓库里带了三份

Piper 的音色模型不入库（63MB），自己拷进 `data/tts/models/`（`.onnx` 与同名 `.onnx.json` 成对放）：

```sh
python tools/tts-server/server.py         # Piper，默认 http://127.0.0.1:9880/v1
python tools/tts-server/kokoro_openai.py  # Kokoro，默认 :9884
python tools/tts-server/minimax_openai.py # MiniMax，默认 :9883（要 key）
```

## 跑

```sh
copy config.example.yaml config.yaml    # 填 roomId；Cookie 留空=游客

go run ./cmd/dafeiyu -list-devices                    # 列输出设备，把名字抄进 tts.deviceName
go run ./cmd/dafeiyu -say "测试一句" -quiet           # 只验合成通不通，不出声
go run ./cmd/dafeiyu -say "测试一句"                  # 真出声，验声卡路由
go run ./cmd/dafeiyu -replay testdata/frames.jsonl    # 不开房间、不出声，验"冷场时会不会开口"
go run ./cmd/dafeiyu                                  # 连真房间
```

`-replay` 是给验收用的：它把一份 JSONL 帧序列打在虚拟钟上，走完同一张规则表和同一个编排器，
最后打印"播出 N 句"。N=0 就说明规则或闸门配歪了，不用开播就能发现。

## OBS

- 声音：`tts.deviceName` 填虚拟音频线名（如 `CABLE-Auxiliary`），OBS 把该线作为音频输入。
  填了设备名后会强制走认设备的原生通道，不会被装了 ffplay 抢走（ffplay 只认系统默认设备）。
- 画面：浏览器源（第三刀起提供 `/overlay`）。

## 播报触发面

| 段 | 触发 | 空场会不会说 |
|---|---|---|
| 礼物答谢 | 礼物帧，按连击段数 10/50/100 分档 | 否 |
| 进场欢迎 | 进场帧 | 否 |
| 弹幕回应 | 弹幕帧（有 LLM 才成句） | 否 |
| 玩法引导 | 定时器 `guideEverySec` | 是 |
| 冷场兜底 | 连续 `idleAfterSec` 无帧 | 是 |
| 下播收场 | 控制帧 status=3 | 事件驱动 |

点赞冲量、关注/分享答谢**未实现**：当前编排契约上它们没有诚实的表达形态，硬套会让叠层显示成假观众发言。

## 第一刀没做的

后台配置页（第二刀）、Live2D 叠层（第三刀）、关键词回话与发评论、点歌（第四刀）。
第一刀不发任何评论，也不接官方互动玩法协议。

## 测试

```sh
go vet ./...
go test ./...
```

`-race` 在本机 Go 1.27.3 上跑不起来（`exit status 0xc0000139`，入口点缺失），与代码无关。

## 素材与署名

`data/host/model/大肥鱼/` 不入库：那是 B站 @Mao绒绒家园（UID 431301506）的免费公皮，
直播须注明作者，禁止搬运/倒卖模型文件。要出镜时自行拷进该目录，并在直播简介署名。
