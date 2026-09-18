package client

import (
	"fmt"
	"io"

	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/textui"
)

// 输出格式（ADR-0002）。同一份事件流，三种投影。
const (
	// FormatText 是一行一条、只往下滚的流水账。管道、重定向、转录都走这个。
	FormatText = "text"
	// FormatJSONL 是原样直通的事件流，给 agent。
	FormatJSONL = "jsonl"
	// FormatLive 就地重画，只显示正在打的这一手。只有对面是终端时才用得了。
	FormatLive = "live"
	// FormatAuto 让程序自己挑：终端用 live，其余用 text。
	FormatAuto = "auto"
)

// ResolveFormat 把 auto 落实成一个具体格式。
//
// 只有终端认得光标移动那套转义序列，所以重画只在终端上开；管道、文件、测试里的
// buffer 一律退回滚动输出——不然录出来的屏和 CI 的日志里会全是 \033[12A。
// 显式写了 --format=live 的就按他说的办：他知道自己在往哪儿输出。
func ResolveFormat(format string, out io.Writer) string {
	if format != FormatAuto {
		return format
	}
	if textui.IsTerminal(out) {
		return FormatLive
	}
	return FormatText
}

// view 是 Play 对「怎么呈现」的全部要求。
//
// 之所以要这么一层，是因为重画那一版必须知道用户敲过东西（终端把输入回显之后
// 换了行，下一帧上移的行数得跟着变），而滚动那一版根本不关心。把这件事收进接口，
// Play 的主循环就只有一份。
type view interface {
	// start 在事件流开始之前调一次。
	start()
	// event 呈现一条事件。raw 是服务端发来的原始那一行。
	event(ev poker.Event, raw []byte) error
	// typed 表示用户刚敲完一行并回车。
	typed()
	// notice 是给用户的一句话（帮助、输入错误），不来自服务端。
	notice(s string)
	// refresh 在处理完用户输入之后调，让需要重画的那一版有机会重画。
	refresh()
	// disconnected 表示连接断了，事件流到此为止。
	disconnected()
}

func newView(format string, out io.Writer, me string) view {
	switch format {
	case FormatJSONL:
		return &jsonlView{out: out}
	case FormatLive:
		return newLiveView(out, me)
	default:
		return &textView{out: out}
	}
}

// textView 是一行一条往下滚的流水账。
type textView struct{ out io.Writer }

func (v *textView) start() {}

func (v *textView) event(ev poker.Event, _ []byte) error {
	if line := Render(ev); line != "" {
		_, err := fmt.Fprintln(v.out, line)
		return err
	}
	return nil
}

func (v *textView) typed()          {}
func (v *textView) notice(s string) { fmt.Fprintln(v.out, s) }
func (v *textView) refresh()        {}
func (v *textView) disconnected()   { fmt.Fprintln(v.out, "与牌桌的连接已断开。") }

// jsonlView 把服务端说的话原样吐出去。
//
// 不解码再编码：agent 拿到的必须是事实本身，中间多一次序列化就多一处能悄悄改掉
// 字段顺序、数字精度、未知字段的地方。
type jsonlView struct{ out io.Writer }

func (v *jsonlView) start() {}

func (v *jsonlView) event(_ poker.Event, raw []byte) error {
	_, err := v.out.Write(append(raw, '\n'))
	return err
}

func (v *jsonlView) typed()          {}
func (v *jsonlView) notice(s string) { fmt.Fprintln(v.out, s) }
func (v *jsonlView) refresh()        {}
func (v *jsonlView) disconnected()   {}
