# poker-cli

在本机多个终端之间对战的无限注德州扑克命令行程序，同时是一个给 AI agent 用的可对战环境。

**想直接开一局打：[docs/quickstart.md](docs/quickstart.md)**——开房、叫人、屏幕上是什么、卡住了怎么办，一分钟能坐下。
**想接一个 agent 进来：[docs/agent.md](docs/agent.md)**。

术语以 [CONTEXT.md](CONTEXT.md) 为准，架构决策记在 [docs/adr/](docs/adr/)。动手改之前先读这两处。

## 跑起来

```sh
go build -o poker ./cmd/poker

./poker serve --code TABLE1 >/dev/null 2>&1 &   # 开桌
sleep 1                                          # 等 socket 建好，不然机器人会连空
./poker bot TABLE1 --as bot1 --rebuy >/dev/null 2>&1 &
./poker bot TABLE1 --as bot2 --rebuy >/dev/null 2>&1 &
./poker join TABLE1 --as 我                      # 自己坐下
```

要跟别人打就各开各的终端，把 `serve` 打印出来的 Table Code 报给他们——细节和排错都在
[快速开始](docs/quickstart.md)里。

凑够两个有筹码的人就自动开牌，此后每 `--hand-delay` 开下一手，不需要任何人确认（ADR-0014）。
轮到你的时候直接敲 `call`、`check`、`fold`、`allin` 或 `bet 100`；
想歇会儿就 `sitout`（这手打完生效，座位和筹码都留着），回来敲 `sitin`，输光了先 `topup 200`。

`bet` 永远是「把本轮总投入推到这个数」，不是「再加这么多」（ADR-0005）。没有 `raise`，也没有单字母别名。

### 屏幕上只有正在打的这一手

对着终端时，`join` 就地重画一屏，而不是把十四手牌的流水堆在一起让你往上翻：

```
第 7 手  河牌  盲注 1/2
  bot1        SB        200  弃
  bot2        BB        197  投 2
▶ 我          BTN       196   4♥ A♥
公共牌 K♦ 4♦ 6♠ 3♥ J♦    底池 7
  ── 翻牌 K♦ 4♦ 6♠
  bot2 过牌
  我 过牌
  ── 转牌 3♥
  bot2 过牌
  我 过牌
  ── 河牌 J♦
  bot2 下注到 2，底池 7
轮到你了    要跟 2
可以：fold / call（跟 2） / bet <4-196> / allin（推 196）
>
```

新的一手开始，上一手的牌、流水、底池一并清掉——这一屏永远只讲正在打的这手牌（ADR-0017）。

这一屏有多高由终端说了算：屏幕高就多显示几条流水，拉矮了就少显示几条，一帧永远塞得进一屏。
塞不进的话终端会滚动，而滚动之后「上移 N 行」回到的就不是原来那个位置，屏幕会一路烂下去。

`--format` 默认是 `auto`：对面是终端就重画，是管道、重定向或文件就退回 `text`（一行一条往下滚）。
两条路都写死在测试里：重画那条真开一个 pty 跑完三手牌，再按 ANSI 的规矩把字节解回屏幕，
断言最后只剩一帧；滚动那条一个转义序列都不许漏出去。显式写 `--format=text` 或 `--format=live` 就按你说的办。

重画只在收到事件时发生，而轮到你的时候不会有任何事件进来，所以打字和重画天然错开。
唯一会被冲掉的是「抢在别人行动时提前输入」——那一行的回显会被下一帧盖住，但命令本身照样发得出去。

### 让它自己一直打下去

```sh
./poker serve --blinds 1/2 --hand-delay 0 --timeout 1s
./poker bot ABC234 --as bot1 --rebuy
./poker bot ABC234 --as bot2 --rebuy
```

`--rebuy` 让机器人输光之后自动补回最初的带入。不加的话，破产的人会自动 Sitting Out，
桌上剩不到两个有筹码的人，牌局就永远停在那儿了——无人值守的自对弈迟早撞上这一幕。

## 给 agent 用

完整的接入说明在 **[docs/agent.md](docs/agent.md)**：事件表、命令表、错误码表，
以及一个 20 行、不依赖任何库、真能和 `poker bot` 对打的 Python agent。

