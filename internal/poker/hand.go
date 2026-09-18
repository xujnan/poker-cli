package poker

const (
	holeCardCount      = 2
	communityCardCount = 5
)

// HandResult 是一手牌打完之后的全部事实，包括所有人的底牌。
//
// 它与事件序列的区别是可见性：事件是分别投递给各个玩家的、经过裁剪的视图，
// HandResult 则是上帝视角，只给服务端自己用（将来写 Hand History，ADR-0008）。
// 千万别顺手把它整个塞进某个广播事件里。
type HandResult struct {
	Number    int
	Players   []string
	Hole      map[string][]Card
	Community []Card
	Ranks     map[string]HandRank
	Winners   []string
	Pot       int
}

// PlayHand 打完一手牌，返回结果与该发给各方的事件序列。
//
// 这是第一刀的形状：每人两张底牌，直接发满五张公共牌，摊牌比七张牌力，赢家通吃。
// 没有下注、没有 Street 推进、没有边池，Pot 恒为 0。等下一刀盲注进来时，
// 这个函数会从「一条直线」长成「一个状态机」——签名会变，但它不含 IO 这一点不会变（ADR-0012）。
//
// 函数是纯的：牌从传进来的 Deck 里发，不碰时钟、不碰网络、不起 goroutine。
// 同一个 seed 洗出的 Deck 加同一份 players，必然得到同一个结果。
func PlayHand(number int, players []string, d *Deck) (HandResult, []Event) {
	if len(players) < 2 {
		panic("poker: 一手牌至少要两个玩家")
	}
	if need := len(players)*holeCardCount + communityCardCount; d.Remaining() < need {
		panic("poker: 牌堆不够发这一手")
	}

	seats := append([]string(nil), players...)
	res := HandResult{
		Number:  number,
		Players: seats,
		Hole:    make(map[string][]Card, len(seats)),
		Ranks:   make(map[string]HandRank, len(seats)),
	}
	events := []Event{{Type: EventHandStart, Hand: number, Players: append([]string(nil), seats...)}}

	// 一张一张轮着发，发两圈——真实牌桌就是这么发的，牌序也因此与线下一致。
	for _, p := range seats {
		res.Hole[p] = make([]Card, 0, holeCardCount)
	}
	for i := 0; i < holeCardCount; i++ {
		for _, p := range seats {
			res.Hole[p] = append(res.Hole[p], d.Draw())
		}
	}
	// 底牌事件逐个定向投递。To 一旦漏填，这张牌就广播出去了——visibility_test 守的就是这里。
	for _, p := range seats {
		events = append(events, Event{
			Type:   EventHoleCards,
			To:     p,
			Hand:   number,
			Player: p,
			Cards:  cloneCards(res.Hole[p]),
		})
	}

	res.Community = d.DrawN(communityCardCount)
	events = append(events, Event{
		Type:  EventCommunityCards,
		Hand:  number,
		Cards: cloneCards(res.Community),
	})

	// 摊牌。第一刀没有弃牌，所有人都到这一步，底牌在此刻合法公开。
	entries := make([]ShowdownEntry, 0, len(seats))
	var best HandRank
	first := true
	for _, p := range seats {
		seven := make([]Card, 0, holeCardCount+communityCardCount)
		seven = append(seven, res.Hole[p]...)
		seven = append(seven, res.Community...)
		rank := Evaluate(seven)
		res.Ranks[p] = rank
		entries = append(entries, ShowdownEntry{
			Player:   p,
			Cards:    cloneCards(res.Hole[p]),
			Category: rank.Category.String(),
			Best:     cloneCards(rank.Best),
		})
		if first || rank.Compare(best) > 0 {
			best, first = rank, false
		}
	}
	for _, p := range seats {
		if res.Ranks[p].Compare(best) == 0 {
			res.Winners = append(res.Winners, p)
		}
	}
	events = append(events, Event{Type: EventShowdown, Hand: number, Showdown: entries})

	pot := res.Pot
	events = append(events, Event{
		Type:    EventHandEnd,
		Hand:    number,
		Winners: append([]string(nil), res.Winners...),
		Pot:     &pot,
	})
	return res, events
}

// cloneCards 复制一份牌，免得事件与 HandResult 共享同一个底层数组——
// 一个上帝视角的结构和一堆按人裁剪的事件共享内存，是可见性 bug 最舒服的温床。
func cloneCards(cards []Card) []Card {
	out := make([]Card, len(cards))
	copy(out, cards)
	return out
}
