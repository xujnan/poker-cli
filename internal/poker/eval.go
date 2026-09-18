package poker

import "sort"

// Category 是牌型，值越大越强。
type Category uint8

const (
	HighCard Category = iota
	OnePair
	TwoPair
	ThreeOfAKind
	Straight
	Flush
	FullHouse
	FourOfAKind
	StraightFlush
)

var categoryNames = [...]string{
	HighCard:      "高牌",
	OnePair:       "一对",
	TwoPair:       "两对",
	ThreeOfAKind:  "三条",
	Straight:      "顺子",
	Flush:         "同花",
	FullHouse:     "葫芦",
	FourOfAKind:   "四条",
	StraightFlush: "同花顺",
}

func (c Category) String() string { return categoryNames[c] }

// HandRank 是一手牌的牌力。比较时先比 Category，相同再逐位比 Tiebreak。
// Tiebreak 的长度取决于牌型：顺子只有 1 位，高牌有 5 位，其余介于两者之间。
type HandRank struct {
	Category Category
	Tiebreak []Rank
	Best     []Card // 构成这个牌力的最佳五张，摊牌时展示用
}

// Compare 返回 -1 / 0 / 1。返回 0 意味着两手牌完全等价，会平分底池。
func (h HandRank) Compare(o HandRank) int {
	if h.Category != o.Category {
		if h.Category < o.Category {
			return -1
		}
		return 1
	}
	for i := 0; i < len(h.Tiebreak) && i < len(o.Tiebreak); i++ {
		if h.Tiebreak[i] != o.Tiebreak[i] {
			if h.Tiebreak[i] < o.Tiebreak[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// Evaluate 从 5 到 7 张牌里挑出最强的五张组合。
//
// 实现是朴素的：C(7,5)=21 种组合全枚举，每种算一次牌力取最大。单次约一两微秒，
// 九人摊牌也在 0.3 毫秒内，远不是瓶颈。不做查表优化，也不提供可切换的实现——
// 等 profile 真的指到这里再说，那时这个函数签名不用变。
func Evaluate(cards []Card) HandRank {
	if len(cards) < 5 || len(cards) > 7 {
		panic("poker: Evaluate 需要 5 到 7 张牌")
	}
	n := len(cards)
	var best HandRank
	first := true
	var five [5]Card
	for a := 0; a < n-4; a++ {
		for b := a + 1; b < n-3; b++ {
			for c := b + 1; c < n-2; c++ {
				for d := c + 1; d < n-1; d++ {
					for e := d + 1; e < n; e++ {
						five[0], five[1], five[2], five[3], five[4] = cards[a], cards[b], cards[c], cards[d], cards[e]
						r := evaluate5(five)
						if first || r.Compare(best) > 0 {
							best, first = r, false
						}
					}
				}
			}
		}
	}
	return best
}

// evaluate5 给恰好五张牌定牌力。
func evaluate5(five [5]Card) HandRank {
	cards := make([]Card, 5)
	copy(cards, five[:])
	sort.Slice(cards, func(i, j int) bool { return cards[i].Rank > cards[j].Rank })

	flush := true
	for i := 1; i < 5; i++ {
		if cards[i].Suit != cards[0].Suit {
			flush = false
			break
		}
	}

	counts := map[Rank]int{}
	for _, c := range cards {
		counts[c.Rank]++
	}

	// 按「出现次数降序、点数降序」排列，四条的点数排在踢脚前面，葫芦的三条排在对子前面。
	type group struct {
		rank  Rank
		count int
	}
	groups := make([]group, 0, 5)
	for r, n := range counts {
		groups = append(groups, group{r, n})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].count != groups[j].count {
			return groups[i].count > groups[j].count
		}
		return groups[i].rank > groups[j].rank
	})

	straightHigh, straight := straightHighCard(cards)

	tie := make([]Rank, 0, 5)
	for _, g := range groups {
		tie = append(tie, g.rank)
	}

	var cat Category
	switch {
	case straight && flush:
		cat, tie = StraightFlush, []Rank{straightHigh}
	case groups[0].count == 4:
		cat = FourOfAKind
	case groups[0].count == 3 && groups[1].count == 2:
		cat = FullHouse
	case flush:
		cat = Flush // tie 此时正好是五张不同点数的降序
	case straight:
		cat, tie = Straight, []Rank{straightHigh}
	case groups[0].count == 3:
		cat = ThreeOfAKind
	case groups[0].count == 2 && groups[1].count == 2:
		cat = TwoPair
	case groups[0].count == 2:
		cat = OnePair
	default:
		cat = HighCard
	}

	best := make([]Card, 5)
	copy(best, cards)
	return HandRank{Category: cat, Tiebreak: tie, Best: best}
}

// straightHighCard 判断五张互不相同的牌是否构成顺子，并返回顺子的最大点数。
// A-2-3-4-5 这手里 Ace 当 1 用，顺子的最大点数是 5，这是唯一的特例。
func straightHighCard(desc []Card) (Rank, bool) {
	for i := 1; i < 5; i++ {
		if desc[i].Rank == desc[i-1].Rank {
			return 0, false // 有重复点数，不可能是顺子
		}
	}
	if desc[0].Rank-desc[4].Rank == 4 {
		return desc[0].Rank, true
	}
	if desc[0].Rank == Ace && desc[1].Rank == Five && desc[4].Rank == Two {
		return Five, true
	}
	return 0, false
}
