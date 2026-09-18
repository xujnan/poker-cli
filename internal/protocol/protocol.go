// Package protocol 定义客户端与服务端之间的 wire 格式：两个方向都是一行一个 JSON 对象。
//
// 依赖方向是 protocol → poker，反向绝不允许（ADR-0012）。事件类型本身住在 poker 包里，
// 因为它是牌局产生的事实；这个包只负责把它搬上线路。
package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/xujnan/poker-cli/internal/poker"
)

// CommandType 是客户端命令的类型。
type CommandType string

const (
	// CmdJoin 是连接建立后的第一条命令，名字即身份（ADR-0009）。
	CmdJoin CommandType = "join"
	// CmdQuit 主动离座。直接断开连接也是一样的效果，这条命令只是让意图显式。
	CmdQuit CommandType = "quit"

	// 五种动作各自是一个命令类型。这样 agent 发出去的就是 {"type":"call"}，
	// 跟 your_turn 里合法动作列表给的名字一模一样，中间不隔一层包装。
	CmdFold  CommandType = "fold"
	CmdCheck CommandType = "check"
	CmdCall  CommandType = "call"
	// CmdBet 的 Amount 是「把本轮总投入推到多少」，不是「再加多少」（ADR-0005）。
	CmdBet   CommandType = "bet"
	CmdAllIn CommandType = "allin"

	// CmdTopUp 补码。随时能发，但只在两手牌之间落地（ADR-0015）。
	CmdTopUp CommandType = "topup"
	// CmdSitOut / CmdSitIn 是「我歇会儿」和「我回来了」（ADR-0014）。
	CmdSitOut CommandType = "sitout"
	CmdSitIn  CommandType = "sitin"
)

// Command 是客户端发往服务端的一条命令。
type Command struct {
	Type CommandType `json:"type"`
	// Name 和 Buyin 只在 join 时用。
	Name  string `json:"name,omitempty"`
	Buyin int    `json:"buyin,omitempty"`
	// Amount 用于 bet（推到多少）和 topup（补多少）。
	Amount int `json:"amount,omitempty"`
}

// IsAction 判断这条命令是不是一个牌桌动作。
func (c Command) IsAction() bool {
	switch c.Type {
	case CmdFold, CmdCheck, CmdCall, CmdBet, CmdAllIn:
		return true
	}
	return false
}

// IsSeatCommand 判断这条命令是不是座位类命令：它们不推进牌局，只改座位状态。
func (c Command) IsSeatCommand() bool {
	switch c.Type {
	case CmdTopUp, CmdSitOut, CmdSitIn:
		return true
	}
	return false
}

// Action 把命令翻译成牌局动作。只有 IsAction 为真时才有意义。
func (c Command) Action() (poker.Action, error) {
	switch c.Type {
	case CmdFold:
		return poker.Action{Kind: poker.Fold}, nil
	case CmdCheck:
		return poker.Action{Kind: poker.Check}, nil
	case CmdCall:
		return poker.Action{Kind: poker.Call}, nil
	case CmdAllIn:
		return poker.Action{Kind: poker.AllIn}, nil
	case CmdBet:
		if c.Amount <= 0 {
			return poker.Action{}, fmt.Errorf("bet 要带一个正数额")
		}
		return poker.Action{Kind: poker.BetTo, Amount: c.Amount}, nil
	}
	return poker.Action{}, fmt.Errorf("%q 不是一个动作", c.Type)
}

// CommandOf 把牌局动作翻译成命令，客户端发指令时用。
func CommandOf(a poker.Action) Command {
	switch a.Kind {
	case poker.Fold:
		return Command{Type: CmdFold}
	case poker.Check:
		return Command{Type: CmdCheck}
	case poker.Call:
		return Command{Type: CmdCall}
	case poker.AllIn:
		return Command{Type: CmdAllIn}
	default:
		return Command{Type: CmdBet, Amount: a.Amount}
	}
}

// MaxLineBytes 是单行的长度上限。给一行 JSON 设上限，是为了让一个坏掉的对端
// 撑不爆我们的内存——它发多少我们就存多少，那就不是协议了。
const MaxLineBytes = 64 * 1024

// WriteCommand 把一条命令写成一行 JSON。
func WriteCommand(w io.Writer, c Command) error {
	return writeLine(w, c)
}

// WriteEvent 把一条事件写成一行 JSON。Event.To 带 json:"-"，不会被写上线路。
func WriteEvent(w io.Writer, e poker.Event) error {
	return writeLine(w, e)
}

func writeLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b)+1 > MaxLineBytes {
		return fmt.Errorf("protocol: 单行超过 %d 字节上限", MaxLineBytes)
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

// CommandReader 从一条连接上逐行读命令。
type CommandReader struct {
	sc *bufio.Scanner
}

// NewCommandReader 包装一个 reader。
func NewCommandReader(r io.Reader) *CommandReader {
	return &CommandReader{sc: newLineScanner(r)}
}

// Next 读下一条命令。连接正常结束时返回 io.EOF。
func (cr *CommandReader) Next() (Command, error) {
	line, err := nextLine(cr.sc)
	if err != nil {
		return Command{}, err
	}
	var c Command
	if err := json.Unmarshal(line, &c); err != nil {
		return Command{}, fmt.Errorf("protocol: 无法解析命令: %w", err)
	}
	if c.Type == "" {
		return Command{}, fmt.Errorf("protocol: 命令缺少 type 字段")
	}
	return c, nil
}

// EventReader 从一条连接上逐行读事件。
type EventReader struct {
	sc *bufio.Scanner
}

// NewEventReader 包装一个 reader。
func NewEventReader(r io.Reader) *EventReader {
	return &EventReader{sc: newLineScanner(r)}
}

// Next 读下一条事件，同时返回原始那一行。
//
// 返回原始行是为了 --format=jsonl 能把服务端发来的字节原样转给下游：
// 解码再编码一次，等于让客户端的结构体定义悄悄改写事实，agent 拿到的就不再是服务端说的话了。
func (er *EventReader) Next() (poker.Event, []byte, error) {
	line, err := nextLine(er.sc)
	if err != nil {
		return poker.Event{}, nil, err
	}
	var e poker.Event
	if err := json.Unmarshal(line, &e); err != nil {
		return poker.Event{}, line, fmt.Errorf("protocol: 无法解析事件: %w", err)
	}
	return e, line, nil
}

func newLineScanner(r io.Reader) *bufio.Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), MaxLineBytes)
	return sc
}

// nextLine 返回下一行非空内容。行内容只在下次 Scan 前有效，调用方要复制。
func nextLine(sc *bufio.Scanner) ([]byte, error) {
	for {
		if !sc.Scan() {
			if err := sc.Err(); err != nil {
				return nil, err
			}
			return nil, io.EOF
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		out := make([]byte, len(line))
		copy(out, line)
		return out, nil
	}
}
