package poker

import "math/rand/v2"

// TableCodeLength 是 Table Code 的长度。
const TableCodeLength = 6

// tableCodeAlphabet 只管**生成**，剔除了 I、O、0、1 这几个读出来或抄下来容易混的字符。
// Table Code 的设计目标是「能口头报给隔壁桌的人」，这比多那几个字符的熵重要。
//
// 校验不用这张表：自己报出去的码要念得清楚，别人递过来的码没理由挑剔（见 ValidTableCode）。
const tableCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// NewTableCode 生成一个 Table Code。随机源由调用方注入，同 Deck（ADR-0004）。
func NewTableCode(r *rand.Rand) string {
	b := make([]byte, TableCodeLength)
	for i := range b {
		b[i] = tableCodeAlphabet[r.IntN(len(tableCodeAlphabet))]
	}
	return string(b)
}

// ValidTableCode 判断一个字符串是否是合法的 Table Code：6 位大写字母或数字。
// 传进来的应该是规范化之后的形式（见 transport.normalizeCode，它先转大写）。
//
// 它接受**全部** 36 个字母数字，包括 I、O、0、1。生成时避开那四个是为了让码念得清楚
// （见 tableCodeAlphabet），但那是对自己的要求，不是拦别人的理由：`--code` 是人自己
// 挑的名字，`POKER0`、`TABLE1` 这种一眼就懂的码没道理被自己的程序拒之门外。
//
// 剩下的那条不能松：ADR-0013 让 Table Code 直接充当 socket 文件名，「加入牌桌」
// 就是一次路径拼接。这里限死字母数字，正是那次拼接的安全带——放进来一个 `/` 或 `..`，
// 就不再是在找一张牌桌了。所以这个函数从来就不只是在挑剔字符集。
func ValidTableCode(s string) bool {
	if len(s) != TableCodeLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			continue
		}
		if c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}
