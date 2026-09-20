package poker

import (
	"math/rand/v2"
	"testing"
)

// dealHand 起一手牌，名字固定，方便各测试引用。
func dealHand(t *testing.T, stacks []int, button int, seed uint64) (*Hand, []Event) {
	t.Helper()
	names := []string{"alice", "bob", "carol", "dave"}
	seats := make([]Seat, len(stacks))
	for i, s := range stacks {
		seats[i] = Seat{Player: names[i], Stack: s}
	}
	d := NewDeck(rand.New(rand.NewPCG(seed, 7)))
	return NewHand(1, seats, button, Blinds{1, 2}, d)
}

// callOrCheck 是「谁都不弃牌」的策略，用来把牌局稳稳推到摊牌。
func callOrCheck(snap *Snapshot) Action {
	if _, ok := legalOf(snap, "check"); ok {
		return Action{Kind: Check}
	}
	return Action{Kind: Call}
}

// playOut 按给定策略把一手牌打完。
func playOut(t *testing.T, h *Hand, events []Event, pick func(*Snapshot) Action) []Event {
	t.Helper()
	for guard := 0; !h.Done(); guard++ {
		if guard > 500 {
			t.Fatal("牌局停不下来")
		}
		player := h.Turn()
		snap := lastSnapshotFor(events, player)
		if snap == nil {
			t.Fatalf("轮到 %s 却没给他快照", player)
		}
		events = append(events, h.Apply(player, pick(snap))...)
	}
	return events
}

func TestHandDealsDistinctCards(t *testing.T) {
	h, events := dealHand(t, []int{200, 200, 200}, 0, 1)
	playOut(t, h, events, callOrCheck)
	res := h.Result()

	seen := map[Card]string{}
	for _, p := range res.Players {
		if len(res.Hole[p]) != holeCardCount {
			t.Fatalf("%s 有 %d 张底牌", p, len(res.Hole[p]))
		}
		for _, c := range res.Hole[p] {
			if owner, dup := seen[c]; dup {
				t.Fatalf("%s 同时出现在 %s 和 %s 手上", c, owner, p)
			}
			seen[c] = p
		}
	}
	if len(res.Community) != 5 {
		t.Fatalf("公共牌有 %d 张", len(res.Community))
	}
	for _, c := range res.Community {
		if owner, dup := seen[c]; dup {
			t.Fatalf("公共牌 %s 也发给了 %s", c, owner)
		}
		seen[c] = "board"
	}
}

// TestHandIsReproducible 是 ADR-0004 那条 --seed 的意义所在：
// 同一个种子加同一串动作，必须打出同一手牌。
func TestHandIsReproducible(t *testing.T) {
	run := func() HandResult {
		h, events := dealHand(t, []int{200, 150, 80}, 1, 20240601)
		playOut(t, h, events, callOrCheck)
		return h.Result()
	}
	a, b := run(), run()

	for _, p := range a.Players {
		for i := range a.Hole[p] {
			if a.Hole[p][i] != b.Hole[p][i] {
				t.Fatalf("同种子两次运行，%s 的底牌不同：%v vs %v", p, a.Hole[p], b.Hole[p])
			}
		}
		if a.Stacks[p] != b.Stacks[p] {
			t.Fatalf("同种子两次运行，%s 的筹码不同：%d vs %d", p, a.Stacks[p], b.Stacks[p])
		}
	}
	for i := range a.Community {
		if a.Community[i] != b.Community[i] {
			t.Fatalf("同种子两次运行，公共牌不同：%v vs %v", a.Community, b.Community)
		}
	}
}

// TestEachPotGoesToItsBestHand：每个池子都归有资格争夺它的人里牌最大的那个。
//
// 注意这条不变量是按池子说的，不是按整手牌说的：有边池的时候，
// 一个牌力不是最强的人完全可能赢下某个边池，那不是 bug。
func TestEachPotGoesToItsBestHand(t *testing.T) {
	for seed := uint64(1); seed <= 120; seed++ {
		r := rand.New(rand.NewPCG(seed, 555))
		h, events := dealHand(t, []int{200, 120, 60, 35}, int(seed)%4, seed)
		events = playOut(t, h, events, func(snap *Snapshot) Action {
			return randomLegalAction(r, snap)
		})
		res := h.Result()
		if len(res.Ranks) == 0 {
			continue // 没摊牌的手不在这条测试的范围里
		}
		for _, pot := range res.Pots {
			var best HandRank
			first := true
			for _, p := range pot.Eligible {
				rank, ok := res.Ranks[p]
				if !ok {
					continue
				}
				if first || rank.Compare(best) > 0 {
					best, first = rank, false
				}
			}
			if first {
				continue
			}
			for _, w := range pot.Winners {
				if res.Ranks[w].Compare(best) != 0 {
					t.Fatalf("seed %d：%s 赢下了一个池子，但他的牌不是这个池子里最大的", seed, w)
				}
			}
		}
	}
}

