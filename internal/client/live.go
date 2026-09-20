package client

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/textui"
)

const (
	// logCap 是牌局流水最多攒几条。屏幕上显示几条由终端多高决定（见 frame），
	// 这个数只是不让一手长牌把内存越攒越多。
	logCap = 64
	// fallbackLogLines 是问不出终端高度时显示几条流水。
	//
	// 问不出来只发生在对面不是终端的时候——那种情况下重画本来就该退回滚动输出，
	// 走不到这里。留一个保守的数，是为了万一走到了也不会画出一屏塞不下的东西。
	fallbackLogLines = 8
	// 座位表那几列有多宽。名字超出就不补了：宁可那一行歪掉，也不能把名字切一半。
	seatNameWidth  = 12
	seatPosWidth   = 7
	seatStackWidth = 6
	// logNameWidth 是流水里「谁」那一列有多宽——名字加上括号里的位置。
	//
	// 它等于座位表的名字列加位置列，所以流水的动作列正好落在座位表的筹码列上。
	// 写成加法而不是写死 19，是为了让这层关系留在代码里：改上面任何一个数，
	// 对齐自己会跟着走，而不是等某天有人发现两块错开了才想起来还有这回事。
	logNameWidth = seatNameWidth + seatPosWidth
	// logActWidth 是「做了什么」那一列有多宽。超出的不截断，宁可那一行的底池往右顶，
	// 也不能把「全下 30（本轮共 30）」切一半。
	logActWidth = 14
	// streetRuleWidth 是街分隔线画多宽（列）。
	//
	// 不铺满整个终端：在一个 200 列的窗口里拉一条 200 列的横线，抢的注意力
	// 比它分隔的内容还多。这个数跟座位表那几行差不多宽，看着是一块的。
	streetRuleWidth = 46
	// roomyHeight 是「屏幕够高，可以拿几行出来留白」的门槛。
	//
	// 空行让几块内容分得开，读起来松快，但它是奢侈品：屏幕矮的时候这几行该让给
	// 真正的内容——一屏只剩八行还拿三行画空白，那是好看压过了好用。
	roomyHeight = 18
	// prevHandLines 是上一手留几行。留的是结尾——摊牌和分池在那儿，
	// 而「谁赢了多少」正是屏幕一清就看不见的那件事。
	prevHandLines = 4
	// minLogLines 是再挤也要留几条流水。
	//
	// 终端矮到连这几条都放不下时，宁可让它滚一下，也不能把「刚才发生了什么」删干净——
	// 那时屏幕上只剩一个静止的局面，人根本不知道牌是怎么走到这儿的。
	minLogLines = 2

	// actingMark 是「牌桌在等这个人」。座位表和流水末尾共用一个写法：
	// 同一件事在屏幕上有两处落点，写法一分岔，看的人就得先认出它们是一回事。
	actingMark = "行动中…"
)

// liveView 把「这一手正在发生什么」画成固定的一屏，就地重画，而不是往下滚（ADR-0017）。
//
// 它要攒状态，这跟 agent 那条路是两回事：agent 收到 your_turn 就有决策所需的一切，
// 可以完全无状态（ADR-0007）；但一个人在等别人行动的时候也想看见牌面和底池，
// 那就得把事件累积起来。累积的只是显示，权威永远在服务端（ADR-0003）——
// 所以凡是事件里带了权威数值的（座位、底池、公共牌），一律以事件为准覆盖，不自己推算。
type liveView struct {
	out io.Writer
	me  string

	blinds string
	seats  []poker.SeatView

	hand   int
	street string
	board  []poker.Card
	pot    int
	hole   []poker.Card
	log    []string

	// prev 是上一手牌的结尾几行，prevHand 是它的手数。
	//
	// 只留一手，而且留在重画区里——屏幕因此不会越长越高。留全部的话，实时帧
	// 就退化成了滚动流水账，而那正是这一版要避开的东西（ADR-0017）；
	// 想看全部有两条现成的路：--format=text，和手牌历史文件。
	prev     []string
	prevHand int

	// acting 是牌桌此刻在等谁行动，空串表示没人在行动。
	//
	// 这是服务端广播的 turn 事件告诉我们的，不是自己按座位顺序推出来的——
	// 客户端一旦开始自己算轮次，就会和服务端算出分歧（ADR-0003）。
	acting string
	// timeout 是这张桌一次行动的时限，deadline 是当前这个人的钟什么时候走完。
	// timeout 为 0（开桌时 --timeout 0）表示不限时，那就没有倒计时可画。
	timeout  time.Duration
	deadline time.Time
	// now 是这一版唯一的时间来源，测试里换成一个假钟。
	// 倒计时是屏幕上第一个「不靠事件推进」的东西，它必须能被断言，
	// 否则「倒计时会不会在不该重来的时候重来」这种错只能靠手看。
	now func() time.Time
	// turn 非空表示正轮到我，里面是服务端给的那份快照。
	turn *poker.Snapshot
	// note 是最近一条要提醒的话（错误、帮助之类），显示到下一条事件为止。
	note string
	over bool
	gone bool

	// up 是下一帧重画前要把光标往上挪几行。
	up int
	// shown 是屏幕上那一帧的每一行，repaint 拿它比出哪几行真变了。
	shown []string
}

