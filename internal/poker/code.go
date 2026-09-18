package poker

import "math/rand/v2"

// TableCodeLength 是 Table Code 的长度。
const TableCodeLength = 6

// tableCodeAlphabet 剔除了 I、O、0、1 这几个读出来或抄下来容易混的字符。
// Table Code 的设计目标是「能口头报给隔壁桌的人」，这比多那几个字符的熵重要。
const tableCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// NewTableCode 生成一个 Table Code。随机源由调用方注入，同 Deck（ADR-0004）。
func NewTableCode(r *rand.Rand) string {
	b := make([]byte, TableCodeLength)
	for i := range b {
		b[i] = tableCodeAlphabet[r.IntN(len(tableCodeAlphabet))]
	}
	return string(b)
}

// ValidTableCode 判断一个字符串是否是合法的 Table Code。
//
// ADR-0013 让 Table Code 直接充当 socket 文件名，所以「加入牌桌」就是一次路径拼接——
// 这条校验是那次拼接的前提：未经校验的字符串里可以藏 ../，那就不再是找一张牌桌了。
func ValidTableCode(s string) bool {
	if len(s) != TableCodeLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		found := false
		for j := 0; j < len(tableCodeAlphabet); j++ {
			if s[i] == tableCodeAlphabet[j] {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