// TestHandEventSequence 守住事件的形状与顺序：agent 是照着这个顺序写状态机的。
func TestHandEventSequence(t *testing.T) {
	h, events := dealHand(t, []int{200, 200}, 0, 11)
	events = playOut(t, h, events, callOrCheck)

	// 开局固定是：hand_start、两个盲注、两份底牌，然后才轮到人说话。
	want := []EventType{
		EventHandStart, EventBlind, EventBlind,
		EventHoleCards, EventHoleCards, EventTurn, EventYourTurn,
	}
	for i, w := range want {
		if events[i].Type != w {
			t.Fatalf("第 %d 条事件是 %s，想要 %s", i, events[i].Type, w)
		}
	}

	// turn 和 your_turn 永远成对出现，而且说的是同一个人。
	//
	// 前者广播给全桌（谁在想牌，真牌桌上一桌人都看得见），后者只给当事人、带完整快照。
	// 漏发广播那条，别人就不知道牌桌在等谁；两条对不上，屏幕上标的人和真正握有
	// 行动权的人就不是一个——这种错特别难看出来，所以在这儿钉死。
	for i, ev := range events {
		if ev.Type != EventTurn {
			continue
		}
		if ev.To != "" {
			t.Fatalf("第 %d 条 turn 是定向的（To=%q），它该广播给全桌", i, ev.To)
		}
		if i+1 >= len(events) || events[i+1].Type != EventYourTurn {
			t.Fatalf("第 %d 条 turn 后面没有紧跟着 your_turn", i)
		}
		if events[i+1].Player != ev.Player {
			t.Fatalf("turn 说轮到 %s，your_turn 却发给了 %s", ev.Player, events[i+1].Player)
		}
		if len(ev.Cards) > 0 || ev.Snapshot != nil {
			t.Fatalf("广播的 turn 里不该带任何牌或快照：%+v", ev)
		}
	}
	// 结尾固定是：摊牌、分池、结束。
	tail := events[len(events)-3:]
	for i, w := range []EventType{EventShowdown, EventPotAwarded, EventHandEnd} {
		if tail[i].Type != w {
			t.Fatalf("倒数第 %d 条事件是 %s，想要 %s", 3-i, tail[i].Type, w)
		}
	}
	end := events[len(events)-1]
	if end.Pot == nil || *end.Pot == 0 {
		t.Fatalf("hand_end 该带上真实的底池，得到 %v", end.Pot)
	}
	if len(end.Seats) != 2 {
		t.Fatalf("hand_end 该带上各家结束时的筹码，得到 %+v", end.Seats)
	}
}

// TestBlindsAreTakenBeforeCards：盲注先收，再发牌。
func TestBlindsAreTakenBeforeCards(t *testing.T) {
	h, events := dealHand(t, []int{200, 200, 200}, 0, 41)
	var blindIdx, holeIdx = -1, -1
	for i, ev := range events {
		if ev.Type == EventBlind && blindIdx < 0 {
			blindIdx = i
		}
		if ev.Type == EventHoleCards && holeIdx < 0 {
			holeIdx = i
		}
	}
	if blindIdx > holeIdx {
		t.Fatal("盲注该在发牌之前收")
	}
	// 底池里此刻正好是两个盲注。
	snap := lastSnapshotFor(events, h.Turn())
	if snap.Pot != 3 {
		t.Fatalf("收完 1/2 盲注后底池该是 3，得到 %d", snap.Pot)
	}
}

// TestShortStackPostsPartialBlind：筹码不够交满盲注的人，交多少算多少并直接 all-in。
func TestShortStackPostsPartialBlind(t *testing.T) {
	h, events := dealHand(t, []int{200, 200, 1}, 0, 43)
	// 座位：alice(button) bob(小盲) carol(大盲)，carol 只有 1 块，交不满 2 块大盲。
	var bigBlind Event
	for _, ev := range events {
		if ev.Type == EventBlind && ev.Action == "big_blind" {
			bigBlind = ev
		}
	}
	if bigBlind.Amount != 1 {
		t.Fatalf("carol 只交得起 1 块大盲，得到 %d", bigBlind.Amount)
	}
	if *bigBlind.Stack != 0 {
		t.Fatalf("交完之后她该一分不剩，得到 %d", *bigBlind.Stack)
	}
	playOut(t, h, events, callOrCheck)
	total := 0
	for _, s := range h.Result().Stacks {
		total += s
	}
	if total != 401 {
		t.Fatalf("筹码总额该是 401，得到 %d", total)
	}
}

func TestNewHandRejectsBadSeats(t *testing.T) {
	t.Run("一个人开不了牌", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("应该 panic")
			}
		}()
		d := NewDeck(rand.New(rand.NewPCG(1, 1)))
		NewHand(1, []Seat{{"alice", 100}}, 0, Blinds{1, 2}, d)
	})
	t.Run("没筹码的人不能入座", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("应该 panic")
			}
		}()
		d := NewDeck(rand.New(rand.NewPCG(1, 1)))
		NewHand(1, []Seat{{"alice", 100}, {"bob", 0}}, 0, Blinds{1, 2}, d)
	})
}
