# 给 AI agent 的接入说明

这是一张德州扑克牌桌。你的 agent 连上一个 Unix domain socket，**读一行 JSON 是一个事件，写一行 JSON 是一个命令**。没有别的 API，人类玩家走的也是这条路（ADR-0002）。

牌桌不假设你用什么语言。下面那个能打牌的 agent 是 20 行 Python，不依赖任何库。

## 连上去

```sh
poker serve --blinds 1/2 --hand-delay 0 --hands 1000   # 打印出 Table Code
```

牌桌监听 `~/.poker/<CODE>.sock`。「加入牌桌」就是把 Table Code 拼进这个路径再连一次 socket——没有服务发现，没有注册中心（ADR-0013）。

连上之后**第一条命令必须是 join**：

```json
{"type":"join","name":"agent1","buyin":200}
```

名字就是身份，没有 token 也没有握手（ADR-0009）。断线之后用同一个名字再 join，座位和筹码都还在。

想用现成的客户端帮你转一道也行，事件完全一样：

```sh
poker join ABC234 --as agent1 --format=jsonl
```

## 一个能打牌的 agent

```python
import json, os, socket, sys

code, name = sys.argv[1], sys.argv[2]
path = os.path.join(os.path.expanduser("~/.poker"), f"{code}.sock")

sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
sock.connect(path)
conn = sock.makefile("rw")

def send(cmd):
    conn.write(json.dumps(cmd) + "\n")
    conn.flush()

send({"type": "join", "name": name, "buyin": 200})

for line in conn:
    event = json.loads(line)
    if event["type"] != "your_turn":
        continue

    snap = event["snapshot"]
    legal = {a["action"]: a for a in snap["legal"]}

    # 只从服务端给的合法动作里挑，不自己推导此刻能不能过牌。
    if "check" in legal:
        send({"type": "check"})
    elif snap["to_call"] * 10 <= snap["stack"]:
        send({"type": "call"})
    else:
        send({"type": "fold"})
```

这段代码是跑过的，它能和 `poker bot` 在同一张桌上对打。

## 轮到你时：`your_turn`

这是唯一一个你必须响应的事件，而且它自带决策需要的一切（ADR-0007）。**你可以完全无状态**——不必累积前面的事件，不必自己维护一份牌桌状态，也就不会和服务端算出分歧。

```json
{"type":"your_turn","hand":6,"player":"agent1","street":"preflop",
 "snapshot":{
   "hand":6, "street":"preflop",
   "hole_cards":["5s","5h"],
   "community_cards":null,
   "pot":4,
   "to_call":0,
   "stack":197,
   "seats":[
     {"player":"bot2","position":"BTN/SB","stack":199,"committed":2,"total":2},
     {"player":"agent1","position":"BB","stack":197,"committed":2,"total":2}],
   "legal":[
     {"action":"fold"},
     {"action":"check"},
     {"action":"bet","min":4,"max":199},
     {"action":"allin","amount":197}]}}
```

| 字段 | 含义 |
| --- | --- |
| `hole_cards` | 你自己的底牌，永远只有你的 |
| `community_cards` | 公共牌，preflop 时是 `null` |
| `pot` | 当前底池总额 |
| `to_call` | 你还要投多少才跟得上，0 表示可以过牌 |
| `stack` | 你手上还剩多少 |
| `seats` | 各家状态，顺序就是座位顺序。`position` 是位置（见下），`committed` 是本轮投入，`total` 是本手总投入，另有 `folded` / `allin` / `sitting_out` |
| `legal` | **此刻真正能做的动作**。`call` / `allin` 带 `amount`（要投多少），`bet` 带 `min` / `max`（本轮总投入能推到的上下限） |

`seats[].position` 是位置标注，只在牌局进行中有值：

```
BTN  庄家位，这条街最后说话
SB   小盲     BB  大盲
UTG  第一个说话（under the gun），人多时后面还有 UTG+1、UTG+2
LJ   HJ   CO  从 BTN 往右数回来的三个位置，CO 紧挨着 BTN
```

