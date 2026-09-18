package server

import (
	"io"
	"testing"
	"time"

	"github.com/xujnan/poker-cli/internal/client"
	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/protocol"
)

func dialWith(t *testing.T, s *Server, name string, buyin int) *client.Session {
	t.Helper()
	sess, err := client.Dial(s.SocketDir(), s.Code(), name, buyin)
	if err != nil {
		t.Fatalf("%s 加入失败: %v", name, err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// collectUntil 读一个客户端的事件流，读到 stop 说停为止；轮到自己就照机器人的脑子打。
func collectUntil(t *testing.T, sess *client.Session, play bool, stop func([]poker.Event) bool) <-chan []poker.Event {
	t.Helper()
	out := make(chan []poker.Event, 1)
	go func() {
		var events []poker.Event
		for {
			ev, _, err := sess.Next()
			if err != nil {
				out <- events
				return
			}
			events = append(events, ev)
			if play && ev.Type == poker.EventYourTurn && ev.Snapshot != nil {
				if err := sess.Send(protocol.CommandOf(client.Decide(ev.Snapshot))); err != nil {
					out <- events
					return
				}
			}
			if stop(events) {
				out <- events
				return
			}
		}
	}()
	return out
}

// keepPlaying 让一个客户端一直打下去，不设手数上限。
//
// 用 autoPlay 那种「打满 N 手就收工」的对手来测长跑是个陷阱：他收工之后只会超时弃牌，
// 于是对面反而一路白赢，那个本该破产的人就永远破产不了。
func keepPlaying(t *testing.T, sess *client.Session) {
	t.Helper()
	collectUntil(t, sess, true, func([]poker.Event) bool { return false })
}

func has(events []poker.Event, pred func(poker.Event) bool) bool {
	for _, ev := range events {
		if pred(ev) {
			return true
		}
	}
	return false
}

// TestTopUpAppliesImmediatelyBetweenHands：没在打牌的时候补码，当场到账。
func TestTopUpAppliesImmediatelyBetweenHands(t *testing.T) {
	s := startTable(t, time.Hour) // 不开局，现在就是「两手牌之间」
	alice := dialWith(t, s, "alice", 100)

	done := collectUntil(t, alice, false, func(evs []poker.Event) bool {
		return has(evs, func(ev poker.Event) bool {
			return ev.Type == poker.EventTopUp && ev.Stack != nil
		})
	})
	if err := alice.Send(protocol.Command{Type: protocol.CmdTopUp, Amount: 50}); err != nil {
		t.Fatalf("发补码失败: %v", err)
	}

	events := waitEvents(t, done)
	var topup *poker.Event
	for i := range events {
		if events[i].Type == poker.EventTopUp && events[i].Stack != nil {
			topup = &events[i]
		}
	}
	if topup == nil {
		t.Fatal("没等到补码到账")
	}
	if topup.Amount != 50 || *topup.Stack != 150 {
		t.Fatalf("补 50 之后该有 150，得到 amount=%d stack=%d", topup.Amount, *topup.Stack)
	}
}

// TestTopUpNeverLandsMidHand：牌局进行中发的补码不会被拒绝，但筹码要等到两手牌之间才变。
//
// 这条是 ADR-0015 的落地检查，而且它能从事件流上直接看出来：
// 一条真正到账的 top_up 绝不该夹在某个 hand_start 和它的 hand_end 中间。
func TestTopUpNeverLandsMidHand(t *testing.T) {
	s := startTable(t, 10*time.Millisecond)
	// alice 带得比带入线浅，好留出补码的空间。
	alice := dialWith(t, s, "alice", 100)
	bob := dial(t, s, "bob")
	autoPlay(t, bob, 20)

	sent := false
	done := collectUntil(t, alice, true, func(evs []poker.Event) bool {
		last := evs[len(evs)-1]
		// 轮到自己的时候顺手发一条补码——这时候一定正在牌局中间。
		if !sent && last.Type == poker.EventYourTurn {
			sent = true
			_ = alice.Send(protocol.Command{Type: protocol.CmdTopUp, Amount: 50})
		}
		return has(evs, func(ev poker.Event) bool {
			return ev.Type == poker.EventTopUp && ev.Stack != nil
		})
	})

	events := waitEvents(t, done)
	inHand := false
	landed := false
	for _, ev := range events {
		switch ev.Type {
		case poker.EventHandStart:
			inHand = true
		case poker.EventHandEnd:
			inHand = false
		case poker.EventTopUp:
			if ev.Stack == nil {
				continue // 这条只是「收到了，打完这手到账」的回执
			}
			landed = true
			if inHand {
				t.Fatal("补码在牌局进行中落地了——筹码不能在一手牌中途变")
			}
		}
	}
	if !landed {
		t.Fatal("补码始终没到账")
	}
}

// TestBustAndRebuyKeepsTheTableRunning 是这一刀的正题：
// 有人输光之后，牌桌还能自动开下一手。
//
// 不补码的话，破产的人自动 Sitting Out，桌上剩不到两个有筹码的人，
// 牌局就永远停在那儿了——一张无人值守的自对弈牌桌迟早会撞上这一幕。
//
// 这里特意用单挑：人一多，浅筹码那个人有三分之一的手不用交盲注，还能靠别人互弃
// 白捡底池，几十手都未必输得光，那样这条测试就成了碰运气。单挑每手都交盲注，血流得稳。
func TestBustAndRebuyKeepsTheTableRunning(t *testing.T) {
	s := startTableWithTimeout(t, 5*time.Millisecond, 50*time.Millisecond)

	// alice 带 4 块打 1/2，输光是迟早的事；--rebuy 让她自己补回来。
	// 她跑的是真正的 poker bot 那条路，补码逻辑也是发布出去的那份。
	alice := dialWith(t, s, "alice", 4)
	go func() { _ = client.RunBot(alice, io.Discard, true) }()

	// bob 既是对手，也是这场测试的眼睛。
	bob := dialWith(t, s, "bob", 200)
	const handsAfterRebuy = 3
	done := collectUntil(t, bob, true, func(evs []poker.Event) bool {
		busted, rebought, hands := false, false, 0
		for _, ev := range evs {
			switch ev.Type {
			case poker.EventSitOut:
				if ev.Player == "alice" && ev.Message == "筹码输光了" {
					busted = true
				}
			case poker.EventTopUp:
				if ev.Player == "alice" && ev.Stack != nil {
					rebought = true
				}
			case poker.EventHandStart:
				if rebought {
					hands++
				}
			}
		}
		return busted && rebought && hands >= handsAfterRebuy
	})

	events := waitEvents(t, done)
	busted, rebought, hands := false, false, 0
	for _, ev := range events {
		switch ev.Type {
		case poker.EventSitOut:
			if ev.Player == "alice" && ev.Message == "筹码输光了" {
				busted = true
			}
		case poker.EventTopUp:
			if ev.Player == "alice" && ev.Stack != nil {
				rebought = true
			}
		case poker.EventHandStart:
			if rebought {
				hands++
			}
		}
	}
	if !busted {
		t.Fatal("alice 一直没输光，这测试没测到东西")
	}
	if !rebought {
		t.Fatal("输光之后没自动补码，牌桌到这儿就该停了")
	}
	if hands < handsAfterRebuy {
		t.Fatalf("补码之后只开了 %d 手，牌桌没真正接着跑起来", hands)
	}
}

// TestSitOutTakesEffectNextHand：暂离是从下一手开始生效的。
//
// 牌都发了、筹码都投进池子了才说不玩，那是把自己的注白送给别人。
func TestSitOutTakesEffectNextHand(t *testing.T) {
	s := startTableWithTimeout(t, 10*time.Millisecond, 50*time.Millisecond)
	alice := dial(t, s, "alice")
	keepPlaying(t, alice)
	bob := dial(t, s, "bob")
	keepPlaying(t, bob)

	carol := dial(t, s, "carol")
	sent := false
	done := collectUntil(t, carol, true, func(evs []poker.Event) bool {
		last := evs[len(evs)-1]
		if !sent && last.Type == poker.EventYourTurn {
			sent = true
			_ = carol.Send(protocol.Command{Type: protocol.CmdSitOut})
		}
		// 暂离之后再看两次开局，确认她真的没被发牌。
		if !sent {
			return false
		}
		starts := 0
		seen := false
		for _, ev := range evs {
			if ev.Type == poker.EventSitOut && ev.Player == "carol" {
				seen = true
				continue
			}
			if seen && ev.Type == poker.EventHandStart {
				starts++
			}
		}
		return starts >= 2
	})

	events := waitEvents(t, done)
	seen := false
	for _, ev := range events {
		if ev.Type == poker.EventSitOut && ev.Player == "carol" {
			seen = true
			continue
		}
		if !seen || ev.Type != poker.EventHandStart {
			continue
		}
		// hand_start 的座位表是「这手牌的参与者」，暂离的人不该在里面。
		for _, sv := range ev.Seats {
			if sv.Player == "carol" {
				t.Fatal("carol 已经暂离了，还在被发牌")
			}
		}
	}
	if !seen {
		t.Fatal("没等到 carol 的暂离事件")
	}
}

// TestBustedPlayerComesBackByToppingUp 走完破产之后回到牌桌的整条路：
// 输光 → 想回座被拦下（没钱回什么座）→ 补码 → 下一手就在牌桌上了。
//
// 最后一步没有 sitin：补完码筹码就不是 0 了，暂离的理由自然消失。
// 「输光了」和「我自己要歇会儿」是两种不同的暂离，混成一个布尔的话这里就说不清了。
func TestBustedPlayerComesBackByToppingUp(t *testing.T) {
	s := startTableWithTimeout(t, 5*time.Millisecond, 50*time.Millisecond)
	bob := dial(t, s, "bob")
	keepPlaying(t, bob)
	alice := dialWith(t, s, "alice", 4) // 单挑带 4 块打 1/2，很快就没了

	stage := 0
	done := collectUntil(t, alice, true, func(evs []poker.Event) bool {
		last := evs[len(evs)-1]
		switch stage {
		case 0:
			if last.Type == poker.EventSitOut && last.Player == "alice" && last.Message == "筹码输光了" {
				stage = 1
				_ = alice.Send(protocol.Command{Type: protocol.CmdSitIn})
			}
		case 1:
			if last.Type == poker.EventError {
				stage = 2
				_ = alice.Send(protocol.Command{Type: protocol.CmdTopUp, Amount: 4})
			}
		case 2:
			if last.Type == poker.EventTopUp && last.Stack != nil {
				stage = 3
			}
		case 3:
			if last.Type == poker.EventHandStart {
				for _, sv := range last.Seats {
					if sv.Player == "alice" {
						stage = 4
					}
				}
			}
		}
		return stage == 4
	})

	events := waitEvents(t, done)

	var sawBust, sawTopUp, sawBack bool
	var refusal *poker.Event
	toppedUpAt := -1
	for i, ev := range events {
		switch ev.Type {
		case poker.EventSitOut:
			if ev.Player == "alice" && ev.Message == "筹码输光了" {
				sawBust = true
			}
		case poker.EventError:
			if refusal == nil {
				refusal = &events[i]
			}
		case poker.EventTopUp:
			if ev.Stack != nil && ev.Player == "alice" {
				sawTopUp = true
				toppedUpAt = i
			}
		case poker.EventHandStart:
			if toppedUpAt < 0 {
				continue
			}
			for _, sv := range ev.Seats {
				if sv.Player == "alice" {
					sawBack = true
				}
			}
		}
	}
	if !sawBust {
		t.Fatal("带 4 块打 1/2 居然没输光，这测试没测到东西")
	}
	if refusal == nil || refusal.Code != "no_chips" {
		t.Fatalf("一分钱没有还想回座，该被 no_chips 拦下，得到 %+v", refusal)
	}
	if !sawTopUp {
		t.Fatal("补码没到账")
	}
	if !sawBack {
		t.Fatal("补完码之后 alice 还是没被发牌")
	}
}

// TestTopUpBeyondTheCapIsRejected：补码不能越过牌桌的带入线（ADR-0015）。
func TestTopUpBeyondTheCapIsRejected(t *testing.T) {
	s := startTable(t, time.Hour)
	alice := dialWith(t, s, "alice", testBuyin) // 已经顶格了
	drainTable(t, alice)

	if err := alice.Send(protocol.Command{Type: protocol.CmdTopUp, Amount: 1}); err != nil {
		t.Fatalf("发补码失败: %v", err)
	}
	waitForError(t, alice, "stack_at_max")
}

// TestTopUpTooBigIsRejectedWithTheRoomLeft：补多了不是静默截断，而是告诉他还能补多少。
func TestTopUpTooBigIsRejectedWithTheRoomLeft(t *testing.T) {
	s := startTable(t, time.Hour)
	alice := dialWith(t, s, "alice", 150)
	drainTable(t, alice)

	if err := alice.Send(protocol.Command{Type: protocol.CmdTopUp, Amount: 500}); err != nil {
		t.Fatalf("发补码失败: %v", err)
	}
	got := make(chan poker.Event, 1)
	go func() {
		for {
			ev, _, err := alice.Next()
			if err != nil {
				close(got)
				return
			}
			if ev.Type == poker.EventError {
				got <- ev
				return
			}
		}
	}()
	select {
	case ev, ok := <-got:
		if !ok {
			t.Fatal("连接断了")
		}
		if ev.Code != "topup_too_big" {
			t.Fatalf("想要 topup_too_big，得到 %+v", ev)
		}
		if ev.Max != testBuyin-150 {
			t.Fatalf("该告诉他还能补 %d，得到 %d", testBuyin-150, ev.Max)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等错误超时")
	}
}

// drainTable 把加入时那几条事件读掉，免得干扰后面的断言。
func drainTable(t *testing.T, sess *client.Session) {
	t.Helper()
	if _, _, err := sess.Next(); err != nil {
		t.Fatalf("没收到牌桌快照: %v", err)
	}
}
