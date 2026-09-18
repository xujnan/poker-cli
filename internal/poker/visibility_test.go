package poker

import (
	"math/rand/v2"
	"testing"
)

// 这个文件是 ADR-0006 的落地：信息可见性是一条被测试守住的不变量，而不是文档里的君子协定。
//
// 这类 bug 不会让程序崩溃，只会让这个 agent 环境悄悄失真——某一方多看到了一点东西，
// 训练和评测的结论就全部作废。所以这里逐格去守，而不是抽查。

// cardsIn 摘出一条事件里携带的所有牌，不管它躺在哪个字段。
//
// 故意把 Snapshot 和 Showdown.Best 也算进来：那些地方都可能夹带底牌，
// 而一条「顺手」把快照塞进广播事件的改动，正是最可能泄漏信息又最不像泄漏的那种。
func cardsIn(e Event) []Card {
	out := append([]Card(nil), e.Cards...)
	out = append(out, e.Board...)
	for _, s := range e.Showdown {
		out = append(out, s.Cards...)
		out = append(out, s.Best...)
	}
	if e.Snapshot != nil {
		out = append(out, e.Snapshot.Hole...)
		out = append(out, e.Snapshot.Community...)
	}
	return out
}

func containsCard(cards []Card, c Card) bool {
	for _, x := range cards {
		if x == c {
			return true
		}
	}
	return false
}

// showdownIndex 返回摊牌事件的位置，没有摊牌时返回 len(events)。
func showdownIndex(events []Event) int {
	for i, ev := range events {
		if ev.Type == EventShowdown {
			return i
		}
	}
	return len(events)
}

// TestHoleCardEventsAreAddressed：底牌事件必须是定向的，且只带主人自己的两张牌。
func TestHoleCardEventsAreAddressed(t *testing.T) {
	h, events := dealHand(t, []int{200, 200, 200}, 0, 42)
	events = playOut(t, h, events, callOrCheck)
	res := h.Result()

	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Type != EventHoleCards {
			continue
		}
		if ev.To == "" {
			t.Fatalf("底牌事件被广播了：%+v", ev)
		}
		if ev.To != ev.Player {
			t.Fatalf("底牌事件发给了别人：To=%s Player=%s", ev.To, ev.Player)
		}
		mine := res.Hole[ev.Player]
		if len(ev.Cards) != len(mine) {
			t.Fatalf("%s 的底牌事件有 %d 张牌，应该是 %d 张", ev.Player, len(ev.Cards), len(mine))
		}
		for _, c := range ev.Cards {
			if !containsCard(mine, c) {
				t.Fatalf("%s 的底牌事件里混进了不属于他的牌 %s", ev.Player, c)
			}
		}
		seen[ev.Player] = true
	}
	for _, p := range res.Players {
		if !seen[p] {
			t.Fatalf("%s 没有收到自己的底牌", p)
		}
	}
}

// TestYourTurnSnapshotCarriesOnlyOwnHole：轮到你时给的那份快照，
// 里面的底牌只能是你自己的。这份快照是整个 agent 接口里信息最密集的地方，
// 也因此是最容易一不小心多塞点东西进去的地方。
func TestYourTurnSnapshotCarriesOnlyOwnHole(t *testing.T) {
	h, events := dealHand(t, []int{200, 150, 80}, 0, 77)
	events = playOut(t, h, events, callOrCheck)
	res := h.Result()

	count := 0
	for _, ev := range events {
		if ev.Type != EventYourTurn {
			continue
		}
		count++
		if ev.To == "" {
			t.Fatalf("your_turn 被广播了：%+v", ev)
		}
		if ev.To != ev.Player {
			t.Fatalf("your_turn 发给了别人：To=%s Player=%s", ev.To, ev.Player)
		}
		for _, c := range ev.Snapshot.Hole {
			if !containsCard(res.Hole[ev.Player], c) {
				t.Fatalf("%s 的快照里有不属于他的牌 %s", ev.Player, c)
			}
		}
		for _, other := range res.Players {
			if other == ev.Player {
				continue
			}
			for _, c := range res.Hole[other] {
				if containsCard(cardsIn(ev), c) {
					t.Fatalf("%s 的快照里能看到 %s 的底牌 %s", ev.Player, other, c)
				}
			}
		}
	}
	if count == 0 {
		t.Fatal("这手牌里没人被问过一次，测试没测到东西")
	}
}

