// Package client 连上一张牌桌，把事件流交给人或机器人。
//
// 人类客户端和机器人客户端共用这里的 Session：同一条连接、同一份 JSONL 事件流、
// 同一条代码路径（ADR-0002、ADR-0010）。两者的差别只在拿到 your_turn 之后怎么决定。
package client

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/protocol"
	"github.com/xujnan/poker-cli/internal/transport"
)

// Session 是一条到牌桌的连接。
type Session struct {
	conn   net.Conn
	events *protocol.EventReader
	name   string
}

// Dial 连上 Table Code 对应的牌桌并报上名字与带入。
//
// 「怎么连上」由 tr 决定（ADR-0001）：同机是拼一个 socket 路径，跨机会是别的。
// 客户端这边只知道自己拿到了一条 net.Conn。
func Dial(tr transport.Transport, code, name string, buyin int) (*Session, error) {
	conn, err := tr.Dial(code)
	if err != nil {
		return nil, err
	}
	// 名字即身份，没有握手也没有凭据（ADR-0009）。
	if err := protocol.WriteCommand(conn, protocol.Command{Type: protocol.CmdJoin, Name: name, Buyin: buyin}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("client: 加入牌桌失败: %w", err)
	}
	return &Session{conn: conn, events: protocol.NewEventReader(conn), name: name}, nil
}

// Name 返回自己在牌桌上的名字。
func (s *Session) Name() string { return s.name }

// Next 读下一条事件，同时给出服务端发来的原始那一行。
func (s *Session) Next() (poker.Event, []byte, error) { return s.events.Next() }

// Send 发一条命令给服务端。
func (s *Session) Send(cmd protocol.Command) error { return protocol.WriteCommand(s.conn, cmd) }

// Close 断开连接。断线在服务端看来就是 Sitting Out，筹码留在座位上。
func (s *Session) Close() error { return s.conn.Close() }

