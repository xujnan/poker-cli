// Package client 连上一张牌桌，把事件流交给人或机器人。
//
// 人类客户端和机器人客户端共用这里的 Session：同一个 socket、同一份 JSONL 事件流、
// 同一条代码路径（ADR-0002、ADR-0010）。两者的差别只在拿到事件之后做什么。
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

// Dial 连上 Table Code 对应的牌桌并报上名字。
//
// 「加入牌桌」在这里就是拼一次路径再连一次 socket——没有服务发现这一步（ADR-0013）。
func Dial(dir, code, name string) (*Session, error) {
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
	if err := protocol.WriteCommand(conn, protocol.Command{Type: protocol.CmdJoin, Name: name}); err != nil {
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

// Close 断开连接。断线在服务端看来就是离座。
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
// 第一刀能说的话只有 quit 和 help——下注命令（ADR-0005）还没进来。
// 这里先用 bufio.Scanner；等真有 bet 要输入、而事件又在不断刷屏时，再上擦除重绘那一套。
func readCommands(s *Session, in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		switch line := strings.TrimSpace(sc.Text()); line {
		case "":
		case "quit", "exit":
			_ = s.Send(protocol.Command{Type: protocol.CmdQuit})
			_ = s.Close()
			return
		case "help":
			fmt.Fprintln(out, "可用命令：help、quit。下注命令还没实现。")
		default:
			fmt.Fprintf(out, "不认识的命令 %q，试试 help。\n", line)
		}
	}
	// 标准输入关了（比如管道结束），但牌桌可能还在打，继续看着就行。
}

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
		for _, cmd := range decide(ev) {
			if err := s.Send(cmd); err != nil {
				return err
			}
		}
	}
}

// decide 是机器人的全部大脑。
//
// 第一刀里没有任何轮到自己行动的时刻——没有下注轮，也就没有 your_turn 事件——
// 所以它固定不做事。等 your_turn（ADR-0007，内嵌快照与合法动作列表）进来时，
// 新逻辑加在这个函数里，服务端一行都不用动。
func decide(ev poker.Event) []protocol.Command {
	_ = ev
	return nil
}