位置是德州扑克里最重要的那个变量——同样两张牌，在 BTN 和在 UTG 是两手完全不同的牌。
它能从庄家位和人数算出来，但那要照着一张各家略有出入的惯例表来，所以服务端算好给你，
免得每个 agent 各实现一遍还实现得不一样。一张桌最多 9 个人。

**照着 `legal` 挑就不会错。** 它不会撒谎——里面给的动作做下去一定被接受，这条有测试守着。它也已经替你算好了最小加注额、推光要多少、此刻能不能过牌，这些都不需要你自己推导。

## 你能发什么

| 命令 | 字段 | 说明 |
| --- | --- | --- |
| `join` | `name`、`buyin` | 连上之后的第一条命令 |
| `fold` | | 弃牌 |
| `check` | | 过牌 |
| `call` | | 跟注。筹码不够跟满就是推光，不是错误 |
| `bet` | `amount` | **把本轮总投入推到 `amount`**，不是「再加 `amount`」（ADR-0005） |
| `allin` | | 推光 |
| `topup` | `amount` | 补码。随时能发，下一手牌开始前到账，上限是牌桌的带入线（ADR-0015） |
| `sitout` / `sitin` | | 暂离 / 回座。暂离从下一手开始生效，座位和筹码都留着 |
| `quit` | | 离座。直接断开连接是一样的效果 |

没有 `raise`——传统术语里的下注与加注在这里是同一件事，区别只在此前有没有人下过注。也没有 `f` / `c` 这类单字母别名。

## 你会收到什么

一手牌的事件顺序固定是：

```
hand_start → blind × 2 → hole_cards → [your_turn ⇄ action]…
           → street（flop/turn/river，各自后面跟若干 action）
           → showdown → pot_awarded → hand_end
```

| 事件 | 什么时候来 | 关键字段 |
| --- | --- | --- |
| `table` | 你刚加入，只发给你 | `players`、`seats`、`blinds` |
| `joined` / `left` | 有人来了 / 走了 | `player`、`seats` |
| `sit_out` / `sit_in` | 有人暂离 / 回座 | `player`、`message`（暂离的原因） |
| `top_up` | 有人补码到账 | `player`、`amount`、`stack` |
| `hand_start` | 新的一手 | `hand`、`players`、`button`、`blinds`、`seats` |
| `blind` | 收盲注 | `player`、`action`（`small_blind`/`big_blind`）、`amount` |
| `hole_cards` | 发底牌，**只发给本人** | `cards` |
| `your_turn` | 轮到你了，**只发给本人** | `snapshot`（见上） |
| `action` | 有人做了动作 | `player`、`action`、`amount`（这一下投了多少）、`committed`（本轮共多少）、`stack`、`pot`、`forced` |
| `street` | 翻开新的一条街 | `street`、`cards`（新翻开的）、`board`（全部公共牌）、`pot` |
| `showdown` | 摊牌 | `showdown[]`：每人的 `player`、`cards`、`category`、`best` |
| `pot_awarded` | 分池 | `pots[]`：每个池的 `amount`、`eligible`、`winners` |
| `hand_end` | 这手结束 | `winners`、`pot`、`seats`（各家结束时的筹码） |
| `error` | 你做错了什么 | `code`、`message`，有时带 `min` / `max` / `amount` |

`action` 事件上的 `forced: true` 表示那一下不是那个玩家自己按的——他超时了或者掉线了，系统按规则替他做的。分析对手行为时别把这些算成他的决策。

## 出错了怎么办

动作不合法时，服务端回一条**带机器可读 `code` 的错误**，而且**轮次仍然归你**（ADR-0011）：

```json
{"type":"error","code":"min_raise","message":"至少要推到 38（或者 allin 推光 150）","min":38,"max":150}
```

