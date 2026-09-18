package poker

import "fmt"

// Suit 是一张牌的花色。花色之间没有大小之分。
type Suit uint8

const (
	Spades Suit = iota
	Hearts
	Diamonds
	Clubs
)

var suitChars = [4]byte{'s', 'h', 'd', 'c'}

func (s Suit) String() string { return string(suitChars[s]) }

// Rank 是一张牌的点数，2 最小，Ace 最大。
// A-2-3-4-5 顺子里 Ace 当 1 用，那是 evaluate5 的局部处理，不影响这里的序。
type Rank uint8

const (
	Two Rank = iota + 2
	Three
	Four
	Five
	Six
	Seven
	Eight
	Nine
	Ten
	Jack
	Queen
	King
	Ace
)

var rankChars = [15]byte{
	Two: '2', Three: '3', Four: '4', Five: '5', Six: '6', Seven: '7',
	Eight: '8', Nine: '9', Ten: 'T', Jack: 'J', Queen: 'Q', King: 'K', Ace: 'A',
}

func (r Rank) String() string { return string(rankChars[r]) }

// Card 是一张牌。文本形式形如 "As"、"Td"、"2c"，JSON 里也用这个形式。
type Card struct {
	Rank Rank
	Suit Suit
}

func (c Card) String() string { return c.Rank.String() + c.Suit.String() }

func (c Card) MarshalText() ([]byte, error) { return []byte(c.String()), nil }

func (c *Card) UnmarshalText(b []byte) error {
	parsed, err := ParseCard(string(b))
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}

// ParseCard 解析 "As" 这样的文本形式，主要供测试和手牌历史回放使用。
func ParseCard(s string) (Card, error) {
	if len(s) != 2 {
		return Card{}, fmt.Errorf("牌的文本形式必须是两个字符，得到 %q", s)
	}
	var c Card
	found := false
	for r := Two; r <= Ace; r++ {
		if rankChars[r] == s[0] {
			c.Rank, found = r, true
			break
		}
	}
	if !found {
		return Card{}, fmt.Errorf("无法识别的点数 %q", s[0:1])
	}
	found = false
	for i, ch := range suitChars {
		if ch == s[1] {
			c.Suit, found = Suit(i), true
			break
		}
	}
	if !found {
		return Card{}, fmt.Errorf("无法识别的花色 %q", s[1:2])
	}
	return c, nil
}

// MustParseCards 解析空格分隔的多张牌，只在测试里用。
func MustParseCards(s string) []Card {
	var out []Card
	for _, f := range splitFields(s) {
		c, err := ParseCard(f)
		if err != nil {
			panic(err)
		}
		out = append(out, c)
	}
	return out
}

func splitFields(s string) []string {
	var out []string
	cur := ""
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(s[i])
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