func newLiveView(out io.Writer, me string) *liveView {
	return &liveView{out: out, me: me, now: time.Now}
}

// spacer 是块与块之间的那个空行，屏幕不够高时它什么都不占。
func (v *liveView) spacer() string {
	if h := textui.Height(v.out); h > 0 && h < roomyHeight {
		return ""
	}
	return "\n"
}

// 下面四个方法是 view 那套接口，把「攒状态 + 重画」接到 Play 的主循环上。

func (v *liveView) start() { v.render() }

func (v *liveView) event(ev poker.Event, _ []byte) error {
	v.apply(ev)
	// 事件也走这条路：别人行动时你正在打字，帧高没变的话就没必要把你打的字冲掉。
	// 帧高一变（多了一条流水，通常如此）它自己会退回整帧重画。
	v.repaint()
	return nil
}

func (v *liveView) notice(s string) { v.note = s }

func (v *liveView) refresh() { v.render() }

// tick 是秒针。只有屏幕上真有一个在走的数字时才重画——没有的话，
// 每秒一帧纯属往终端里灌字节，而对着管道时那还是一堆转义序列。
func (v *liveView) tick() {
	if v.deadline.IsZero() || v.acting == "" || v.acting == v.me || v.gone {
		return
	}
	v.repaint()
}

func (v *liveView) disconnected() {
	v.gone, v.turn = true, nil
	v.render()
	// 把光标挪出这一帧，免得后面 shell 的提示符压在最后一行上。
	fmt.Fprintln(v.out)
}