同一轮连续三次非法，服务端会按「能过牌就过牌，否则弃牌」替你做决定，牌局继续——写坏的 agent 卡不死整张牌桌，但也别指望无限重试。

| `code` | 意思 |
| --- | --- |
| `not_your_turn` | 还没轮到你 |
| `cannot_check` | 你还欠着注，`amount` 是欠多少 |
| `nothing_to_call` | 没注要跟，用 `check` |
| `min_raise` / `bet_too_small` | 加注不够，`min` 是最少要推到多少 |
| `insufficient_stack` | 推太多了，`max` 是你最多能推到多少 |
| `no_chips` | 你一分钱都没有了 |
| `hand_over` / `no_hand` / `not_in_hand` | 现在没有你能行动的牌局 |
| `name_taken` / `bad_name` / `buyin_too_small` | 加入牌桌被拒 |
| `stack_at_max` / `topup_too_big` | 补码被拒，`max` 是还能补多少 |
| `table_full` | 桌子坐满了（最多 9 人） |
| `unknown_command` / `bad_action` / `bad_amount` | 命令本身有问题 |

**最省事的做法是永远不碰这张表**：只从 `legal` 里挑动作，你就一条错误都收不到。

## 这个环境承诺什么

**你看不到别人的底牌。** 任何时候都只有你自己的 `hole_cards` 会发给你。摊牌时亮出来的牌对所有人可见，但**弃牌者的底牌永不公开**——他不进摊牌，他的牌从发出去那一刻起就再没在任何人的事件流里出现过。暂离的人能看到的信息，严格等于一个弃牌后旁观的在座玩家，不多一格也不少一格。

这不是文档里的君子协定，是被测试逐格守住的不变量（ADR-0006）。不完全信息博弈的全部价值就在于「谁在什么时候看不到什么」，漏一处，训练和评测的结论就全部作废。

**牌是可复现的。** `poker serve --seed N` 之后，每手牌的种子都记进手牌历史，拿着一条记录就能把那一手单独重放出来（ADR-0016）：

```sh
poker verify ~/.poker/history/ABC234-20260918-120358.jsonl
# 重放了 528 手牌，0 手对不上。
```

**每一手都有完整记录。** `~/.poker/history/` 下的 JSONL，每行一手，含种子、所有人的底牌、每一个动作。那是上帝视角的训练评测数据，跟发给你的事件流不是一回事。

## 跑一次评测

```sh
# 打满 60 手自动收桌，不用谁去杀进程
poker serve --blinds 1/2 --seed 21 --hand-delay 0 --hands 60 --timeout 2s

poker bot ABC234 --as baseline --rebuy &
python3 my_agent.py ABC234 challenger &

# 跑完看战绩
poker history ~/.poker/history/ABC234-*.jsonl --stats
```

上面那个 20 行的 Python agent 跑出来是这样的：

```
共 60 手牌

玩家                 手数      赢      净筹码
baseline           60     18      +11
challenger         60     42      -11
```

顺带一提这张表为什么同时给「赢的手数」和「净筹码」：challenger 赢的手数是对手的两倍多，
钱却是输的——它只会过牌和跟小注，于是赢一堆底池很小的手，输掉少数几个大的。
只看胜率会得出完全相反的结论。（60 手也远不够下判断，真评测该跑几千手。）

几个和评测直接相关的参数：

- `--hands N`：打满就收桌。靠外面杀进程也能停，但停在哪一手是随机的，而随机停下的一批数据没法拿来比较两个 agent。
- `--seed N`：固定牌序。
- `--timeout`：单次行动的时限。到点服务端替你做决定，所以**一个想不出来就不回话的 agent 会被当成弃牌**，而不是把牌桌冻住。
- `--hand-delay 0`：两手牌之间不等。
- `--rebuy`（客户端）：输光自动补回最初带入，牌桌才能一直打下去。

净筹码按「每手结束时的筹码减开局时的筹码」累加，所以补码不会被算成盈利。
