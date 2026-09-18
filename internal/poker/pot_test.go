package poker

import "testing"

func potOf(seats ...PotSeat) []Pot { return BuildPots(seats) }

func TestBuildPotsSingleLayer(t *testing.T) {
	pots := potOf(
		PotSeat{Player: "alice", Committed: 100},
		PotSeat{Player: "bob", Committed: 100},
	)
	if len(pots) != 1 {
		t.Fatalf("投入相同该只有一个池，得到 %d 个", len(pots))
	}
	if pots[0].Amount != 200 {
		t.Fatalf("池里该有 200，得到 %d", pots[0].Amount)
	}
	if len(pots[0].Eligible) != 2 {
		t.Fatalf("两个人都有资格，得到 %v", pots[0].Eligible)
	}
}

// TestBuildPotsSidePot：有人 all-in 得少，超出的部分要独立成池。
func TestBuildPotsSidePot(t *testing.T) {
	pots := potOf(
		PotSeat{Player: "alice", Committed: 50}, // all-in
		PotSeat{Player: "bob", Committed: 100},
		PotSeat{Player: "carol", Committed: 100},
	)
	if len(pots) != 2 {
		t.Fatalf("该切成主池加一个边池，得到 %d 个", len(pots))
	}
	if pots[0].Amount != 150 || len(pots[0].Eligible) != 3 {
		t.Fatalf("主池该是 150 且三人有资格，得到 %+v", pots[0])
	}
	if pots[1].Amount != 100 || len(pots[1].Eligible) != 2 {
		t.Fatalf("边池该是 100 且只有 bob、carol 有资格，得到 %+v", pots[1])
	}
	for _, p := range pots[1].Eligible {
		if p == "alice" {
			t.Fatal("alice 匹配不了边池，不该有资格")
		}
	}
}

// TestBuildPotsKeepsFoldedChips：弃牌者投进去的钱留在池里，但他没资格分。
func TestBuildPotsKeepsFoldedChips(t *testing.T) {
	pots := potOf(
		PotSeat{Player: "alice", Committed: 100},
		PotSeat{Player: "bob", Committed: 40, Folded: true},
		PotSeat{Player: "carol", Committed: 100},
	)
	total := 0
	for _, p := range pots {
		total += p.Amount
		for _, e := range p.Eligible {
			if e == "bob" {
				t.Fatalf("弃牌的 bob 不该出现在 %+v 的资格名单里", p)
			}
		}
	}
	if total != 240 {
		t.Fatalf("池子总额该是 240（含 bob 弃掉的 40），得到 %d", total)
	}
}

// TestBuildPotsReturnsUnmatchedBet：加注了没人跟，多出来的那部分独立成池，
// 池里只有他一个人有资格，等于原样退还。
func TestBuildPotsReturnsUnmatchedBet(t *testing.T) {
	pots := potOf(
		PotSeat{Player: "alice", Committed: 100},
		PotSeat{Player: "bob", Committed: 10, Folded: true},
	)
	_, payout := AwardPots(pots, map[string]HandRank{}, []string{"alice", "bob"})
	if payout["alice"] != 110 {
		t.Fatalf("alice 该拿回自己的 100 外加 bob 的 10，得到 %d", payout["alice"])
	}
}

// TestAwardPotsSplitsEvenly：牌力完全一样就平分。
func TestAwardPotsSplitsEvenly(t *testing.T) {
	pots := potOf(
		PotSeat{Player: "alice", Committed: 50},
		PotSeat{Player: "bob", Committed: 50},
	)
	tie := Evaluate(MustParseCards("Ac Kd Qh Js Tc"))
	awarded, payout := AwardPots(pots, map[string]HandRank{"alice": tie, "bob": tie}, []string{"alice", "bob"})
	if payout["alice"] != 50 || payout["bob"] != 50 {
		t.Fatalf("该一人一半，得到 %v", payout)
	}
	if len(awarded[0].Winners) != 2 {
		t.Fatalf("两个人都是赢家，得到 %v", awarded[0].Winners)
	}
}

