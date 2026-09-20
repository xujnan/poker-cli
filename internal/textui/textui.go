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

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"

	"github.com/xujnan/poker-cli/internal/poker"
)

// 花色符号用的是文本形态（U+2660 一族），不是带变体选择符的 emoji 形态（♠️）。
//
// 后者在多数终端里是双宽，而各家终端对它到底占一列还是两列并不一致，
// 于是一张对齐好的牌桌换个终端就散了。文本形态是单宽，等宽字体基本都有。
var suitSymbols = [4]string{"♠", "♥", "♦", "♣"}

const (
	red   = "\033[31m"
	bold  = "\033[1m"
	dim   = "\033[2m"
	reset = "\033[0m"
)

// colorOn 决定要不要上色。默认关着，只有 UseColor 在确认对面是终端之后才打开。
//
// 进程级的开关不好看，但上色本来就是进程级的终端属性，而替代方案是把一个样式参数
// 一路穿过 Render / Format / 每个辅助函数——为一个显示开关改十几个签名不划算。
// 用原子量是图个省心：这样「启动时设一次、之后只读」这条约定万一被破坏，也不会是数据竞争。
var colorOn atomic.Bool

// IsTerminal 判断 w 那一头是不是终端。
//
// 凡是「只有终端才做得了」的事都该先问它一句：上色、就地重画光标。管道、重定向、
// 测试里的 buffer 一律返回 false——往那些地方写转义序列，出来的就是一堆乱码。
//
// 这里用 x/term 而不是自己看 os.ModeCharDevice，因为后者根本不是在问「是不是终端」，
// 它问的是「是不是字符设备」，而 /dev/null、/dev/zero、串口全都是字符设备。
// 于是 `poker join ... > /dev/null` 会被判成终端，往里灌一堆光标移动序列。
// x/term 走的是真正的 TCGETS ioctl——只有终端答得上来。
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// Height 返回 w 那一头的终端有多少行，问不出来就返回 0。
//
// 就地重画的那一屏必须塞得进一屏：塞不进时终端会滚动，而滚动之后「上移 N 行」
// 回到的就不是原来那个位置了。以前这个上限是一个拍脑袋的常数，现在直接问终端。
// 每次重画都问一遍（一次 ioctl，微秒级），顺带把「用户中途拉大拉小窗口」也管了，
// 不必再去接 SIGWINCH。
func Height(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	_, h, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return h
}

// Cols 返回 w 那一头的终端有多少列，问不出来就返回 0。
//
// 跟 Height 一样是每次用的时候现问。画横线这类「铺到多宽」的东西必须问它：
// 画过头了终端会折行，而折了一行，就地重画时「上移 N 行」就再也对不上了。
func Cols(w io.Writer) int {
	f, ok := w.(*os.File)
	if !ok {
		return 0
	}
	c, _, err := term.GetSize(int(f.Fd()))
	if err != nil {
		return 0
	}
	return c
}

// UseColor 在 w 确实是终端时打开彩色。只在进程启动时调一次。
//
// 管道和文件里一律不上色：转录、测试、重定向出来的日志都不该混进转义序列。
// 也认 NO_COLOR（https://no-color.org）和 TERM=dumb。
func UseColor(w io.Writer) {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return
	}
	colorOn.Store(IsTerminal(w))
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

// Bold 和 Dim 给一段文字加重或减弱，没开彩色时原样返回。
//
// 只能用在**不含其他样式**的片段上。终端的 \033[0m 是「全部复位」，不是「复位我这一层」，
// 所以把一张标红的牌包进 Dim 里，牌尾那个复位会顺手把 dim 也关掉，后半行就花了。
// 要给带牌的一行分层，就把不带牌的那几段分别包起来（街分隔线那条横线就是这么做的）。
func Bold(s string) string { return wrap(bold, s) }

// Dim 见 Bold。
func Dim(s string) string { return wrap(dim, s) }

func wrap(style, s string) string {
	if !colorOn.Load() || s == "" {
		return s
	}
	return style + s + reset
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

// Width 算一个字符串在等宽终端里占几列。
//
// 宽度表交给 go-runewidth。自己手写的那张范围表撑不住名字是任意用户输入这件事：
// 它把组合符号（法语的重音、泰文的声调）当成各占一列、把 ZWJ 拼出来的 emoji
// 当成好几个字符，还漏掉了 U+1F0A0 那一块——扑克牌 emoji 🃏，一个扑克程序漏了它。
// 每一处都让座位表歪一格，而歪掉的那一格不会有编译错误。
//
// 花色符号（U+2660 一族）属于 Unicode 里的「宽度不定」区，runewidth 默认按一列排，
// 和多数终端一致——这正是不用 emoji 形态（♠️）的原因，那个是双宽而且各家不一致。
func Width(s string) int {
	w := 0
	for i := 0; i < len(s); {
		// 跳过 ANSI 转义序列：它一列都不占，算进去的话上了色的那行就会短一截。
		// 这一层得自己来，runewidth 只认字符，不认转义序列。
		if n := escapeLen(s[i:]); n > 0 {
			i += n
			continue
		}
		// 一次量一整段普通文本，而不是一个 rune 一个 rune 地量：ZWJ 序列和组合符号
		// 都是一串 rune 占一个格子，拆开逐个问会把它们数成好几格。
		//
		// 从第 1 个字节之后找下一个转义序列，是为了保证这一段至少有一个字节，
		// 循环一定往前走——上面那个 escapeLen 遇到残缺的转义序列会返回 0。
		rest := s[i:]
		end := len(rest)
		if j := strings.Index(rest[1:], "\033["); j >= 0 {
			end = j + 1
		}
		w += runewidth.StringWidth(rest[:end])
		i += end
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
