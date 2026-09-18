package poker

import "math/rand/v2"

// Deck 是一副洗好的牌。随机源由调用方注入——ADR-0004 的 --seed 可复现性全靠这一点，
// 这个包内不允许出现任何自己创建随机源或读取时钟的代码。
type Deck struct {
	cards []Card
	next  int
}

// NewDeck 用给定随机源洗出一副 52 张的新牌。
func NewDeck(r *rand.Rand) *Deck {
	cards := make([]Card, 0, 52)
	for s := Spades; s <= Clubs; s++ {
		for rk := Two; rk <= Ace; rk++ {
			cards = append(cards, Card{Rank: rk, Suit: s})
		}
	}
	r.Shuffle(len(cards), func(i, j int) {
		cards[i], cards[j] = cards[j], cards[i]
	})
	return &Deck{cards: cards}
}

// Draw 发一张牌。牌堆发空时 panic——那意味着调用方算错了牌数，不是运行时错误。
func (d *Deck) Draw() Card {
	if d.next >= len(d.cards) {
		panic("poker: 牌堆已空")
	}
	c := d.cards[d.next]
	d.next++
	return c
}

// DrawN 连发 n 张。
func (d *Deck) DrawN(n int) []Card {
	out := make([]Card, n)
	for i := range out {
		out[i] = d.Draw()
	}
	return out
}

// Remaining 返回牌堆剩余张数。
func (d *Deck) Remaining() int { return len(d.cards) - d.next }
