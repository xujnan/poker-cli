// Package client 连上一张牌桌，把事件流交给人或机器人。
//
// 人类客户端和机器人客户端共用这里的 Session：同一个 socket、同一份 JSONL 事件流、
// 同一条代码路径（ADR-0002、ADR-0010）。两者的差别只在拿到 your_turn 之后怎么决定。
package client

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/protocol"
)

// 输出格式（ADR-0002）。
const (
	FormatText  = "text"
	FormatJSONL = "jsonl"
)

// Session 是一条到牌桌的连接。
type Session struct {
	conn   net.Conn
	events *protocol.EventReader
	name   string
}

// Dial 连上 Table Code 对应的牌桌并报上名字与带入。
//
// 「加入牌桌」在这里就是拼一次路径再连一次 socket——没有服务发现这一步（ADR-0013）。
func Dial(dir, code, name string, buyin int) (*Session, error) {
	if dir == "" {
		d, err := protocol.DefaultDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	path, err := protocol.SocketPath(dir, code)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("client: 连不上牌桌 %s（%s）：%w", strings.ToUpper(code), path, err)
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
// in 为 nil 时只看不玩。format 决定渲染方式，两种格式读的是同一份事件（ADR-0002）。
func Play(s *Session, format string, out io.Writer, in io.Reader) error {
	if in != nil {
		go readCommands(s, in, out)
	}
	for {
		ev, raw, err := s.Next()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				if format == FormatText {
					fmt.Fprintln(out, "与牌桌的连接已断开。")
				}
				return nil
			}
			return err
		}
		if format == FormatJSONL {
			// 原样直通：不解码再编码，服务端说的话一个字节都不改（agent 拿到的必须是事实本身）。
			if _, err := out.Write(append(raw, '\n')); err != nil {
				return err
			}
			continue
		}
		if line := Render(ev); line != "" {
			fmt.Fprintln(out, line)
		}
	}
}

// readCommands 从标准输入读命令。
//
// 这里仍然是最朴素的一行一读。等到「事件在刷屏、你正在输一半 bet」真的难受起来时，
// 再上擦除重绘那一套——在那之前引一个 readline 依赖是提前付账。
func readCommands(s *Session, in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch line {
		case "":
			continue
		case "quit", "exit":
			_ = s.Send(protocol.Command{Type: protocol.CmdQuit})
			_ = s.Close()
			return
		case "help":
			fmt.Fprintln(out, helpText)
			continue
		}
		action, err := poker.ParseAction(line)
		if err != nil {
			fmt.Fprintf(out, "%v\n", err)
			continue
		}
		if err := s.Send(protocol.CommandOf(action)); err != nil {
			fmt.Fprintf(out, "发不出去：%v\n", err)
			return
		}
	}
	// 标准输入关了（比如管道结束），但牌桌可能还在打，继续看着就行。
}

const helpText = `可用命令：
  fold          弃牌
  check         过牌
  call          跟注
  bet <数额>    把本轮总投入推到这个数（不是「再加」这么多）
  allin         推光
  help / quit`

// RunBot 跑一个机器人客户端。
//
// 它是一个独立进程，连的是同一个 socket，收的是同一份事件流，走的是外部 AI agent
// 一模一样的那条路（ADR-0010）。这意味着每一次本地对战都在回归测试 agent 接口本身——
// 千万别为了省事把它改成服务端里的一个 goroutine，那样它天然能看到所有人的底牌，
// ADR-0006 那条不变量对它就不成立了。
func RunBot(s *Session, out io.Writer) error {
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
		if ev.Type != poker.EventYourTurn || ev.Snapshot == nil {
			continue
		}
		action := Decide(ev.Snapshot)
		fmt.Fprintf(out, "→ %s\n", action)
		if err := s.Send(protocol.CommandOf(action)); err != nil {
			return err
		}
	}
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
		if canBet {
			// 加注到最小加注额。够用，而且不会把自己推进算不清的局面。
			return poker.Action{Kind: poker.BetTo, Amount: bet.Min}
		}
		if canCheck {
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