// apply 把一条事件并进视图。
func (v *liveView) apply(ev poker.Event) {
	// 提醒只活到下一条事件为止。轮到你的时候不会有任何事件进来，所以
	// 「动作非法」那条错误会一直挂着，直到你真的做出一个合法动作。
	v.note = ""

	if len(ev.Seats) > 0 {
		v.seats = ev.Seats
	}
	if ev.Pot != nil {
		v.pot = *ev.Pot
	}
	if ev.Blinds != "" {
		v.blinds = ev.Blinds
	}

	switch ev.Type {
	case poker.EventTable:
		v.timeout = time.Duration(ev.TimeoutMS) * time.Millisecond
	case poker.EventHandStart:
		// 把刚打完那一手的结尾留一份，其余清掉——这正是「只展示正在玩的」那句话的落点。
		v.prev, v.prevHand = tailOf(v.log, prevHandLines), v.hand
		// 底池也在内：hand_start 不带底池，不清的话上一手的数字会一直挂着，
		// 直到第一个盲注把它盖掉，中间那几帧是在说谎。
		v.hand, v.street = ev.Hand, "preflop"
		v.board, v.hole, v.log = nil, nil, nil
		v.turn, v.over, v.note = nil, false, ""
		v.acting, v.deadline = "", time.Time{}
		v.pot = 0
		if ev.Pot != nil {
			v.pot = *ev.Pot
		}
		// 翻牌前也来一条分隔线。四条街只有它没有的话，盲注和动作就直接贴在
		// 上一手的记录底下，一眼看不出这手牌是从哪儿开始的。
		v.addLog("%s", v.streetRule(streetName("preflop"), nil))
	case poker.EventBlind:
		name := "小盲"
		if ev.Action == "big_blind" {
			name = "大盲"
		}
		v.addLog("%s", logLine(v.who(ev.Player), fmt.Sprintf("%s %d", name, ev.Amount), ""))
	case poker.EventHoleCards:
		// 只认自己那一份。服务端本来就只会把底牌投递给它的主人（ADR-0006），
		// 但这是一个会一直画在屏幕上的状态：不在这里认一次人，可见性就多了一个
		// 只靠「服务端不会那么干」撑着的落点。
		if ev.Player == v.me {
			v.hole = ev.Cards
		}
	case poker.EventTurn:
		// 只有换人了才重新计时。同一个人的 turn 会再来一遍——他发了个非法动作，
		// 服务端把合法动作表重新告诉他一次。而服务端那边的钟在这种时候是不重置的
		// （不然一个只会发非法动作的客户端就能无限拖下去），屏幕得跟它说同一件事。
		if ev.Player != v.acting {
			v.deadline = time.Time{}
			if v.timeout > 0 {
				v.deadline = v.now().Add(v.timeout)
			}
		}
		v.acting = ev.Player
	case poker.EventYourTurn:
		v.turn = ev.Snapshot
		if ev.Snapshot != nil {
			v.street = ev.Snapshot.Street
			v.board = ev.Snapshot.Community
			v.pot = ev.Snapshot.Pot
			v.hole = ev.Snapshot.Hole
			v.seats = ev.Snapshot.Seats
		}
	case poker.EventAction:
		// 他动过了，牌桌不再等他。下一条 turn 会说出在等谁。
		v.turn, v.acting, v.deadline = nil, "", time.Time{}
		v.addLog("%s", logLine(v.who(ev.Player), actionWhat(ev), actionPot(ev)))
		v.patchSeat(ev)
	case poker.EventStreet:
		v.street, v.board = ev.Street, ev.Board
		v.addLog("%s", v.streetRule(streetName(ev.Street), ev.Cards))
	case poker.EventShowdown:
		for _, e := range ev.Showdown {
			v.addLog("%s", logLine(v.who(e.Player), textui.Cards(e.Cards), "→ "+e.Category))
		}
	case poker.EventPotAwarded:
		for i, pot := range ev.Pots {
			label := "底池"
			if i > 0 {
				label = fmt.Sprintf("边池 %d", i)
			}
			v.addLog("%s %d → %s", label, pot.Amount, strings.Join(pot.Winners, "、"))
		}
	case poker.EventHandEnd:
		v.over, v.turn, v.acting, v.deadline = true, nil, "", time.Time{}
	case poker.EventError:
		v.note = fmt.Sprintf("✗ [%s] %s", ev.Code, ev.Message)
	case poker.EventJoined, poker.EventLeft, poker.EventSitOut, poker.EventSitIn, poker.EventTopUp:
		v.note = Render(ev)
	}
}

// patchSeat 把一条动作事件里那个人的筹码更新上去。
//
// action 事件只带动作者一个人的数字，不带整桌——所以这里是「补一格」，不是覆盖全表。
func (v *liveView) patchSeat(ev poker.Event) {
	if ev.Stack == nil {
		return
	}
	seats := append([]poker.SeatView(nil), v.seats...)
	for i := range seats {
		if seats[i].Player != ev.Player {
			continue
		}
		seats[i].Stack = *ev.Stack
		seats[i].Committed = ev.Committed
		if ev.Action == "fold" {
			seats[i].Folded = true
		}
		if *ev.Stack == 0 && ev.Action == "allin" {
			seats[i].AllIn = true
		}
	}
	v.seats = seats
}

// who 是流水里「谁」那一列：名字后面用括号带上他这手牌的位置。
//
// 位置是德扑里最要紧的那个变量——同样两张牌，在 BTN 和在 UTG 是两手完全不同的牌。
// 座位表上有，但看流水时眼睛在下半屏，来回对照太累；而这一列本来就有地方放。
// 两手牌之间没有庄家位，也就没有位置可言，那时候只写名字。
func (v *liveView) who(name string) string {
	for _, s := range v.seats {
		if s.Player == name && s.Position != "" {
			return name + " (" + s.Position + ")"
		}
	}
	return name
}

// logLine 把流水排成三列：谁、做了什么、底池。
//
// 不补齐的话人名长短不一，「弃牌」「下注到 30」这些就各自从不同的列开始，
// 一眼扫下来看不出谁做了什么——而看流水本来就是在扫，不是在读。
func logLine(who, what, pot string) string {
	line := textui.Pad(who, logNameWidth) + textui.Pad(what, logActWidth) + pot
	// 没有底池那一列时，别在行尾留一串看不见的空格。
	return strings.TrimRight(line, " ")
}