// Play 跑人类客户端：把事件渲染出来，同时从 in 读命令。
//
// in 为 nil 时只看不玩。format 决定渲染方式，三种格式读的是同一份事件（ADR-0002）。
// rebuy 为真时，输光了就自动补回最初的带入。
//
// 事件和用户输入在这里汇成一个 select 循环，两边各有一个 goroutine 只负责「读」。
// 呈现全都发生在这条主循环上，一次一件——重画那一版非得如此不可：它要数自己上一帧
// 有多少行才能把光标挪回去，而并发的两路输出会把这个数搅乱，屏幕上就会留下残渣。
func Play(s *Session, format string, out io.Writer, in io.Reader, rebuy bool) error {
	v := newView(format, out, s.Name())
	done := make(chan struct{})
	defer close(done)

	events := make(chan eventMsg, 8)
	go func() {
		for {
			ev, raw, err := s.Next()
			select {
			case events <- eventMsg{ev: ev, raw: raw, err: err}:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var lines chan string
	if in != nil {
		lines = make(chan string)
		go func() {
			sc := bufio.NewScanner(in)
			for sc.Scan() {
				select {
				case lines <- sc.Text():
				case <-done:
					return
				}
			}
			// 标准输入关了（比如管道结束），但牌桌可能还在打，继续看着就行。
			close(lines)
		}()
	}

	buy := rebuyer{name: s.Name(), enabled: rebuy}
	v.start()
	for {
		select {
		case m := <-events:
			if m.err != nil {
				if errors.Is(m.err, io.EOF) || errors.Is(m.err, net.ErrClosed) {
					v.disconnected()
					return nil
				}
				return m.err
			}
			if cmd, ok := buy.observe(m.ev); ok {
				if err := s.Send(cmd); err != nil {
					return err
				}
			}
			if err := v.event(m.ev, m.raw); err != nil {
				return err
			}
		case line, ok := <-lines:
			if !ok {
				// 置 nil 之后这条 case 就永远不会被选中了。
				lines = nil
				continue
			}
			// 终端已经把这一行连同换行一起回显出来了，重画那一版得知道这件事。
			v.typed()
			handleLine(s, v, line)
			v.refresh()
		}
	}
}

// eventMsg 是读事件那个 goroutine 送回主循环的一条消息，err 非空表示流到头了。
type eventMsg struct {
	ev  poker.Event
	raw []byte
	err error
}

// handleLine 处理用户敲的一行。
//
// quit 不在这里返回：它发完命令就把连接关掉，剩下的交给事件那一路——读到
// net.ErrClosed，走和对面挂掉完全一样的收尾。两条退出路径合成一条，少一处能分岔的地方。
func handleLine(s *Session, v view, line string) {
	line = strings.TrimSpace(line)
	switch line {
	case "":
		return
	case "quit", "exit":
		_ = s.Send(protocol.Command{Type: protocol.CmdQuit})
		_ = s.Close()
		return
	case "help":
		v.notice(helpText)
		return
	case "sitout":
		send(s, v, protocol.Command{Type: protocol.CmdSitOut})
		return
	case "sitin":
		send(s, v, protocol.Command{Type: protocol.CmdSitIn})
		return
	}
	if rest, ok := strings.CutPrefix(line, "topup"); ok {
		amount, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil || amount <= 0 {
			v.notice("topup 要跟一个正数额，比如 topup 200")
			return
		}
		send(s, v, protocol.Command{Type: protocol.CmdTopUp, Amount: amount})
		return
	}
	action, err := poker.ParseAction(line)
	if err != nil {
		v.notice(err.Error())
		return
	}
	send(s, v, protocol.CommandOf(action))
}

func send(s *Session, v view, cmd protocol.Command) {
	if err := s.Send(cmd); err != nil {
		v.notice(fmt.Sprintf("发不出去：%v", err))
	}
}

const helpText = `可用命令：
  fold          弃牌
  check         过牌
  call          跟注
  bet <数额>    把本轮总投入推到这个数（不是「再加」这么多）
  allin         推光
  topup <数额>  补码。随时能发，下一手牌开始前到账
  sitout        暂离，这手牌打完生效；座位和筹码都留着
  sitin         回座
  help / quit`

// RunBot 跑一个机器人客户端。
//
// 它是一个独立进程，走的是同一个传输、同一份事件流，跟外部 AI agent
// 一模一样（ADR-0010）。这意味着每一次本地对战都在回归测试 agent 接口本身——
// 千万别为了省事把它改成服务端里的一个 goroutine，那样它天然能看到所有人的底牌，
// ADR-0006 那条不变量对它就不成立了。
func RunBot(s *Session, out io.Writer, rebuy bool) error {
	return RunBotWith(s, out, rebuy, Decide)
}

// RunBotWith 跟 RunBot 一样，只是换一个大脑。
//
// 分出这一层是为了让测试能把「怎么决策」和「输光了怎么补码」拆开。补码那条路
// 是要测的东西，决策那条路却会让测试变成赌骰子：两个策略一样的机器人对打，
// 浅筹码那个人的筹码是个鞅，「他会输光」这件事在任何固定时限里都不保证发生——
// 一条 17% 会挂的测试，比没有测试更糟。换成只弃牌的大脑，输光就成了必然。
//
// 将来做 agent 评测时，「同一套接口换不同的大脑」也正是要的形状。
func RunBotWith(s *Session, out io.Writer, rebuy bool, decide func(*poker.Snapshot) poker.Action) error {
	buy := rebuyer{name: s.Name(), enabled: rebuy}
	for {
		ev, _, err := s.Next()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if line := Render(ev); line != "" {
			fmt.Fprintln(out, line)
		}
		if cmd, ok := buy.observe(ev); ok {
			if err := s.Send(cmd); err != nil {
				return err
			}
			continue
		}
		if ev.Type != poker.EventYourTurn || ev.Snapshot == nil {
			continue
		}
		action := decide(ev.Snapshot)
		fmt.Fprintf(out, "→ %s\n", action)
		if err := s.Send(protocol.CommandOf(action)); err != nil {
			return err
		}
	}
}

// rebuyer 盯着自己的筹码，输光了就补回最初的带入。
//
// 不做这件事的话，一张无人值守的牌桌迟早会因为有人破产而永远停住——
// 破产的人自动 Sitting Out，剩下不到两个人，就再也没有下一手了。
type rebuyer struct {
	name    string
	enabled bool
	// target 是第一次看到自己时的筹码，也就是最初的带入。
	target int
	// waiting 表示已经发过补码、还没等到到账，免得同一次破产连发好几条。
	waiting bool
}

func (r *rebuyer) observe(ev poker.Event) (protocol.Command, bool) {
	if !r.enabled {
		return protocol.Command{}, false
	}
	if ev.Type == poker.EventTopUp && ev.Player == r.name {
		r.waiting = false
		return protocol.Command{}, false
	}
	// 只看两手牌之间的那几类事件。
	//
	// 别的事件也带座位表，但牌局中途的那些里，「筹码 0」的意思是全下——他还在这手牌里，
	// 底池说不定就是他的。照着那个补码，等于没破产也白拿一笔。
	switch ev.Type {
	case poker.EventTable, poker.EventHandEnd, poker.EventSitOut:
	default:
		return protocol.Command{}, false
	}
	for _, sv := range ev.Seats {
		if sv.Player != r.name {
			continue
		}
		if r.target == 0 && sv.Stack > 0 {
			r.target = sv.Stack
		}
		if sv.Stack == 0 && r.target > 0 && !r.waiting {
			r.waiting = true
			return protocol.Command{Type: protocol.CmdTopUp, Amount: r.target}, true
		}
	}
	return protocol.Command{}, false
}

// Decide 是机器人的全部大脑：一个够笨但不会乱来的策略。
//
// 它只读 your_turn 里那份快照，不自己记牌、不累积状态——这正是 ADR-0007 里
// 「agent 可以完全无状态」那句话的意思。它也只从快照给出的合法动作列表里选，
// 从不自己推导此刻能不能 check，所以它永远不会收到一条非法动作的错误。
//
// 打得好不好不是重点。重点是每次本地对战都有一个真实的 agent 在走这条接口。
func Decide(snap *poker.Snapshot) poker.Action {
	canCheck := hasLegal(snap, "check")
	bet, canBet := legalOf(snap, "bet")

	switch strength(snap) {
	case strong:
		// 只在没人下注的时候主动下注；别人已经下了就跟，不再加回去。
		//
		// 「强牌就加注到最小加注额」听起来没问题，直到两个这样的机器人在同一条街上
		// 都拿到强牌：A 加到 4，B 加到 6，A 加到 8……一路对加到有人推光为止。
		// 规则上完全合法——无限注本来就没有加注上限，状态机照单全收——但那不是在打牌。
		// 不再加回去这条，让每条街最多一轮下注就收口。
		if snap.ToCall == 0 {
			if canBet {
				return poker.Action{Kind: poker.BetTo, Amount: bet.Min}
			}
			return poker.Action{Kind: poker.Check}
		}
		return poker.Action{Kind: poker.Call}
	case medium:
		if canCheck {
			return poker.Action{Kind: poker.Check}
		}
		// 便宜就跟，贵了就走。八分之一的筹码是随手定的一条线，不是什么策略洞见。
		if snap.ToCall*8 <= snap.Stack {
			return poker.Action{Kind: poker.Call}
		}
		return poker.Action{Kind: poker.Fold}
	default:
		if canCheck {
			return poker.Action{Kind: poker.Check}
		}
		return poker.Action{Kind: poker.Fold}
	}
}

type handStrength int

const (
	weak handStrength = iota
	medium
	strong
)

// strength 给当前局面一个粗糙的三档评价。
func strength(snap *poker.Snapshot) handStrength {
	cards := append(append([]poker.Card(nil), snap.Hole...), snap.Community...)
	if len(cards) >= 5 {
		// 翻牌之后就有五张牌可评了，直接用真正的牌力评估器。
		rank := poker.Evaluate(cards)
		switch {
		case rank.Category >= poker.TwoPair:
			return strong
		case rank.Category == poker.OnePair:
			return medium
		default:
			return weak
		}
	}
	// preflop 只有两张底牌，评估器用不上，只好看牌面。
	if len(snap.Hole) < 2 {
		return weak
	}
	a, b := snap.Hole[0].Rank, snap.Hole[1].Rank
	switch {
	case a == b:
		return strong
	case a >= poker.Ten && b >= poker.Ten:
		return medium
	default:
		return weak
	}
}

func hasLegal(snap *poker.Snapshot, action string) bool {
	_, ok := legalOf(snap, action)
	return ok
}

func legalOf(snap *poker.Snapshot, action string) (poker.LegalAction, bool) {
	for _, l := range snap.Legal {
		if l.Action == action {
			return l, true
		}
	}
	return poker.LegalAction{}, false
}