加 `--format=jsonl`，每行一个事件对象，阻塞读一行即可事件驱动：

```sh
./poker join ABC234 --as agent1 --format=jsonl
```

轮到你时收到的 `your_turn` 自带一份完整快照，决策需要的东西全在里面——包括一份**合法动作列表**，
所以 agent 不必自己推导此刻能不能过牌、最小加注额是多少（ADR-0007）：

```json
{"type":"your_turn","hand":6,"player":"agent1","street":"preflop",
 "snapshot":{"hand":6,"street":"preflop","hole_cards":["5s","5h"],"community_cards":null,
  "pot":4,"to_call":0,"stack":197,
  "seats":[{"player":"bot2","stack":199,"committed":2,"total":2},{"player":"agent1","stack":197,"committed":2,"total":2}],
  "legal":[{"action":"fold"},{"action":"check"},{"action":"bet","min":4,"max":199},{"action":"allin","amount":197}]}}
```

回一行命令就行：`{"type":"call"}`、`{"type":"bet","amount":40}`。
动作不合法时回来的是带 `code` 的结构化错误（如 `min_raise` 带 `min`），轮次仍归你，最多重试三次（ADR-0011）。

`--seed` 给定时牌序完全可复现（ADR-0004）——前提是座位顺序也一样，因为发牌是按座位轮着发的。

跑评测时用 `--hands N`：打满就自动收桌。靠外面杀进程也能停，但停在哪一手是随机的，
而随机停下的一批数据没法拿来比较两个 agent。

## 手牌历史

每手牌结束时，一条含**随机种子、全部底牌与每个动作**的完整记录会追加写进
`~/.poker/history/<CODE>-<开桌时间>.jsonl`（ADR-0008）。`--no-history` 关掉它。

每条记录都是**自足**的：种子、座位顺序、庄家位、盲注四样凑齐，就能把第 37 手单独重放出来，
不必先把前 36 手连同每个人的每个动作原样重来（ADR-0016）。`poker verify` 干的就是这件事：

```console
$ poker verify ~/.poker/history/TABLEC-20260918-120358.jsonl
重放了 528 手牌，0 手对不上。
```

对不上的时候它会指出问题出在哪一层：

```console
✗ 第 2 行（第 2 手，种子 13105718115652666056）：按种子重放，bot3 的底牌对不上：记的是 [Jd Qd]，重放出来是 [4d Ad]
```

上色只在对面确实是终端时才开——管道、重定向、`NO_COLOR=1` 一律输出干净文本，
免得转录和日志里混进转义序列。

`poker history` 是给人看的那一半——复盘某一手，或者拉一张战绩表：

```console
$ poker history ~/.poker/history/PKR234-*.jsonl --hand 2
── 第 2 手 ──  PKR234  2026-09-18 16:59:12  盲注 1/2  庄家 bot1
   种子 6758751650239165124
   筹码  bot2 200 | bot1 199 | 我 201
   底牌  bot2 J♦ K♠ | bot1 K♥ 6♦ | 我 6♣ 7♣

   翻牌前
     bot1       弃牌
     我         跟注 1
     bot2       过牌

   翻牌  Q♣ 8♣ 3♣
     我         下注到 2
     bot2       弃牌

   底池 6 → 我
   结束  bot2 198 (-2) | bot1 199 (+0) | 我 203 (+2)

$ poker history ~/.poker/history/PKR234-*.jsonl --stats
共 5 手牌

玩家             手数     赢   净筹码
bot2                5      1       +5
bot1                5      1       +1
我                  5      3       -6
```

历史是上帝视角的（所有人的底牌都在里面），跟发给玩家的事件流不是一回事——
ADR-0006 那套可见性规矩管的是事件，不是这个文件。别把记录当事件发出去。

## 现在做到哪了

两刀下来，一局完整的无限注德州扑克已经能打了：

