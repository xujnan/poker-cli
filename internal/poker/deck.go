package poker

import "math/rand/v2"

// Deck 是一副洗好的牌。随机源由调用方注入——ADR-0004 的 --seed 可复现性全靠这一点，
// 这个包内不允许出现任何自己创建随机源或读取时钟的代码。
type Deck struct {
	cards []Card
	next  int
}

// RandFor 由一个种子造出随机源。
//
// 发牌和重放必须走同一个函数：两边各自 NewPCG 一下，参数差一个字面量就再也对不上了，
// 而「种子可复现」这件事一旦对不上，是不会有任何报错的——只是重放出来的牌不一样。
func RandFor(seed uint64) *rand.Rand {
	return rand.New(rand.NewPCG(seed, 0x9E3779B97F4A7C15))
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
