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

## 常驻

边车要一直活着才有用。两种做法：

- 当场用：`dafeiyu.exe`（连房间 + 开后台接口，默认 :8420）
- 开机自启 + 挂了自动拉起：用管理员权限跑一次
  `schtasks` / PowerShell `Register-ScheduledTask`（本机实测普通权限注册会被拒 `0x80070005`）。
  不想提权就挂一个守护脚本：每 5 秒查一次 `dafeiyu` 进程，没了就 `Start-Process` 拉起来，
  日志写 `data/watchdog.log`。已实测：杀掉边车 12 秒内自己活过来，接口恢复。

关掉常驻：结束 `dafeiyu` 与守护的 `powershell` 进程，或删除计划任务 `dafeiyu`。

## OBS

页面由二进制自己提供，不用再开静态服务：

- 叠层：浏览器源地址 `http://127.0.0.1:8420/overlay`，**宽高必须填 1080×1920**
  （实测库在宽度 < 1080 时会把画布算成 0x0、连模型文件都不取；挂错尺寸时页面上会直接写原因，不是一片空白）
- 后台：浏览器打开 `http://127.0.0.1:8420/admin`
- 叠层整页透明，OBS 里角色背后不会有一块板；署名"模型：Mao绒绒家园"随画面走

声音：`tts.deviceName` 填虚拟音频线名，OBS 抓它的**播放端**。本机实测的三条路：
  - VB-Cable：Windows 输出设成 `CABLE Input`，OBS 音频输入抓 `CABLE Output`
  - Voicemeeter：Windows 输出设成 `Voicemeeter Input`，OBS 抓 `Voicemeeter Out B1`
  - 直接跟系统默认：`deviceName` 留空

  填设备名是**子串匹配**（不分大小写），所以别只填 `CABLE`——`CABLE Input` 和 `CABLE Output` 都含这三个字，
  会撞上排在前面的那条。`-list-devices` 打出来的名字整段抄进去最稳。
  填了设备名后会强制走认设备的原生通道，不会被装了 ffplay 抢走（ffplay 只认系统默认设备）。
- 画面：浏览器源（第三刀起提供 `/overlay`）。

## 口型

后端把合成好的音频压成 20Hz 响度序列（0~100）连同起点时刻放进 `/state`，叠层按
`Date.now()-speakAt` 取对应一格写 `ParamMouthOpenY`——音节重的地方嘴张得大，停顿处自然合上。
声音只由本机声卡放一份，浏览器不再放第二份（那会双重出声）。

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

## 配置存在哪

YAML 只当**出厂种子**：第一次跑把 `config.yaml` 灌进 `data/dafeiyu.db`，之后一切改动都在库里，
后台页改的就是库。跑着的边车会当场吃到新配置（换话术表 + 按库里状态开闭麦），不用重启。

闸门（全局间隔 6 秒 / 单句 60 字 / 队列 2 句…）不在页面上，仍走 YAML 出厂值。

## 还没做的

关键词回话与发评论、点歌（第四刀）。闸门参数（全局间隔 6 秒 / 单句 60 字 / 队列 2 句）不在页面上，走 YAML 出厂值。
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
