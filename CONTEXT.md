# Poker CLI

一个在本机多个终端之间对战的无限注德州扑克命令行程序，同时要能作为 AI agent 的可对战环境。

## Language

**Table（牌桌）**:
长期存在的对局场所，由 Table Code 标识。玩家加入和离开的是牌桌，筹码带在牌桌上。
_Avoid_: 房间, Room, Game

**Table Code（牌桌码）**:
标识一张牌桌的 6 位大写字母数字串，设计成人能读出来、能口头报给别人。
_Avoid_: 房间号, Room ID, Game ID

**Hand（一手牌）**:
从发底牌到分池结束的一个完整回合。一张牌桌上反复进行多手牌，每手牌结束后庄家位轮转。
_Avoid_: 局, Round, Game

**Street（下注轮）**:
一手牌内部的四个下注阶段之一：preflop、flop、turn、river。
_Avoid_: 轮, 阶段, Phase, Stage

**Buy-in（带入）**:
玩家加入牌桌时带到桌上的筹码数额，加入时由玩家选定。
_Avoid_: 充值, 入场费, 本金

**Bot（机器人）**:
由程序而非人操控的玩家，占据一个与人类玩家完全相同的座位。
_Avoid_: AI, NPC, 电脑玩家

**Seat（座位）**:
牌桌上的一个位置。玩家占据座位后，即使不参与当前手牌，座位仍属于他。
_Avoid_: 位子, 玩家槽, Slot

**Stack（桌上筹码）**:
玩家当前放在牌桌上、可用于下注的筹码量。
_Avoid_: 余额, 本金, Balance, Chips

**Sitting Out（暂离）**:
玩家保留座位但不参与手牌的状态。Stack 归零时自动进入该状态，仍能收到牌桌上的公开信息。
_Avoid_: 观战, 旁观, 离开, Spectating

**Top-up（补码）**:
玩家向自己的 Stack 追加筹码。只能发生在两手牌之间。
_Avoid_: 补充, 充值, Re-buy, Reload

**Hole Cards（底牌）**:
发给单个玩家、仅他本人可见的两张牌。
_Avoid_: 手牌（会与 Hand 混淆）, 私牌, 暗牌

**Community Cards（公共牌）**:
翻在桌面上、所有玩家共用的至多五张牌。
_Avoid_: 公牌, 台面牌, 明牌

**Action（动作）**:
玩家轮到自己时可以做的事，共五种：bet、call、check、fold、allin。
_Avoid_: 操作, 指令, Move

**Bet（下注）**:
把自己在当前 Street 上的总投入推到指定数额的动作。它同时覆盖了传统术语中的下注与加注。
_Avoid_: Raise, 加注, 跟注加价

**Blind（盲注）**:
每手牌开始前由两个特定座位强制投入的筹码，分小盲与大盲，建桌时定死。
_Avoid_: 底注, Ante

**Button（庄家位）**:
一手牌中最后行动的座位，标记发牌顺序的起点，每手牌结束后顺时针轮转一位。
_Avoid_: 庄家, Dealer, D 位

**Pot（底池）**:
当前一手牌中所有玩家已投入的筹码总和。
_Avoid_: 奖池, 池子

**Side Pot（边池）**:
当某玩家 all-in 且筹码少于其他人时，超出他所能匹配部分的筹码另行形成的独立奖池，由仍有筹码的玩家单独争夺。
_Avoid_: 副池, 分池

**Showdown（摊牌）**:
一手牌结束时未弃牌的玩家亮出 Hole Cards 比大小的环节。亮出的牌对所有人可见。
_Avoid_: 开牌, 亮牌, 比牌

**Hand History（手牌历史）**:
一手牌结束后追加落盘的完整事实记录，含随机种子、全部 Hole Cards 与每个 Action。
_Avoid_: 日志, 战绩, Replay
