package client

import (
	"fmt"
	"io"
	"strings"

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
	// logNameWidth 是流水里人名那一列有多宽。
	//
	// 跟座位表那一列同宽是故意的：两块东西上下叠着，用同一条竖线对齐，
	// 整帧看着才是一张表，而不是几段各自为政的文字。名字超出这个宽度就不补了，
	// 宁可那一行歪掉，也不能把名字切一半。
	logNameWidth = 12
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
	// minLogLines 是再挤也要留几条流水。
	//
	// 终端矮到连这几条都放不下时，宁可让它滚一下，也不能把「刚才发生了什么」删干净——
	// 那时屏幕上只剩一个静止的局面，人根本不知道牌是怎么走到这儿的。
	minLogLines = 2
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

	// turn 非空表示正轮到我，里面是服务端给的那份快照。
	turn *poker.Snapshot
	// note 是最近一条要提醒的话（错误、帮助之类），显示到下一条事件为止。
	note string
	over bool
	gone bool

	// up 是下一帧重画前要把光标往上挪几行。
	up int
}

func newLiveView(out io.Writer, me string) *liveView {
	return &liveView{out: out, me: me}
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
	v.render()
	return nil
}

func (v *liveView) notice(s string) { v.note = s }

func (v *liveView) refresh() { v.render() }

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
	case poker.EventHandStart:
		// 新的一手，把上一手的东西全清掉——这正是「只展示正在玩的」那句话的落点。
		// 底池也在内：hand_start 不带底池，不清的话上一手的数字会一直挂着，
		// 直到第一个盲注把它盖掉，中间那几帧是在说谎。
		v.hand, v.street = ev.Hand, "preflop"
		v.board, v.hole, v.log = nil, nil, nil
		v.turn, v.over, v.note = nil, false, ""
		v.pot = 0
		if ev.Pot != nil {
			v.pot = *ev.Pot
		}
	case poker.EventBlind:
		name := "小盲"
		if ev.Action == "big_blind" {
			name = "大盲"
		}
		v.addLog("%s", logLine(ev.Player, fmt.Sprintf("%s %d", name, ev.Amount), ""))
	case poker.EventHoleCards:
		// 只认自己那一份。服务端本来就只会把底牌投递给它的主人（ADR-0006），
		// 但这是一个会一直画在屏幕上的状态：不在这里认一次人，可见性就多了一个
		// 只靠「服务端不会那么干」撑着的落点。
		if ev.Player == v.me {
			v.hole = ev.Cards
		}
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
		v.turn = nil
		v.addLog("%s", logLine(ev.Player, actionWhat(ev), actionPot(ev)))
		v.patchSeat(ev)
	case poker.EventStreet:
		v.street, v.board = ev.Street, ev.Board
		v.addLog("%s", v.streetRule(streetName(ev.Street), ev.Cards))
	case poker.EventShowdown:
		for _, e := range ev.Showdown {
			v.addLog("%s", logLine(e.Player, textui.Cards(e.Cards), "→ "+e.Category))
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
		v.over, v.turn = true, nil
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

// logLine 把流水排成两列：谁，做了什么。
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
	lead, label := "────", fmt.Sprintf(" %s %s ", name, textui.Cards(dealt))
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
}

// frame 画出当前这一帧。最后一行是输入提示，不换行——光标就停在它后面。
//
// 一帧必须塞得进一屏：塞不进时终端会滚动，而滚动之后「上移 N 行」回到的就不是
// 原来那个位置，屏幕会一路烂下去。以前靠一个拍脑袋的常数压着，现在直接问终端多高，
// 把除流水之外的部分先排好，剩下几行就显示几条流水。
func (v *liveView) frame() string {
	head, fixed := v.headAndSeats(), v.tail()
	budget := v.logBudget(strings.Count(head, "\n") + strings.Count(fixed, "\n"))

	log := v.log
	if len(log) > budget {
		log = log[len(log)-budget:]
	}
	var b strings.Builder
	b.WriteString(head)
	for _, l := range log {
		fmt.Fprintf(&b, "  %s\n", l)
	}
	b.WriteString(fixed)
	return b.String()
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
		line := marker + textui.Pad(s.Player, 12) + textui.Pad(s.Position, 7) +
			textui.PadLeft(fmt.Sprintf("%d", s.Stack), 6)
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
		fmt.Fprintf(&b, "%s\n%s\n", textui.Bold("轮到你了"+toCallSuffix(v.turn)), legalLine(v.turn))
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