// TestAwardPotsOddChipGoesByPosition：除不尽的那一块按座位顺序给，
// 从 Button 左手第一位开始——真实牌桌就是这么发的。
func TestAwardPotsOddChipGoesByPosition(t *testing.T) {
	pots := []Pot{{Amount: 10, Eligible: []string{"alice", "bob", "carol"}}}
	tie := Evaluate(MustParseCards("Ac Kd Qh Js Tc"))
	ranks := map[string]HandRank{"alice": tie, "bob": tie, "carol": tie}

	_, payout := AwardPots(pots, ranks, []string{"bob", "carol", "alice"})
	if payout["bob"] != 4 || payout["carol"] != 3 || payout["alice"] != 3 {
		t.Fatalf("多出来的那块该归顺序上的第一位 bob，得到 %v", payout)
	}
	total := payout["alice"] + payout["bob"] + payout["carol"]
	if total != 10 {
		t.Fatalf("分完还是 10 块，得到 %d", total)
	}
}

// TestAwardPotsEachLayerJudgedSeparately：主池和边池分别判赢家。
//
// 牌力最强的人如果 all-in 得少，他只能赢主池，边池归剩下两人里牌大的那个。
// 这是边池最容易算错的地方：一不小心就会让最强的那手牌把边池也卷走。
func TestAwardPotsEachLayerJudgedSeparately(t *testing.T) {
	pots := potOf(
		PotSeat{Player: "alice", Committed: 50},
		PotSeat{Player: "bob", Committed: 100},
		PotSeat{Player: "carol", Committed: 100},
	)
	ranks := map[string]HandRank{
		"alice": Evaluate(MustParseCards("As Ks Qs Js Ts")), // 最强，但只投了 50
		"bob":   Evaluate(MustParseCards("9c 9d 9h 9s 2c")),
		"carol": Evaluate(MustParseCards("3c 3d 3h 8s 8c")),
	}
	awarded, payout := AwardPots(pots, ranks, []string{"alice", "bob", "carol"})

	if len(awarded[0].Winners) != 1 || awarded[0].Winners[0] != "alice" {
		t.Fatalf("主池该归牌最大的 alice，得到 %v", awarded[0].Winners)
	}
	if len(awarded[1].Winners) != 1 || awarded[1].Winners[0] != "bob" {
		t.Fatalf("边池 alice 没资格，该归 bob，得到 %v", awarded[1].Winners)
	}
	if payout["alice"] != 150 || payout["bob"] != 100 || payout["carol"] != 0 {
		t.Fatalf("分配不对：%v", payout)
	}
}

// TestAwardPotsWithoutShowdown：没人摊牌（都弃了只剩一个）时，池子直接归有资格的那个人。
func TestAwardPotsWithoutShowdown(t *testing.T) {
	pots := potOf(
		PotSeat{Player: "alice", Committed: 20},
		PotSeat{Player: "bob", Committed: 5, Folded: true},
	)
	awarded, payout := AwardPots(pots, map[string]HandRank{}, []string{"alice", "bob"})
	if payout["alice"] != 25 {
		t.Fatalf("alice 该拿走全部 25，得到 %d", payout["alice"])
	}
	for _, p := range awarded {
		for _, w := range p.Winners {
			if w == "bob" {
				t.Fatal("弃牌的人赢了池子")
			}
		}
	}
}

// TestBuildPotsThreeWayAllIn：三个人三种筹码深度，切成三层。
func TestBuildPotsThreeWayAllIn(t *testing.T) {
	pots := potOf(
		PotSeat{Player: "alice", Committed: 10},
		PotSeat{Player: "bob", Committed: 50},
		PotSeat{Player: "carol", Committed: 200},
	)
	if len(pots) != 3 {
		t.Fatalf("三种深度该切三层，得到 %d 层", len(pots))
	}
	want := []struct {
		amount int
		n      int
	}{{30, 3}, {80, 2}, {150, 1}}
	for i, w := range want {
		if pots[i].Amount != w.amount || len(pots[i].Eligible) != w.n {
			t.Fatalf("第 %d 层该是 %d 筹码 %d 人有资格，得到 %+v", i, w.amount, w.n, pots[i])
		}
	}
	total := 0
	for _, p := range pots {
		total += p.Amount
	}
	if total != 260 {
		t.Fatalf("三层加起来该是 260，得到 %d", total)
	}
}
