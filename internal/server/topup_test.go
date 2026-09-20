package server

import (
	"io"
	"testing"
	"time"

	"github.com/xujnan/poker-cli/internal/client"
	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/protocol"
	"github.com/xujnan/poker-cli/internal/transport"
)

func dialWith(t *testing.T, s *Server, name string, buyin int) *client.Session {
	t.Helper()
	sess, err := client.Dial(s.Transport(), s.Code(), name, buyin)
	if err != nil {
		t.Fatalf("%s 加入失败: %v", name, err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// collectUntil 读一个客户端的事件流，读到 stop 说停为止；轮到自己就照机器人的脑子打。
// foldAlways 是一个只会弃牌的大脑，用来让「输光」成为必然而不是碰运气。
//
// 一个会打牌的浅筹码玩家，筹码是个鞅：他可能先涨上去，于是「他会输光」在任何
// 固定时限里都不保证发生（实测 17% 的运行会因此超时）。只弃牌的人不一样——
// 每圈固定输掉自己的小盲，唯一的进账是对手弃牌时那一个小盲，净额每圈 ≤ 0，
// 而且筹码封了顶：他只在盲注超过自己筹码时才被动全下，最多翻一倍就回到原点。
// 于是输光有界且很快。
func foldAlways(*poker.Snapshot) poker.Action { return poker.Action{Kind: poker.Fold} }

// aliceBuyin 是那个注定要输光的人带多少上桌：两个大盲，单挑一圈就见底。
const aliceBuyin = 4

func collectUntil(t *testing.T, sess *client.Session, play bool, stop func([]poker.Event) bool) <-chan []poker.Event {
	t.Helper()
	decide := client.Decide
	if !play {
		decide = nil
	}
	return collectUntilWith(t, sess, decide, stop)
}

// collectUntilWith 跟 collectUntil 一样，但可以指定用哪个大脑；decide 为 nil 表示只看不玩。
func collectUntilWith(t *testing.T, sess *client.Session, decide func(*poker.Snapshot) poker.Action, stop func([]poker.Event) bool) <-chan []poker.Event {
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
			if decide != nil && ev.Type == poker.EventYourTurn && ev.Snapshot != nil {
				if err := sess.Send(protocol.CommandOf(decide(ev.Snapshot))); err != nil {
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

	// alice 带 4 块打 1/2，只弃牌，所以输光是必然而不是碰运气（见 foldAlways）。
	// 她跑的仍是真正的 poker bot 那条路，--rebuy 的补码逻辑就是发布出去的那份——
	// 换掉的只有大脑，被测的那半点没动。
	alice := dialWith(t, s, "alice", aliceBuyin)
	go func() { _ = client.RunBotWith(alice, io.Discard, true, foldAlways) }()

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

	// 这条测试不再碰运气，靠的是「只弃牌的人涨不起来」这个性质，所以把它钉住。
	//
	// 界放在「翻倍」而不是「等于带入」：她确实能短暂涨过带入——当大盲投出 2 之后
	// 对手小盲弃牌，她白收自己那 2 加对手那 1，于是 4 变 5。但也就到此为止，
	// 她赢得回来的最多是自己投出去的盲注加对手的小盲，翻倍是做不到的。
	// 哪天有人把 foldAlways 换回会打牌的大脑，这一条会先红（那次实测冲到了 45），
	// 而不是让测试重新开始偶尔超时。
	const cantDouble = 2 * aliceBuyin
	for _, ev := range events {
		for _, sv := range ev.Seats {
			if sv.Player == "alice" && sv.Stack > cantDouble {
				t.Fatalf("alice 只弃牌却攒到了 %d 个筹码，这条测试又回去赌骰子了", sv.Stack)
			}
		}
	}

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
	// 单挑带 4 块打 1/2，而且只弃牌——输光因此是必然的，不是碰运气（见 foldAlways）。
	alice := dialWith(t, s, "alice", 4)

	stage := 0
	done := collectUntilWith(t, alice, foldAlways, func(evs []poker.Event) bool {
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

// cappedTable 开一张带入上限等于默认带入的桌——「顶格就不能再补」那套规矩的经典形状。
func cappedTable(t *testing.T) *Server {
	t.Helper()
	tr, err := transport.NewUnix(t.TempDir())
	if err != nil {
		t.Fatalf("造不出传输: %v", err)
	}
	return startTableCapped(t, tr, time.Hour, 0, testBuyin)
}

// TestTopUpBeyondTheCapIsRejected：设了上限之后，补码不能越过它（ADR-0015）。
func TestTopUpBeyondTheCapIsRejected(t *testing.T) {
	s := cappedTable(t)
	alice := dialWith(t, s, "alice", testBuyin) // 已经顶格了
	drainTable(t, alice)

	if err := alice.Send(protocol.Command{Type: protocol.CmdTopUp, Amount: 1}); err != nil {
		t.Fatalf("发补码失败: %v", err)
	}
	waitForError(t, alice, "stack_at_max")
}

// TestTopUpTooBigIsRejectedWithTheRoomLeft：补多了不是静默截断，而是告诉他还能补多少。
func TestTopUpTooBigIsRejectedWithTheRoomLeft(t *testing.T) {
	s := cappedTable(t)
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

// TestNoCapByDefault：默认这张桌不管你有多少筹码，补码想补多少补多少。
//
// 以前上限是写死的，而且只拦补码这一条路——本地几个人打着玩、或者跑 agent 评测时，
// 「想深筹码打就深筹码打」才是常态，不该被一个默认值拦住。
func TestNoCapByDefault(t *testing.T) {
	s := startTable(t, time.Hour)
	alice := dialWith(t, s, "alice", testBuyin) // 已经是默认带入了
	drainTable(t, alice)

	const way = testBuyin * 50
	if err := alice.Send(protocol.Command{Type: protocol.CmdTopUp, Amount: way}); err != nil {
		t.Fatalf("发补码失败: %v", err)
	}
	got := make(chan poker.Event, 1)
	go func() {
		for {
			ev, _, err := alice.Next()
			if err != nil {
				return
			}
			if ev.Type == poker.EventTopUp || ev.Type == poker.EventError {
				got <- ev
				return
			}
		}
	}()
	select {
	case ev := <-got:
		if ev.Type == poker.EventError {
			t.Fatalf("默认不该有上限，却被 %s 拦下了：%s", ev.Code, ev.Message)
		}
		if ev.Stack == nil || *ev.Stack != testBuyin+way {
			t.Fatalf("补完该有 %d，事件说 %+v", testBuyin+way, ev.Stack)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等补码到账超时")
	}
}

// TestCapCoversJoinToo：设了上限，带着超额的钱直接坐下也要被拦。
//
// 这是一个真漏洞的回归测试：以前上限只写在补码那条路上，而 join 一分钱不限——
// 一张默认带入 200 的桌，`--buyin 999999` 直接就坐下了。想变成桌上最深的筹码
// 根本不用走补码，那条上限于是只是个摆设。规矩要么两边都管，要么两边都不管。
func TestCapCoversJoinToo(t *testing.T) {
	s := cappedTable(t)
	over, err := client.Dial(s.Transport(), s.Code(), "土豪", testBuyin*100)
	if err != nil {
		t.Fatalf("连不上: %v", err)
	}
	defer over.Close()

	ev, _, err := over.Next()
	if err != nil {
		t.Fatalf("该收到一条错误，却读不到: %v", err)
	}
	if ev.Type != poker.EventError || ev.Code != "buyin_too_big" {
		t.Fatalf("该被 buyin_too_big 拦下，得到 %+v", ev)
	}
	if ev.Max != testBuyin {
		t.Fatalf("错误里该带着上限 %d，得到 %d", testBuyin, ev.Max)
	}

	// 没设上限的桌上，同样的带入要放行——否则就是把默认值又变回了硬规矩。
	plain := startTable(t, time.Hour)
	rich, err := client.Dial(plain.Transport(), plain.Code(), "土豪", testBuyin*100)
	if err != nil {
		t.Fatalf("连不上: %v", err)
	}
	defer rich.Close()
	ev, _, err = rich.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != poker.EventTable {
		t.Fatalf("没设上限的桌该直接让他坐下，得到 %+v", ev)
	}
}

// TestRebuyWorksAfterReconnect 是一个真 bug 的回归测试。
//
// 输光之后断线、再用 `join --as 同一个名字 --rebuy` 连回来，--rebuy 一声不吭，
// 人就卡在 0 筹码 Sitting Out，牌桌上白少一个人。
//
// 原因是补码目标是**猜**出来的：「第一次看见自己有多少筹码」。这个猜测在别的路上
// 都成立，唯独这一条上必然落空——重连时座位上本来就是 0，于是永远学不到目标。
// 现在不猜了：自己报的带入自己知道，牌桌的默认值写在 table 事件里。
func TestRebuyWorksAfterReconnect(t *testing.T) {
	s := startTableWithTimeout(t, 5*time.Millisecond, 50*time.Millisecond)
	bob := dial(t, s, "bob")
	keepPlaying(t, bob)

	alice := dialWith(t, s, "alice", aliceBuyin)
	busted := make(chan struct{})
	go func() {
		var once bool
		for {
			ev, _, err := alice.Next()
			if err != nil {
				return
			}
			if ev.Type == poker.EventYourTurn && ev.Snapshot != nil {
				_ = alice.Send(protocol.CommandOf(poker.Action{Kind: poker.Fold}))
			}
			if !once && ev.Type == poker.EventSitOut && ev.Player == "alice" && ev.Message == "筹码输光了" {
				once = true
				close(busted)
			}
		}
	}()
	select {
	case <-busted:
	case <-time.After(5 * time.Second):
		t.Fatal("alice 没输光")
	}
	alice.Close()

	// 重连，带 --rebuy。
	back, err := client.Dial(s.Transport(), s.Code(), "alice", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer back.Close()
	go func() { _ = client.RunBotWith(back, io.Discard, true, foldAlways) }()

	// 找个只看不玩的观察者来盯，bob 的事件流已经被 keepPlaying 吃掉了。
	watch := dial(t, s, "watch")
	seen := make(chan int, 1)
	go func() {
		for {
			ev, _, err := watch.Next()
			if err != nil {
				return
			}
			if ev.Type == poker.EventTopUp && ev.Player == "alice" && ev.Stack != nil {
				seen <- *ev.Stack
				return
			}
		}
	}()
	select {
	case got := <-seen:
		t.Logf("补上了，现在有 %d", got)
	case <-time.After(3 * time.Second):
		t.Fatal("重连之后 --rebuy 没有补码，alice 永远停在 0 筹码")
	}
}