// streetRule 画一条带街名和新翻开的牌的分隔线，用来把前后两条街的动作断开。
//
// 一条街打完进下一条，是这手牌里最需要一眼看见的断点——上一条街的下注全部结清，
// 牌面变了，重新开始说话。只在前面点两个横杠不够显眼，所以这里补到一整行。
//
// 宽度要看终端：画过头了会折行，而折了一行，「上移 N 行」就再也对不上了。
func (v *liveView) streetRule(name string, dealt []poker.Card) string {
	// 翻牌前没有新牌翻开，那就别画一个占位的短横——「──── 翻牌前 - ────」
	// 里那个横杠会让人以为牌面上有什么东西。
	label := fmt.Sprintf(" %s ", name)
	if len(dealt) > 0 {
		label = fmt.Sprintf(" %s %s ", name, textui.Cards(dealt))
	}
	lead := "────"
	width := streetRuleWidth
	// 减 2 是 frame 给每条流水加的那两格缩进。
	if c := textui.Cols(v.out) - 2; c > 0 && c < width {
		width = c
	}
	gap := width - textui.Width(lead) - textui.Width(label)
	if gap <= 0 {
		// 一根横线都补不下了。label 尾上那个空格是用来跟横线隔开的，没横线就不该占位——
		// 窄终端上这一格正好是折不折行的分界。
		return textui.Dim(lead) + strings.TrimRight(label, " ")
	}
	// 只把横线调暗：它是分隔符，不是内容。牌和街名不能跟着暗下去，
	// 而且它们自带颜色，包进同一层样式里会被牌尾那个复位打断。
	return textui.Dim(lead) + label + textui.Dim(strings.Repeat("─", gap))
}

func (v *liveView) addLog(format string, args ...any) {
	v.log = append(v.log, fmt.Sprintf(format, args...))
	if len(v.log) > logCap {
		v.log = v.log[len(v.log)-logCap:]
	}
}

// typed 告诉视图：用户敲了回车，终端把那一行回显之后换了行。
//
// 不记这一笔的话，下一帧上移的行数就少一行，屏幕上会留下一条越积越多的残渣。
func (v *liveView) typed() {
	if v.up > 0 {
		v.up++
	}
}

// render 就地重画一帧。
func (v *liveView) render() {
	frame := v.frame()
	var b strings.Builder
	if v.up > 0 {
		fmt.Fprintf(&b, "\033[%dA", v.up) // 回到上一帧的开头
	}
	b.WriteString("\r\033[J") // 光标移到行首，清掉底下所有旧内容
	b.WriteString(frame)
	fmt.Fprint(v.out, b.String())
	v.up = strings.Count(frame, "\n")
	v.shown = strings.Split(frame, "\n")
}

// repaint 只重写变了的那几行，不碰输入行。
//
// 整帧重画会连带抹掉终端刚回显出来的半行命令——你正在敲的东西就这么没了。
// 以前这只在别人行动时偶尔发生一次，倒计时进来之后变成了每秒一次，不能再将就。
// 所以这条路先把光标存起来（它此刻停在输入行上，列数只有终端自己知道），
// 挪上去把变了的那几行重写一遍，再放回原处：输入行一个字节都没动过。
//
// 只在帧高没变时走得通——变了的话下面的行要整体挪位置，那是整帧重画的活。
// 另一个前提是光标确实停在最后一行上：敲的字多到把输入行挤到换行时就不成立了，
// 那时候这一帧会画歪，等下一条事件整帧重画才正过来。`typed` 数行数时是同一个假设。
func (v *liveView) repaint() {
	if v.up == 0 || v.shown == nil {
		v.render()
		return
	}
	next := strings.Split(v.frame(), "\n")
	if len(next) != len(v.shown) {
		v.render()
		return
	}

	var b strings.Builder
	b.WriteString("\0337") // 存光标
	row, moved := len(next)-1, false
	for i, l := range next {
		if l == v.shown[i] {
			continue
		}
		if d := row - i; d > 0 {
			fmt.Fprintf(&b, "\033[%dA", d)
		} else if d < 0 {
			fmt.Fprintf(&b, "\033[%dB", -d)
		}
		row, moved = i, true
		// \033[K 清到行尾：新的一行比旧的短时，旧的尾巴不能留在那儿。
		fmt.Fprintf(&b, "\r%s\033[K", l)
	}
	if !moved {
		return
	}
	b.WriteString("\0338") // 放回去
	fmt.Fprint(v.out, b.String())
	v.shown = next
}

