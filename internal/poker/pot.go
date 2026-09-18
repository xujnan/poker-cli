package poker

import "sort"

// Pot 是一个池子。第一个是主池，后面的都是边池（Side Pot）。
type Pot struct {
	Amount int `json:"amount"`
	// Eligible 是有资格争夺这个池的人：没弃牌，而且投够了这一层。
	Eligible []string `json:"eligible"`
	Winners  []string `json:"winners,omitempty"`
}

// PotSeat 是分池需要知道的关于一个座位的全部信息。
//
// 弃牌者也要传进来：他投进去的筹码留在池里，只是他不再有资格争夺。
type PotSeat struct {
	Player    string
	Committed int
	Folded    bool
}

// BuildPots 按各人的总投入分层，把桌上的筹码切成主池与若干边池。
//
// 分层的道理：某人 all-in 了 50，那么每个人的前 50 块构成一个池，只有投够 50 的人
// 才有资格争夺——超出 50 的部分他匹配不了，自然轮不到他分。所以「有几个不同的投入额」
// 就有「几层池子」。这也顺带处理了「加注了却没人跟」的情形：多出来的那部分独立成池，
// 池里只有他一个人有资格，等于原样退还给他。
//
// 这段逻辑是整个项目里最容易算错、错了又最不容易被发现的地方——它不会崩，
// 只会让某个人少拿几块钱。ADR-0004 之所以坚持随机源必须可播种，为的就是能给它写回归测试。
func BuildPots(seats []PotSeat) []Pot {
	levels := make([]int, 0, len(seats))
	for _, s := range seats {
		if s.Committed > 0 {
			levels = append(levels, s.Committed)
		}
	}
	if len(levels) == 0 {
		return nil
	}
	sort.Ints(levels)
	// 去重，得到一层层的分界线。
	uniq := levels[:1]
	for _, l := range levels[1:] {
		if l != uniq[len(uniq)-1] {
			uniq = append(uniq, l)
		}
	}

	var pots []Pot
	prev := 0
	for _, level := range uniq {
		amount := 0
		var eligible []string
		for _, s := range seats {
			amount += min(s.Committed, level) - min(s.Committed, prev)
			if !s.Folded && s.Committed >= level {
				eligible = append(eligible, s.Player)
			}
		}
		prev = level
		if amount == 0 {
			continue
		}
		if len(eligible) == 0 {
			// 这一层的钱全是弃牌者投的，没人有资格争。正常牌局走不到这里
			// （总得有人把这些注匹配下来），兜底是并进上一个池，绝不让筹码凭空消失。
			if len(pots) > 0 {
				pots[len(pots)-1].Amount += amount
				continue
			}
			for _, s := range seats {
				if !s.Folded {
					eligible = append(eligible, s.Player)
				}
			}
		}
		pots = append(pots, Pot{Amount: amount, Eligible: eligible})
	}
	return pots
}

// AwardPots 判定每个池的赢家，返回填好 Winners 的池子与每人应得的筹码。
//
// ranks 只含摊牌的人。弃牌者不在里面，也就永远赢不了任何池子——
// 顺带说一句，这正是「弃牌者底牌永不公开」（ADR-0006）在计算上说得通的原因：
// 他的牌从来不参与比较，所以根本没有亮出来的理由。
//
// order 是从 Button 左手第一位起的座位顺序，只用来决定平分时除不尽的那几块归谁——
// 真实牌桌上就是从 Button 左手开始发多出来的筹码。
func AwardPots(pots []Pot, ranks map[string]HandRank, order []string) ([]Pot, map[string]int) {
	payout := make(map[string]int)
	out := make([]Pot, 0, len(pots))

	for _, pot := range pots {
		var best HandRank
		first := true
		var winners []string
		for _, p := range pot.Eligible {
			r, ok := ranks[p]
			if !ok {
				continue // 没摊牌的人不参与比较
			}
			switch {
			case first || r.Compare(best) > 0:
				best, first, winners = r, false, []string{p}
			case r.Compare(best) == 0:
				winners = append(winners, p)
			}
		}
		if len(winners) == 0 {
			// 只剩一个人没弃牌时不必摊牌，ranks 是空的，池子直接归他。
			winners = append(winners, pot.Eligible...)
		}
		winners = sortByOrder(winners, order)

		share := pot.Amount / len(winners)
		odd := pot.Amount % len(winners)
		for i, w := range winners {
			amount := share
			if i < odd {
				amount++
			}
			payout[w] += amount
		}
		pot.Winners = winners
		out = append(out, pot)
	}
	return out, payout
}

// sortByOrder 按座位顺序重排赢家，让「多出来的筹码归谁」有个确定的答案。
func sortByOrder(winners []string, order []string) []string {
	pos := make(map[string]int, len(order))
	for i, p := range order {
		pos[p] = i
	}
	out := append([]string(nil), winners...)
	sort.SliceStable(out, func(i, j int) bool {
		pi, iok := pos[out[i]]
		pj, jok := pos[out[j]]
		if iok != jok {
			return iok
		}
		return pi < pj
	})
	return out
}
