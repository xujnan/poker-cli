package poker

import (
	"math/rand/v2"
	"testing"
)

// 这个文件是 ADR-0006 的落地：信息可见性是一条被测试守住的不变量，而不是文档里的君子协定。
//
// 这类 bug 不会让程序崩溃，只会让这个 agent 环境悄悄失真——某一方多看到了一点东西，
// 训练和评测的结论就全部作废。所以这里逐格去守，而不是抽查。

func playTestHand(t *testing.T, players []string, seed uint64) (HandResult, []Event) {
	t.Helper()
	d := NewDeck(rand.New(rand.NewPCG(seed, 7)))
	return PlayHand(1, players, d)
}

// cardsIn 摘出一条事件里携带的所有牌，不管它躺在哪个字段。
//
// 故意把 Showdown.Best 也算进来：那五张牌里可能有底牌，一条「顺手」把 Best 塞进
// 广播事件的改动，正是最可能泄漏信息又最不像泄漏的那种。
func cardsIn(e Event) []Card {
	out := append([]Card(nil), e.Cards...)
	for _, s := range e.Showdown {
		out = append(out, s.Cards...)
		out = append(out, s.Best...)
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

// TestHoleCardEventsAreAddressed：底牌事件必须是定向的，且只带主人自己的两张牌。
func TestHoleCardEventsAreAddressed(t *testing.T) {
	players := []string{"alice", "bob", "carol"}
	res, events := playTestHand(t, players, 42)

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
	for _, p := range players {
		if !seen[p] {
			t.Fatalf("%s 没有收到自己的底牌", p)
		}
	}
}

// TestNoBroadcastLeaksHoleCardsBeforeShowdown：摊牌之前，任何广播事件都不许带任何人的底牌。
func TestNoBroadcastLeaksHoleCardsBeforeShowdown(t *testing.T) {
	players := []string{"alice", "bob", "carol", "dave"}
	res, events := playTestHand(t, players, 7)

	for _, ev := range events {
		if ev.Type == EventShowdown {
			break // 摊牌是底牌唯一合法公开的时刻，后面的事另有测试管
		}
		if ev.To != "" {
			continue // 定向事件由上一个测试管
		}
		for _, p := range players {
			for _, hole := range res.Hole[p] {
				if containsCard(cardsIn(ev), hole) {
					t.Fatalf("广播事件 %s 泄漏了 %s 的底牌 %s", ev.Type, p, hole)
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
	players := []string{"alice", "bob", "carol"}
	res, events := playTestHand(t, players, 2024)

	for _, viewer := range players {
		var sawOwn bool
		for _, ev := range events {
			if !Visible(ev, viewer) {
				continue
			}
			if ev.Type == EventShowdown {
				break
			}
			for _, c := range cardsIn(ev) {
				for _, other := range players {
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

// TestShowdownIsPublic：摊牌亮出来的牌对所有人可见，而且每个到摊牌的人都必须亮。
//
// 反过来也得守：要是哪天有人「保险起见」把摊牌也改成定向的，这个环境就从
// 不完全信息博弈变成了没人能验证结果的黑箱。
func TestShowdownIsPublic(t *testing.T) {
	players := []string{"alice", "bob"}
	res, events := playTestHand(t, players, 99)

	var found bool
	for _, ev := range events {
		if ev.Type != EventShowdown {
			continue
		}
		found = true
		if ev.To != "" {
			t.Fatalf("摊牌事件不该是定向的，却发给了 %s", ev.To)
		}
		if len(ev.Showdown) != len(players) {
			t.Fatalf("摊牌里有 %d 个人，应该是 %d 个", len(ev.Showdown), len(players))
		}
		for _, entry := range ev.Showdown {
			mine := res.Hole[entry.Player]
			for _, c := range mine {
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
	players := []string{"alice", "bob"}
	res, events := playTestHand(t, players, 5)

	before := append([]Card(nil), res.Hole["alice"]...)
	for i := range events {
		for j := range events[i].Cards {
			events[i].Cards[j] = Card{Rank: Two, Suit: Clubs}
		}
	}
	for i, c := range res.Hole["alice"] {
		if c != before[i] {
			t.Fatalf("改事件里的牌改到了 HandResult：%v", res.Hole["alice"])
		}
	}
}