- 盲注（含单挑时 button 即小盲的特例）、Button 每手轮转
- 四条 Street 的完整下注轮，大盲在 preflop 的 option
- bet / call / check / fold / allin，最小加注额、不足额 all-in 不重开下注轮
- 主池与边池按投入额分层，未被匹配的注额原样退还，平分除不尽时按位置发
- Stack、Buy-in、Top-up（补码），输光自动进入 Sitting Out，补了码就回来
- 手动 `sitout` / `sitin`；`--rebuy` 让无人值守的牌桌一直打下去
- 断线接管与行动超时：没人能靠装死或拔网线冻住整张牌桌
- Hand History 只追加落盘，每手一个自足的种子，`poker verify` 逐手重放校验，`poker history` 复盘与战绩
- `--hands N` 打满收桌，评测跑完自己停
- 人类客户端在终端上就地重画，只显示正在打的这一手；非终端自动退回滚动输出

还没有的：跨机联机（传输已经在接口后面了，加 TCP 时不用动牌局逻辑）。

## 目录

```
cmd/poker/         子命令入口
internal/poker/    牌局纯核心：牌、牌堆、牌力、底池、一手牌的状态机、事件
internal/protocol/ 客户端与服务端之间的 wire 格式（说什么）
internal/transport/ 怎么连上一张牌桌（怎么连）：接口 + 同机实现 + 内存实现
internal/textui/   在终端上长什么样：花色符号、红黑配色、按显示列宽对齐、对面是不是终端
internal/history/  手牌历史：记录、只追加落盘、重放校验
internal/server/   牌桌（一个进程一张桌）
internal/client/   人类客户端与机器人客户端
```

依赖方向单向朝内：`internal/poker/` 不 import 任何其他内部包，**也不 import 任何第三方库**，
更不含 IO、goroutine 或 `time.Now()`（ADR-0012）。这条约束是 `--seed` 可复现测试与可见性测试共同的前提，
是整个项目里最容易被悄悄侵蚀的一条，所以 `internal/poker/deps_test.go` 逐个文件检查 import，违反了直接报错。

模块之外只依赖三个库，都在「难的是平台本身」那一类（ADR-0018）：`golang.org/x/term`
（对面是不是终端、终端多高）、`github.com/mattn/go-runewidth`（一个字素在屏幕上占几列）、
`github.com/creack/pty`（只用在测试里，开一个真终端来验收重画）。前两个各自替掉了一段自己手写的、
已经在骗人的代码——那才是它们能进来的理由。牌力、边池、可见性这些属于这个牌局本身的东西一律自己写。
一手牌是一个纯状态机：`NewHand` 起局，`Apply(玩家, 动作)` 推进，每次推进返回该发出去的事件。

牌在屏幕上是 `A♠`（红桃方块在终端里标红），在线路和历史文件里是 `"As"`，两者不是一个东西：前者归 `internal/textui`，
后者是 `poker.Card.String()`。混成一个的话，改一次显示就会把 JSONL 的 wire 格式和
已经存下的手牌历史一起改掉——照着 docs/agent.md 写的 agent 会当场解析失败，
旧历史文件也再也 verify 不过，而这两样都不会有编译错误。

「怎么连上一张牌桌」收敛在 `internal/transport` 后面（ADR-0001）：服务端拿到的是 `net.Listener`，
客户端拿到的是 `net.Conn`，中间走 Unix socket 还是别的都不归它们管。
包里有两个实现——同机的和纯内存的——跑的是同一套一致性断言，
牌局的端到端测试也在两者上各跑一遍。只有一个实现的接口，形状是照着那个实现长的。

## 测试

```sh
go test ./...
go test -race ./...
# 乱发命令，看牌桌崩不崩
go test -run=NONE -fuzz=FuzzClientCommands -fuzztime=30s ./internal/server/
```

每次 push 都在 CI 上跑一遍（`.github/workflows/ci.yml`）：gofmt、`go vet`、`go mod tidy` 之后没有变化、
`go test -race ./...`，外加 30 秒模糊测试。`-race` 是重点——服务端是一个牌桌 goroutine 加每条连接两个，
竞态在本地十次里可能九次不出现。

几条最值得看的：

- `cmd/poker/agentdoc_test.go` —— **把 docs/agent.md 里那段 Python 抠出来真跑一遍**：起真服务端、真对手，
  打满 8 手，然后去历史里确认它自己按下过动作（而不是一路被超时代打）。代码是从文档里抠的，不是抄一份进测试——
  抄一份的话两边会各自演化，而烂掉的恰恰是别人照着抄的那一份。事件改个字段名，这条当场红。
