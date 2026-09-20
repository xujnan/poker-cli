package client

import (
	"math/rand/v2"
	"testing"

	"github.com/xujnan/poker-cli/internal/poker"
)

// 机器人是 agent 接口的常驻回归测试（ADR-0010），所以它自己也得有测试守着。

func snapshotFor(events []poker.Event, player string) *poker.Snapshot {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == poker.EventYourTurn && events[i].Player == player {
			return events[i].Snapshot
		}
	}
	return nil
}

// TestTwoBotsFinishAHandQuickly 是那次「对加死锁」的回归测试。
//
// 原先的策略是「牌强就加注到最小加注额」。两个这样的机器人在同一条街上都拿到强牌时，
// 会一路 2→4→6→8 对加到有人推光为止：规则上完全合法，状态机也照单全收，
// 但一手牌要走上百个动作，牌桌看着就像卡死了。这条测试盯的就是「一手牌得在几十个动作内打完」。
func TestTwoBotsFinishAHandQuickly(t *testing.T) {
	const maxActions = 40
	for seed := uint64(1); seed <= 60; seed++ {
		for _, stacks := range [][]int{{200, 200}, {200, 150, 80}, {500, 60, 200, 90}} {
			seats := make([]poker.Seat, len(stacks))
			names := []string{"a", "b", "c", "d"}
			for i, st := range stacks {
				seats[i] = poker.Seat{Player: names[i], Stack: st}
			}
			d := poker.NewDeck(rand.New(rand.NewPCG(seed, 17)))
			h, events := poker.NewHand(1, seats, int(seed)%len(stacks), poker.Blinds{Small: 1, Big: 2}, d)

			actions := 0
			for !h.Done() {
				actions++
				if actions > maxActions {
					t.Fatalf("seed %d / %d 人：一手牌用了超过 %d 个动作还没打完，机器人多半在互相对加",
						seed, len(stacks), maxActions)
				}
				player := h.Turn()
				snap := snapshotFor(events, player)
				if snap == nil {
					t.Fatalf("seed %d：轮到 %s 却没给他快照", seed, player)
				}
				got := h.Apply(player, Decide(snap))
				for _, ev := range got {
					if ev.Type == poker.EventError {
						// 机器人只从合法动作列表里选，它要是被判非法，
						// 说明那份列表跟真正的规则对不上（ADR-0007 的前提就没了）。
						t.Fatalf("seed %d：机器人挑的动作被判 %s（%s）", seed, ev.Code, ev.Message)
					}
				}
				events = append(events, got...)
			}
		}
	}
}

// TestBotDoesNotReRaise：面对已经下过的注，机器人只跟不加。
//
// 这是上面那条死锁的成因，单独钉一遍：改回「强牌无脑加注」会先在这里红。
func TestBotDoesNotReRaise(t *testing.T) {
	snap := &poker.Snapshot{
		Street:    "flop",
		Hole:      poker.MustParseCards("As Ah"),
		Community: poker.MustParseCards("Ad Kc 7s"), // 三条 A，强得不能再强
		Pot:       100,
		ToCall:    20, // 已经有人下注了
		Stack:     200,
		Legal: []poker.LegalAction{
			{Action: "fold"},
			{Action: "call", Amount: 20},
			{Action: "bet", Min: 40, Max: 200},
			{Action: "allin", Amount: 200},
		},
	}
	got := Decide(snap)
	if got.Kind == poker.BetTo {
		t.Fatalf("面对下注时不该加回去，得到 %v", got)
	}
	if got.Kind != poker.Call {
		t.Fatalf("拿着三条 A 面对 20 的注，该跟，得到 %v", got)
	}
}

// TestBotBetsWhenCheckedTo：没人下注而牌又好，该主动下注——
// 一个从不下注的机器人，会让整条下注轮在自对弈里根本跑不起来。
func TestBotBetsWhenCheckedTo(t *testing.T) {
	snap := &poker.Snapshot{
		Street:    "flop",
		Hole:      poker.MustParseCards("As Ah"),
		Community: poker.MustParseCards("Ad Kc 7s"),
		Pot:       100,
		ToCall:    0,
		Stack:     200,
		Legal: []poker.LegalAction{
			{Action: "fold"},
			{Action: "check"},
			{Action: "bet", Min: 2, Max: 200},
			{Action: "allin", Amount: 200},
		},
	}
	got := Decide(snap)
	if got.Kind != poker.BetTo || got.Amount != 2 {
		t.Fatalf("没人下注又拿着三条 A，该下注到 2，得到 %v", got)
	}
}

