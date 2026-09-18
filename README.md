# poker-cli

在本机多个终端之间对战的无限注德州扑克命令行程序，同时是一个给 AI agent 用的可对战环境。

术语以 [CONTEXT.md](CONTEXT.md) 为准，架构决策记在 [docs/adr/](docs/adr/)。动手改之前先读这两处。

## 跑起来

```sh
go build -o poker ./cmd/poker

# 开一张牌桌，它会打印 Table Code
./poker serve --hand-delay 1s

# 另开终端，用打印出来的码坐下
./poker join ABC234 --as alice

# 再开一个，让机器人坐下（独立进程，走的是和外部 agent 完全相同的接口）
./poker bot ABC234 --as bot1
```

凑够两个人就自动开牌，此后每 `--hand-delay` 开下一手，不需要任何人确认（ADR-0014）。

给 agent 用的话加 `--format=jsonl`，每行一个事件对象，阻塞读一行即可：

```sh
./poker join ABC234 --as agent1 --format=jsonl
```

```json
{"type":"hand_start","hand":1,"players":["bot1","agent1"]}
{"type":"hole_cards","hand":1,"player":"agent1","cards":["9d","6c"]}
{"type":"community_cards","hand":1,"cards":["2h","Ks","9s","As","Qc"]}
{"type":"showdown","hand":1,"showdown":[{"player":"bot1","cards":["Ad","7h"],"category":"一对","best":["Ad","As","Ks","Qc","9s"]}]}
{"type":"hand_end","hand":1,"pot":0,"winners":["bot1"]}
```

`--seed` 给定时牌序完全可复现（ADR-0004）——前提是座位顺序也一样，因为发牌是按座位轮着发的。

## 现在做到哪了

第一刀是一条最薄但完整贯通的竖切：**两人以上、无盲注、每人两张底牌、直接发满五张公共牌、摊牌比七张牌力、赢家通吃**。
`serve → join → JSONL 事件流 → 牌力比较` 这条管道已经全线打通。

还没有的东西，按计划逐刀加：下注轮与 Street 推进、盲注、边池、Button 轮转、Stack 与补码、Sitting Out、Hand History 落盘。
`pot` 字段已经在事件里就位，目前恒为 0，等盲注进来自然有值。

## 目录

```
cmd/poker/        子命令入口
internal/poker/   牌局纯核心：牌、牌堆、牌力、一手牌、事件
internal/protocol/ 客户端与服务端之间的 wire 格式
internal/server/  牌桌（一个进程一张桌）
internal/client/  人类客户端与机器人客户端
```

依赖方向单向朝内：`internal/poker/` 不 import 任何其他内部包，也不含任何 IO、goroutine 或 `time.Now()`（ADR-0012）。
这条约束是 `--seed` 可复现测试与可见性测试共同的前提，是整个项目里最容易被悄悄侵蚀的一条。

## 测试

```sh
go test ./...
go test -race ./...
```

两条承重墙有专门的测试守着：

- `internal/poker/eval_test.go` —— 牌力识别与比较，含轮子顺子、踢脚、完全平局。
- `internal/poker/visibility_test.go` 与 `internal/server/server_test.go` —— ADR-0006 的可见性不变量，
  分别在事件层面和真实 socket 投递路径上各守一道。
