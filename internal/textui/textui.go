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
	"strings"

	"github.com/xujnan/poker-cli/internal/poker"
)

// 花色符号用的是文本形态（U+2660 一族），不是带变体选择符的 emoji 形态（♠️）。
//
// 后者在多数终端里是双宽，而各家终端对它到底占一列还是两列并不一致，
// 于是一张对齐好的牌桌换个终端就散了。文本形态是单宽，等宽字体基本都有。
var suitSymbols = [4]string{"♠", "♥", "♦", "♣"}

// Card 是一张牌在终端上的样子，例如 A♠。
func Card(c poker.Card) string {
	return c.Rank.String() + suitSymbols[c.Suit]
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
	for _, r := range s {
		if isWide(r) {
			w += 2
		} else {
			w++
		}
	}
	return w
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