// frame 画出当前这一帧。最后一行是输入提示，不换行——光标就停在它后面。
//
// 一帧必须塞得进一屏：塞不进时终端会滚动，而滚动之后「上移 N 行」回到的就不是
// 原来那个位置，屏幕会一路烂下去。以前靠一个拍脑袋的常数压着，现在直接问终端多高，
// 把除流水之外的部分先排好，剩下几行就显示几条流水。
func (v *liveView) frame() string {
	prev := v.prevBlock()
	head, fixed := v.headAndSeats(), v.tail()
	budget := v.logBudget(strings.Count(head, "\n") + strings.Count(fixed, "\n") + strings.Count(prev, "\n"))

	log := v.log
	// 牌桌在等谁，也排进流水的末尾。座位表上那个标记要先找到他在哪一行才看得见，
	// 而眼睛在等的时候盯的是流水最后一行——下一条记录会从哪儿冒出来。
	if pending := v.pendingRow(); pending != "" {
		log = append(append([]string(nil), log...), pending)
	}
	if len(log) > budget {
		log = log[len(log)-budget:]
	}
	var b strings.Builder
	b.WriteString(prev)
	b.WriteString(head)
	for _, l := range log {
		fmt.Fprintf(&b, "  %s\n", l)
	}
	b.WriteString(fixed)
	return b.String()
}

// shortDur 把时限写成人话：30s、1m30s、500ms。
func shortDur(d time.Duration) string {
	if d >= time.Second && d%time.Second == 0 {
		return d.String()
	}
	return d.Round(time.Millisecond).String()
}

// actingLabel 是「行动中…」，后面跟上这个人还剩多少时间。
//
// 倒计时只画给别人。轮到自己时屏幕不会每秒重画——重画会把终端刚回显出来的
// 那半行命令抹掉，而你正在敲它。那时候画一个不动的数字比不画更糟：它看着像
// 在走，其实停在你开始打字的那一刻（还剩多少时间改由 tail 那行静态地说）。
func (v *liveView) actingLabel() string {
	if v.deadline.IsZero() || v.acting == v.me {
		return actingMark
	}
	left := v.deadline.Sub(v.now())
	if left < 0 {
		left = 0
	}
	// 向上取整：还剩 0.2 秒时显示 1s，走到 0 才是 0s。显示 0s 却还能动，
	// 比显示 1s 却已经代打了要好解释。
	return fmt.Sprintf("%s %ds", actingMark, (left.Milliseconds()+999)/1000)
}

// pendingRow 是流水末尾那一行：牌桌在等谁。
//
// 它跟上面那些不是一回事——上面每一行都是已经发生的事，这一行是还没发生的。
// 所以整行调暗：位置在流水里，分量不在。调暗也只能整行来，这一行没有别的样式，
// 不会撞上「牌带颜色，包一层就被牌尾那个复位打断」那个坑。
//
// 它不进 v.log。流水是这手牌的记录，而「在等谁」下一条事件就翻篇了；
// 混进去的话，上一手留下的结尾会以「轮到某某」收尾，那是一句永远不会兑现的话。
func (v *liveView) pendingRow() string {
	if v.acting == "" {
		return ""
	}
	return textui.Dim(logLine(v.who(v.acting), v.actingLabel(), ""))
}

// logBudget 算出这一帧还能放几条流水。fixed 是除流水外已经占掉的行数。
func (v *liveView) logBudget(fixed int) int {
	h := textui.Height(v.out)
	if h <= 0 {
		// 问不出高度说明对面不是终端，重画本来就不该走到这里。
		return fallbackLogLines
	}
	// fixed 数的是换行符，而最后一个换行符之后光标还停在一行上——那一行也得占个位置。
	// 断开时再多留一行：那一帧末尾还会多打一个换行，把光标挪出这一屏。
	reserve := 1
	if v.gone {
		reserve = 2
	}
	if n := h - fixed - reserve; n > minLogLines {
		return n
	}
	return minLogLines
}