// TestNoBroadcastLeaksHoleCardsBeforeShowdown：摊牌之前，任何广播事件都不许带任何人的底牌。
func TestNoBroadcastLeaksHoleCardsBeforeShowdown(t *testing.T) {
	h, events := dealHand(t, []int{200, 200, 200, 200}, 0, 7)
	events = playOut(t, h, events, callOrCheck)
	res := h.Result()

	for _, ev := range events[:showdownIndex(events)] {
		if ev.To != "" {
			continue // 定向事件由别的测试管
		}
		for _, p := range res.Players {
			for _, hole := range res.Hole[p] {
				if containsCard(cardsIn(ev), hole) {
					t.Fatalf("广播事件 %s 泄漏了 %s 的底牌 %s", ev.Type, p, hole)
				}
			}
		}
	}
}

// TestFoldedHoleCardsAreNeverShown 是这一刀新长出来的那条：有人弃牌了。
//
// ADR-0006 写得很死——弃牌者的 Hole Cards 永不公开。他不进摊牌，
// 所以他的牌在发给他本人之后，就再也不该出现在任何人的事件流里，牌局结束之后也一样。
func TestFoldedHoleCardsAreNeverShown(t *testing.T) {
	// 让 alice 在 preflop 就弃牌，bob 和 carol 一路打到摊牌。
	h, events := dealHand(t, []int{200, 200, 200}, 0, 5)
	if h.Turn() != "alice" {
		t.Fatalf("这局该从 alice 开始，得到 %s", h.Turn())
	}
	events = append(events, h.Apply("alice", Action{Kind: Fold})...)
	events = playOut(t, h, events, callOrCheck)

	res := h.Result()
	if len(res.Folded) != 1 || res.Folded[0] != "alice" {
		t.Fatalf("该只有 alice 弃牌，得到 %v", res.Folded)
	}
	if len(res.Ranks) != 2 {
		t.Fatalf("摊牌的该是两个人，得到 %d 个", len(res.Ranks))
	}
	if _, ok := res.Ranks["alice"]; ok {
		t.Fatal("弃牌的人不该参与牌力比较")
	}

	// 从 alice 弃牌那一刻起，她的底牌不该再出现在任何事件里——广播的和定向给别人的都不行。
	foldIdx := 0
	for i, ev := range events {
		if ev.Type == EventAction && ev.Player == "alice" && ev.Action == "fold" {
			foldIdx = i
		}
	}
	for _, ev := range events[foldIdx:] {
		for _, c := range res.Hole["alice"] {
			if containsCard(cardsIn(ev), c) {
				t.Fatalf("弃牌之后，事件 %s（To=%q）里还带着 alice 的底牌 %s", ev.Type, ev.To, c)
			}
		}
	}
	// 摊牌里更不能有她。
	for _, ev := range events {
		if ev.Type != EventShowdown {
			continue
		}
		for _, entry := range ev.Showdown {
			if entry.Player == "alice" {
				t.Fatal("弃牌的人被拉进了摊牌")
			}
		}
	}
}

// TestFoldedPlayersStayHiddenAcrossManyHands 把上一条铺开跑：随机打几百手，
// 每一手都检查每个弃牌者的底牌有没有漏出去。
func TestFoldedPlayersStayHiddenAcrossManyHands(t *testing.T) {
	for seed := uint64(1); seed <= 120; seed++ {
		r := rand.New(rand.NewPCG(seed, 8888))
		h, events := dealHand(t, []int{200, 120, 60, 35}, int(seed)%4, seed)
		events = playOut(t, h, events, func(snap *Snapshot) Action {
			return randomLegalAction(r, snap)
		})
		res := h.Result()

		for _, folder := range res.Folded {
			// 弃牌者自己那条定向的 hole_cards 是唯一允许出现的地方。
			for _, ev := range events {
				if ev.Type == EventHoleCards && ev.To == folder {
					continue
				}
				if ev.Type == EventYourTurn && ev.To == folder {
					continue
				}
				for _, c := range res.Hole[folder] {
					if containsCard(cardsIn(ev), c) {
						t.Fatalf("seed %d：事件 %s（To=%q）泄漏了弃牌者 %s 的底牌 %s",
							seed, ev.Type, ev.To, folder, c)
					}
				}
			}
		}
	}
}