- `internal/server/fuzz_test.go` —— 乱发命令，断言牌桌从不崩。发的不只是坏 JSON（那太容易挡），
  还有结构合法、顺序和数额离谱的命令：没轮到就 bet、负数额、大到溢出的数——状态机的坑在这一类里。
- `internal/server/crash_test.go` —— 牌桌真崩一次之后：前面打完的手牌一条不少地落了盘，
  报出来的错里带着那一手的种子。没有种子，这个 bug 就只能靠运气再撞一次。
- `internal/poker/deps_test.go` —— 牌局核心只能用标准库。以前不用测，因为整个模块没有依赖可违反。
- `internal/textui/textui_test.go` —— 对齐这件事只能量，不能看。含四类以前算错的名字（组合重音、泰文、
  ZWJ emoji、扑克牌 emoji 🃏），以及一条对着真 pty 跑的：字符设备不等于终端，`> /dev/null` 不该被上色。
- `internal/poker/eval_test.go` —— 牌力识别与比较，含轮子顺子、踢脚、完全平局。
- `internal/poker/betting_test.go` —— 下注轮的规则细节（盲注位置、大盲 option、最小加注、
  不足额 all-in 不重开下注轮），外加一条随机对局的**筹码守恒**测试：让随机 agent 从合法动作列表里
  瞎选，跑几百手牌，桌上的筹码总额一分都不能变。钱算错了不会崩，只会悄悄少给某人几块。
- `internal/poker/pot_test.go` —— 边池分层与判定，每个池单独判赢家。
- `internal/client/pty_test.go` —— 就地重画唯一真正的验收：开一个真 pty，把三手牌的事件跑完，
  再按 ANSI 的规矩把两万多字节解回屏幕，断言 24 行的终端上最后只剩一帧、没有残渣、没有留着的输入提示。
  判卷用的终端模拟器是照着 ANSI 的定义写的五十行，不是照着被测代码写的——跟着一起理解错就什么都没测到。
- `internal/client/live_test.go` —— 就地重画那一版的算术。新的一手必须把上一手的牌、流水、底池全清掉；
  每帧往回挪的行数必须正好等于上一帧的行数（少一行留残渣，多一行吃历史），用户敲的那一行回显也算一行；
  一帧的高度跟着终端走，把窗口拉到 8 行也不能溢出。另有一条守着 ADR-0006：这一版是攒状态的，
  别人的底牌一旦被记进去就会一直画在屏幕上。
- `internal/client/play_test.go` —— 事件和键盘输入汇成的那个 select 循环。里面钉着 jsonl 必须逐字节原样直通，
  以及 text 一个转义序列都不许漏出去——那条路上跑着转录、CI 日志和 `| tee`。
- `internal/client/decide_test.go` —— 机器人是 agent 接口的常驻回归测试（ADR-0010），所以它自己也得被测。
  里面钉着一条真出过的 bug：两个「强牌就加注到最小加注额」的机器人会互相对加到有人推光，
  规则上完全合法，但一手牌要走上百个动作。
- `internal/server/topup_test.go` —— 补码只在两手牌之间落地（这条能直接从事件流上看出来：
  一条到账的 `top_up` 绝不该夹在 `hand_start` 和 `hand_end` 中间），以及破产→补码→牌桌接着跑。
- `cmd/poker/main_test.go` —— 编译出真二进制、起真进程跑一遍。它测的是别处结构上看不到的东西：
  进程什么时候退出、退出之前有没有把事情做完。「最后一手的历史没落盘」那个 bug 就活在这里。
- `internal/transport/transport_test.go` —— 同一套断言跑在每个传输实现上，分清哪些是「传输的约定」、
  哪些只是「Unix socket 恰好如此」。
- `internal/history/history_test.go` 与 `internal/server/history_test.go` —— 记下来的东西必须真的还原得回去：
  几百手随机对局逐条重放校验，外加十种「记录被改过」的情形都必须报错。
  一个什么都说「对」的校验器比没有校验器更糟，它会让人以为历史是可信的。
- `internal/poker/visibility_test.go` 与 `internal/server/server_test.go` —— ADR-0006 的可见性不变量，
  分别在事件层面和真实 socket 投递路径上各守一道，含「弃牌者底牌永不公开」和
  「Sitting Out 的人看到的信息严格等于弃牌后旁观的在座玩家」。