// TestBotOnlyPicksLegalActions：机器人永远只从合法动作列表里挑。
//
// 它不自己推导此刻能不能过牌——那正是 ADR-0007 给出合法动作列表要省掉的事。
func TestBotOnlyPicksLegalActions(t *testing.T) {
	cases := []struct {
		name  string
		legal []poker.LegalAction
		hole  string
		board string
		call  int
	}{
		{"只能弃牌或跟注", []poker.LegalAction{{Action: "fold"}, {Action: "call", Amount: 10}, {Action: "allin", Amount: 10}}, "2c 7d", "Ah Kh Qs", 10},
		{"筹码不够加注", []poker.LegalAction{{Action: "fold"}, {Action: "check"}, {Action: "allin", Amount: 3}}, "As Ah", "Ad Kc 7s", 0},
		{"翻牌前没有公共牌", []poker.LegalAction{{Action: "fold"}, {Action: "call", Amount: 2}, {Action: "bet", Min: 4, Max: 100}, {Action: "allin", Amount: 100}}, "Kd Ks", "", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap := &poker.Snapshot{
				Street: "flop", Hole: poker.MustParseCards(c.hole),
				Community: poker.MustParseCards(c.board),
				ToCall:    c.call, Stack: 100, Legal: c.legal,
			}
			got := Decide(snap)
			for _, l := range c.legal {
				if l.Action == got.Kind.String() {
					return
				}
			}
			t.Fatalf("挑了个不在列表里的动作 %v，列表是 %+v", got, c.legal)
		})
	}
}

// TestRebuyerIgnoresAllInMidHand：牌局中途全下不是破产。
//
// 全下的人筹码也是 0，但他还在这手牌里，底池说不定就是他的。
// 照着中途的座位表补码，等于没破产也白拿一笔——真机跑长局时就是这么露出来的。
func TestRebuyerIgnoresAllInMidHand(t *testing.T) {
	r := rebuyer{name: "me", enabled: true}

	// 入座，从 table 事件里拿到这张桌的默认带入——补码就补到这个数。
	//
	// 以前这里是靠「第一次看见自己有多少筹码」猜的，而那个猜测在输光之后重连时
	// 必然落空（座位上本来就是 0），--rebuy 于是一声不吭，人卡在 0 筹码。
	if _, ok := r.observe(poker.Event{
		Type:  poker.EventTable,
		Buyin: 100,
		Seats: []poker.SeatView{{Player: "me", Stack: 100}},
	}); ok {
		t.Fatal("刚入座就补码？")
	}

	// 牌局中途全下：筹码显示 0，但这不是破产。
	for _, kind := range []poker.EventType{poker.EventHandStart, poker.EventStreet, poker.EventAction} {
		if _, ok := r.observe(poker.Event{
			Type:  kind,
			Seats: []poker.SeatView{{Player: "me", Stack: 0, AllIn: true}},
		}); ok {
			t.Fatalf("%s 事件里的全下被当成破产了", kind)
		}
	}

	// 这手牌打完了，确实一分不剩，这才该补。
	cmd, ok := r.observe(poker.Event{
		Type:  poker.EventHandEnd,
		Seats: []poker.SeatView{{Player: "me", Stack: 0}},
	})
	if !ok {
		t.Fatal("真破产了却没补码")
	}
	if cmd.Amount != 100 {
		t.Fatalf("该补回最初的带入 100，得到 %d", cmd.Amount)
	}

	// 补码还没到账之前不再重复发。
	if _, ok := r.observe(poker.Event{
		Type:  poker.EventSitOut,
		Seats: []poker.SeatView{{Player: "me", Stack: 0}},
	}); ok {
		t.Fatal("同一次破产补了两回")
	}
}
