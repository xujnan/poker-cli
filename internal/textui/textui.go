// Package textui 管「在终端上长什么样」：牌怎么显示、一列怎么对齐。
//
// 它跟 poker.Card.String() 是两回事，这条界线要守住：
//
//	poker.Card.String()  "As"  牌的规范文本形式，上线路、进历史文件，机器读的
//	textui.Card()        "A♠"  终端上的样子，人读的
//
// 混成一个的话，改一次显示就会把 JSONL 的 wire 格式和已经存下来的手牌历史一起改掉——
// 照着 docs/agent.md 写的 agent 会当场解析失败，旧历史文件也再也 verify 不过。
// ADR-0002 说的「共用同一套事件，只在渲染上分岔」，分岔口就在这里。
package textui

import (
	"io"
	"os"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/xujnan/poker-cli/internal/poker"
)

// 花色符号用的是文本形态（U+2660 一族），不是带变体选择符的 emoji 形态（♠️）。
//
// 后者在多数终端里是双宽，而各家终端对它到底占一列还是两列并不一致，
// 于是一张对齐好的牌桌换个终端就散了。文本形态是单宽，等宽字体基本都有。
var suitSymbols = [4]string{"♠", "♥", "♦", "♣"}

const (
	red   = "\033[31m"
	reset = "\033[0m"
)

// colorOn 决定要不要上色。默认关着，只有 UseColor 在确认对面是终端之后才打开。
//
// 进程级的开关不好看，但上色本来就是进程级的终端属性，而替代方案是把一个样式参数
// 一路穿过 Render / Format / 每个辅助函数——为一个显示开关改十几个签名不划算。
// 用原子量是图个省心：这样「启动时设一次、之后只读」这条约定万一被破坏，也不会是数据竞争。
var colorOn atomic.Bool

// UseColor 在 w 确实是终端时打开彩色。只在进程启动时调一次。
//
// 管道和文件里一律不上色：转录、测试、重定向出来的日志都不该混进转义序列。
// 也认 NO_COLOR（https://no-color.org）和 TERM=dumb。
func UseColor(w io.Writer) {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return
	}
	f, ok := w.(*os.File)
	if !ok {
		return
	}
	info, err := f.Stat()
	if err != nil {
		return
	}
	colorOn.Store(info.Mode()&os.ModeCharDevice != 0)
}

// Card 是一张牌在终端上的样子，例如 A♠。
//
// 开了彩色的话，红桃和方块整张牌标红——这是读牌时真正用得上的那个区分。
// 黑桃梅花保持终端默认前景色，不写成黑色：深色背景下黑字等于隐身。
func Card(c poker.Card) string {
	s := c.Rank.String() + suitSymbols[c.Suit]
	if colorOn.Load() && (c.Suit == poker.Hearts || c.Suit == poker.Diamonds) {
		return red + s + reset
	}
	return s
}

// Cards 把一串牌排成一行。空的时候给一个短横，免得那一栏看起来像漏了。
func Cards(cs []poker.Card) string {
	if len(cs) == 0 {
		return "-"
	}
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = Card(c)
	}
	return strings.Join(parts, " ")
}

// Width 估算一个字符串在等宽终端里占几列。
//
// 只分两档：东亚宽字符占两列，其余一列。真正严谨的宽度表在 golang.org/x/text/width，
// 但那要引一个依赖，而这里要摆平的只有中文名字、中文表头和几个花色符号。
//
// 花色符号（U+2660 一族）属于 Unicode 里的「宽度不定」区，多数终端按一列排，
// 所以这里也按一列算——这正是不用 emoji 形态的原因。
func Width(s string) int {
	w := 0
	for i := 0; i < len(s); {
		// 跳过 ANSI 转义序列：它一列都不占，算进去的话上了色的那行就会短一截。
		if n := escapeLen(s[i:]); n > 0 {
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if isWide(r) {
			w += 2
		} else {
			w++
		}
		i += size
	}
	return w
}

// escapeLen 返回开头那个 ANSI 转义序列的长度，开头不是转义序列则返回 0。
func escapeLen(s string) int {
	if !strings.HasPrefix(s, "\033[") {
		return 0
	}
	for i := 2; i < len(s); i++ {
		if s[i] >= '@' && s[i] <= '~' {
			return i + 1
		}
	}
	return 0
}

func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F: // 谚文字母
		return true
	case r >= 0x2E80 && r <= 0xA4CF: // 中日韩部首 ~ 彝文，含中文标点「、」「。」
		return r != 0x303F
	case r >= 0xAC00 && r <= 0xD7A3: // 谚文音节
		return true
	case r >= 0xF900 && r <= 0xFAFF: // 中日韩兼容表意
		return true
	case r >= 0xFE30 && r <= 0xFE6F: // 中日韩兼容形式
		return true
	case r >= 0xFF00 && r <= 0xFF60: // 全角，含「（」「：」
		return true
	case r >= 0xFFE0 && r <= 0xFFE6:
		return true
	case r >= 0x1F300 && r <= 0x1FAFF: // emoji
		return true
	}
	return false
}

// Pad 把字符串右边补到 n 列宽。
//
// 不能用 %-10s：那个按字节算，而「我」是三个字节、两列宽，于是中文名字那一行永远是歪的。
func Pad(s string, n int) string {
	if gap := n - Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// PadLeft 把字符串左边补到 n 列宽，用来右对齐表格里的数字列。
func PadLeft(s string, n int) string {
	if gap := n - Width(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}
	return s
}