// prevBlock 是屏幕最上面那一小块：上一手的结尾。
//
// 一手打完到下一手开始只隔 --hand-delay，屏幕一清，「谁赢了这个底池」就没了。
// 留结尾而不是留开头：摊牌和分池在结尾，而那才是你回头想看的东西。
func (v *liveView) prevBlock() string {
	if len(v.prev) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", textui.Dim(fmt.Sprintf("上一手（第 %d 手）", v.prevHand)))
	for _, l := range v.prev {
		fmt.Fprintf(&b, "  %s\n", l)
	}
	b.WriteString(v.spacer())
	return b.String()
}

// tailOf 取一个切片的最后 n 项。
func tailOf(xs []string, n int) []string {
	if len(xs) <= n {
		return append([]string(nil), xs...)
	}
	return append([]string(nil), xs[len(xs)-n:]...)
}

func (v *liveView) headAndSeats() string {
	var b strings.Builder

	head := "等人来凑一桌…"
	if v.hand > 0 {
		head = fmt.Sprintf("第 %d 手", v.hand)
		if v.street != "" {
			head += "  " + streetName(v.street)
		}
	}
	if v.blinds != "" {
		head += "  盲注 " + v.blinds
	}
	fmt.Fprintf(&b, "%s\n%s", textui.Bold(head), v.spacer())

	for _, s := range v.seats {
		marker := "  "
		if v.turn != nil && s.Player == v.me {
			marker = "▶ "
		}
		line := marker + textui.Pad(s.Player, seatNameWidth) + textui.Pad(s.Position, seatPosWidth) +
			textui.PadLeft(fmt.Sprintf("%d", s.Stack), seatStackWidth)
		switch {
		case s.Folded:
			line += "  弃"
		case s.AllIn:
			line += "  全下"
		case s.SittingOut:
			line += "  暂离"
		case s.Committed > 0:
			line += fmt.Sprintf("  投 %d", s.Committed)
		}
		if s.Player == v.me && len(v.hole) > 0 {
			line += "   " + textui.Cards(v.hole)
		}
		// ▶ 已经在这一行说过同一件事了。同一行标两遍不会更醒目，只是把
		// 本来就挤的一行又撑宽一截——而这一行还要塞位置、筹码、投入和底牌。
		if s.Player == v.acting && marker != "▶ " {
			line += "   " + v.actingLabel()
		}
		fmt.Fprintf(&b, "%s\n", line)
	}

	if len(v.board) > 0 || v.pot > 0 {
		fmt.Fprintf(&b, "%s公共牌 %s    底池 %d\n", v.spacer(), textui.Cards(v.board), v.pot)
	}
	b.WriteString(v.spacer())
	return b.String()
}

// tail 是流水底下那几行：提醒、轮次、输入提示。
func (v *liveView) tail() string {
	var b strings.Builder
	b.WriteString(v.spacer())
	if v.note != "" {
		fmt.Fprintf(&b, "%s\n", v.note)
	}

	switch {
	case v.gone:
		// 断开之后不再画输入提示：那一行还在的话，看起来像还能敲命令。
		b.WriteString("与牌桌的连接已断开。\n")
		return b.String()
	case v.turn != nil:
		// 轮到自己时只说时限，不说还剩多少——这一屏在你打字的时候不重画，
		// 一个不会走的「还剩 12 秒」会一直停在 12 秒上。
		head := "轮到你了" + toCallSuffix(v.turn)
		if v.timeout > 0 {
			head += fmt.Sprintf("    限时 %s", shortDur(v.timeout))
		}
		fmt.Fprintf(&b, "%s\n%s\n", textui.Bold(head), legalLine(v.turn))
	case v.over:
		b.WriteString("这手牌结束了，等下一手…\n")
	}
	b.WriteString("> ")
	return b.String()
}

func toCallSuffix(snap *poker.Snapshot) string {
	if snap.ToCall > 0 {
		return fmt.Sprintf("    要跟 %d", snap.ToCall)
	}
	return ""
}

func legalLine(snap *poker.Snapshot) string {
	opts := make([]string, 0, len(snap.Legal))
	for _, l := range snap.Legal {
		switch l.Action {
		case "call":
			opts = append(opts, fmt.Sprintf("call（跟 %d）", l.Amount))
		case "bet":
			opts = append(opts, fmt.Sprintf("bet <%d-%d>", l.Min, l.Max))
		case "allin":
			opts = append(opts, fmt.Sprintf("allin（推 %d）", l.Amount))
		default:
			opts = append(opts, l.Action)
		}
	}
	return "可以：" + strings.Join(opts, " / ")
}