// TestEachPlayerSeesOnlyOwnHoleCards 按玩家逐个重放他真正收到的那条事件流。
//
// 这是最贴近现实的一个角度：服务端投递时只认 Visible，这里就用 Visible 把每个人的视角
// 还原出来，然后问一句「摊牌之前，你看到过别人的牌吗」。
func TestEachPlayerSeesOnlyOwnHoleCards(t *testing.T) {
	h, events := dealHand(t, []int{200, 200, 200}, 0, 2024)
	events = playOut(t, h, events, callOrCheck)
	res := h.Result()

	for _, viewer := range res.Players {
		var sawOwn bool
		for _, ev := range events[:showdownIndex(events)] {
			if !Visible(ev, viewer) {
				continue
			}
			for _, c := range cardsIn(ev) {
				for _, other := range res.Players {
					if !containsCard(res.Hole[other], c) {
						continue
					}
					if other != viewer {
						t.Fatalf("%s 在事件 %s 里看到了 %s 的底牌 %s", viewer, ev.Type, other, c)
					}
					sawOwn = true
				}
			}
		}
		if !sawOwn {
			t.Fatalf("%s 连自己的底牌都没收到", viewer)
		}
	}
}

// TestSpectatorSeesExactlyWhatAFolderSees：ADR-0006 说 Sitting Out 的玩家
// 能看到的信息严格等于一个弃牌后旁观的在座玩家。
//
// 旁观者收到的就是全部广播事件，一条定向事件都不该有——这里用一个不在牌桌上的名字
// 走一遍 Visible，确认他拿到的东西里没有任何人的底牌。
func TestSpectatorSeesExactlyWhatAFolderSees(t *testing.T) {
	h, events := dealHand(t, []int{200, 200, 200}, 0, 63)
	events = playOut(t, h, events, callOrCheck)
	res := h.Result()

	for _, ev := range events[:showdownIndex(events)] {
		if !Visible(ev, "旁观者") {
			continue
		}
		if ev.To != "" {
			t.Fatalf("旁观者不该收到任何定向事件，却收到了发给 %q 的 %s", ev.To, ev.Type)
		}
		for _, p := range res.Players {
			for _, c := range res.Hole[p] {
				if containsCard(cardsIn(ev), c) {
					t.Fatalf("旁观者在 %s 里看到了 %s 的底牌 %s", ev.Type, p, c)
				}
			}
		}
	}
}

// TestShowdownIsPublic：摊牌亮出来的牌对所有人可见，而且每个到摊牌的人都必须亮。
//
// 反过来也得守：要是哪天有人「保险起见」把摊牌也改成定向的，这个环境就从
// 不完全信息博弈变成了没人能验证结果的黑箱。
func TestShowdownIsPublic(t *testing.T) {
	h, events := dealHand(t, []int{200, 200}, 0, 99)
	events = playOut(t, h, events, callOrCheck)
	res := h.Result()

	var found bool
	for _, ev := range events {
		if ev.Type != EventShowdown {
			continue
		}
		found = true
		if ev.To != "" {
			t.Fatalf("摊牌事件不该是定向的，却发给了 %s", ev.To)
		}
		if len(ev.Showdown) != len(res.Ranks) {
			t.Fatalf("摊牌里有 %d 个人，该有 %d 个", len(ev.Showdown), len(res.Ranks))
		}
		for _, entry := range ev.Showdown {
			for _, c := range res.Hole[entry.Player] {
				if !containsCard(entry.Cards, c) {
					t.Fatalf("%s 摊牌时没亮出 %s", entry.Player, c)
				}
			}
		}
	}
	if !found {
		t.Fatal("这手牌没有摊牌事件")
	}
}

// TestGodViewStaysOutOfEvents：HandResult 是上帝视角，事件是按人裁剪的视图，
// 两者不共享底层数组。共享内存的话，一次「就地排序一下」就能让所有人的视图一起变。
func TestGodViewStaysOutOfEvents(t *testing.T) {
	h, events := dealHand(t, []int{200, 200}, 0, 5)
	events = playOut(t, h, events, callOrCheck)
	res := h.Result()

	before := append([]Card(nil), res.Hole["alice"]...)
	for i := range events {
		for j := range events[i].Cards {
			events[i].Cards[j] = Card{Rank: Two, Suit: Clubs}
		}
		if events[i].Snapshot != nil {
			for j := range events[i].Snapshot.Hole {
				events[i].Snapshot.Hole[j] = Card{Rank: Two, Suit: Clubs}
			}
		}
	}
	for i, c := range res.Hole["alice"] {
		if c != before[i] {
			t.Fatalf("改事件里的牌改到了 HandResult：%v", res.Hole["alice"])
		}
	}
}
