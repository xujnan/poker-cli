package poker

import (
	"math/rand/v2"
	"testing"
)

func TestPlayHandDealsDistinctCards(t *testing.T) {
	players := []string{"alice", "bob", "carol"}
	d := NewDeck(rand.New(rand.NewPCG(1, 2)))
	res, _ := PlayHand(1, players, d)

	seen := map[Card]string{}
	for _, p := range players {
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
	if len(res.Community) != communityCardCount {
		t.Fatalf("公共牌有 %d 张", len(res.Community))
	}
	for _, c := range res.Community {
		if owner, dup := seen[c]; dup {
			t.Fatalf("公共牌 %s 也发给了 %s", c, owner)
		}
		seen[c] = "board"
	}
	if want := 52 - (len(players)*holeCardCount + communityCardCount); d.Remaining() != want {
		t.Fatalf("牌堆剩 %d 张，应该剩 %d 张", d.Remaining(), want)
	}
}

// TestPlayHandIsReproducible 是 ADR-0004 那条 --seed 的意义所在：
// 同一个种子必须打出同一手牌，否则边池这类逻辑将来根本没法写回归测试。
func TestPlayHandIsReproducible(t *testing.T) {
	players := []string{"alice", "bob", "carol"}
	run := func() HandResult {
		d := NewDeck(rand.New(rand.NewPCG(20240601, 0x9E3779B97F4A7C15)))
		res, _ := PlayHand(1, players, d)
		return res
	}
	a, b := run(), run()

	for _, p := range players {
		for i := range a.Hole[p] {
			if a.Hole[p][i] != b.Hole[p][i] {
				t.Fatalf("同种子两次运行，%s 的底牌不同：%v vs %v", p, a.Hole[p], b.Hole[p])
			}
		}
	}
	for i := range a.Community {
		if a.Community[i] != b.Community[i] {
			t.Fatalf("同种子两次运行，公共牌不同：%v vs %v", a.Community, b.Community)
		}
	}
	if len(a.Winners) != len(b.Winners) || a.Winners[0] != b.Winners[0] {
		t.Fatalf("同种子两次运行，赢家不同：%v vs %v", a.Winners, b.Winners)
	}
}

func TestPlayHandWinnersHaveTheBestRank(t *testing.T) {
	players := []string{"alice", "bob", "carol", "dave"}
	// 多跑几个种子，顺便扫到平局那条分支。
	for seed := uint64(1); seed <= 200; seed++ {
		d := NewDeck(rand.New(rand.NewPCG(seed, 3)))
		res, _ := PlayHand(1, players, d)
		if len(res.Winners) == 0 {
			t.Fatalf("seed %d：一手牌没有赢家", seed)
		}
		best := res.Ranks[res.Winners[0]]
		for _, p := range players {
			cmp := res.Ranks[p].Compare(best)
			if cmp > 0 {
				t.Fatalf("seed %d：%s 的牌力比赢家还强", seed, p)
			}
			isWinner := false
			for _, w := range res.Winners {
				if w == p {
					isWinner = true
					break
				}
			}
			if isWinner != (cmp == 0) {
				t.Fatalf("seed %d：%s 是否赢家(%v) 与牌力比较(%d) 不一致", seed, p, isWinner, cmp)
			}
		}
	}
}

// TestPlayHandEventSequence 守住事件的形状与顺序：agent 是照着这个顺序写状态机的。
func TestPlayHandEventSequence(t *testing.T) {
	players := []string{"alice", "bob"}
	d := NewDeck(rand.New(rand.NewPCG(11, 22)))
	_, events := PlayHand(3, players, d)

	want := []EventType{
		EventHandStart,
		EventHoleCards, EventHoleCards,
		EventCommunityCards,
		EventShowdown,
		EventHandEnd,
	}
	if len(events) != len(want) {
		t.Fatalf("事件数 = %d，想要 %d：%+v", len(events), len(want), events)
	}
	for i, w := range want {
		if events[i].Type != w {
			t.Fatalf("第 %d 条事件是 %s，想要 %s", i, events[i].Type, w)
		}
		if events[i].Hand != 3 {
			t.Fatalf("第 %d 条事件的手数是 %d，想要 3", i, events[i].Hand)
		}
	}

	end := events[len(events)-1]
	if end.Pot == nil {
		t.Fatal("hand_end 必须带底池字段，哪怕它是 0")
	}
	if *end.Pot != 0 {
		t.Fatalf("第一刀没有下注，底池应该是 0，得到 %d", *end.Pot)
	}
	if len(end.Winners) == 0 {
		t.Fatal("hand_end 没有赢家")
	}
}

func TestPlayHandRejectsLonePlayer(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("一个人也能开一手牌？应该 panic")
		}
	}()
	d := NewDeck(rand.New(rand.NewPCG(1, 1)))
	PlayHand(1, []string{"alice"}, d)
}
