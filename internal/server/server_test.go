package server

import (
	"io"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/xujnan/poker-cli/internal/client"
	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/protocol"
	"github.com/xujnan/poker-cli/internal/transport"
)

// 这个文件测的是真的跑起来的那条路：真的 Unix socket、真的几个客户端进程、真的事件流。
// 纯核心里的可见性测试守的是事件该长什么样，这里守的是投递关口有没有照着做。

const testBuyin = 200

func startTable(t *testing.T, handDelay time.Duration) *Server {
	return startTableWithTimeout(t, handDelay, 0)
}

func startTableWithTimeout(t *testing.T, handDelay, timeout time.Duration) *Server {
	t.Helper()
	tr, err := transport.NewUnix(t.TempDir())
	if err != nil {
		t.Fatalf("造不出传输: %v", err)
	}
	return startTableOn(t, tr, handDelay, timeout)
}

// startTableOn 在指定传输上开一张牌桌。牌桌逻辑不该关心底下是什么（ADR-0001），
// 所以这个函数收一个 Transport，测试就能拿同一套断言去跑不同的传输。
func startTableOn(t *testing.T, tr transport.Transport, handDelay, timeout time.Duration) *Server {
	t.Helper()
	s, err := New(Options{
		Transport:     tr,
		Rand:          rand.New(rand.NewPCG(20240918, 5)),
		HandDelay:     handDelay,
		ActionTimeout: timeout,
		Blinds:        poker.Blinds{Small: 1, Big: 2},
		Buyin:         testBuyin,
		Log:           io.Discard,
	})
	if err != nil {
		t.Fatalf("开桌失败: %v", err)
	}
	go func() {
		if err := s.Serve(); err != nil {
			t.Errorf("Serve 出错: %v", err)
		}
	}()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func dial(t *testing.T, s *Server, name string) *client.Session {
	t.Helper()
	sess, err := client.Dial(s.Transport(), s.Code(), name, 0)
	if err != nil {
		t.Fatalf("%s 加入失败: %v", name, err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}

// autoPlay 在后台读一个客户端的事件流，轮到自己就用机器人的大脑决定动作，
// 收满 hands 手牌之后把事件全部交出来。
//
// 它走的是 client.Decide——跟 poker bot 那个独立进程用的是同一个函数，
// 所以这里顺带也在测 agent 那条路（ADR-0010）。
func autoPlay(t *testing.T, sess *client.Session, hands int) <-chan []poker.Event {
	t.Helper()
	out := make(chan []poker.Event, 1)
	go func() {
		var events []poker.Event
		ended := 0
		for {
			ev, _, err := sess.Next()
			if err != nil {
				out <- events
				return
			}
			events = append(events, ev)
			if ev.Type == poker.EventYourTurn && ev.Snapshot != nil {
				if err := sess.Send(protocol.CommandOf(client.Decide(ev.Snapshot))); err != nil {
					out <- events
					return
				}
			}
			if ev.Type == poker.EventHandEnd {
				ended++
				if ended >= hands {
					out <- events
					return
				}
			}
		}
	}()
	return out
}

func waitEvents(t *testing.T, ch <-chan []poker.Event) []poker.Event {
	t.Helper()
	select {
	case evs := <-ch:
		return evs
	case <-time.After(10 * time.Second):
		t.Fatal("等事件超时")
		return nil
	}
}

func firstOf(events []poker.Event, kind poker.EventType) *poker.Event {
	for i := range events {
		if events[i].Type == kind {
			return &events[i]
		}
	}
	return nil
}

func countOf(events []poker.Event, kind poker.EventType) int {
	n := 0
	for _, ev := range events {
		if ev.Type == kind {
			n++
		}
	}
	return n
}

// TestTwoPlayersPlayABettingHand 是这一刀的贯通测试：
// serve → join → 盲注 → 下注轮 → 分池 → 结束，全程走 socket。
func TestTwoPlayersPlayABettingHand(t *testing.T) {
	s := startTable(t, 10*time.Millisecond)
	alice := dial(t, s, "alice")
	aliceEvents := autoPlay(t, alice, 1)
	bob := dial(t, s, "bob")
	bobEvents := autoPlay(t, bob, 1)

	got := map[string][]poker.Event{
		"alice": waitEvents(t, aliceEvents),
		"bob":   waitEvents(t, bobEvents),
	}

	for name, events := range got {
		if firstOf(events, poker.EventHandStart) == nil {
			t.Fatalf("%s 没看到开局", name)
		}
		if countOf(events, poker.EventBlind) != 2 {
			t.Fatalf("%s 该看到两个盲注，得到 %d 个", name, countOf(events, poker.EventBlind))
		}
		if countOf(events, poker.EventHoleCards) != 1 {
			t.Fatalf("%s 该只收到一份底牌，得到 %d 份", name, countOf(events, poker.EventHoleCards))
		}
		if countOf(events, poker.EventAction) == 0 {
			t.Fatalf("%s 没看到任何动作，下注轮根本没跑起来", name)
		}
		end := firstOf(events, poker.EventHandEnd)
		if end == nil {
			t.Fatalf("%s 没看到牌局结束", name)
		}
		if end.Pot == nil || *end.Pot < 3 {
			t.Fatalf("%s 看到的底池是 %v，至少该有两个盲注", name, end.Pot)
		}
		if firstOf(events, poker.EventPotAwarded) == nil {
			t.Fatalf("%s 没看到分池", name)
		}
		for _, ev := range events {
			if ev.Type == poker.EventError {
				t.Fatalf("%s 收到错误事件：%s %s", name, ev.Code, ev.Message)
			}
		}
	}

	// 筹码守恒：这一条在进程之间也得成立。
	end := firstOf(got["alice"], poker.EventHandEnd)
	total := 0
	for _, sv := range end.Seats {
		total += sv.Stack
	}
	if total != 2*testBuyin {
		t.Fatalf("两人带入共 %d，牌局结束后桌上却有 %d", 2*testBuyin, total)
	}
}

// TestTableRunsOverAnyTransport 是这次重构要兑现的那句话：
// 换传输而不动牌局逻辑（ADR-0001）。
//
// 同一套断言跑在两个完全不同的传输上——一个走 Unix socket 和文件系统，
// 一个纯在内存里。牌桌、客户端、事件流的代码一个字都没变。
// 哪天真要加 TCP，这里再挂一个实现就行；要是这条测试写不出来，
// 那个「收敛到接口后面」就只是把 net.Dial 挪了个地方。
func TestTableRunsOverAnyTransport(t *testing.T) {
	unix, err := transport.NewUnix(t.TempDir())
	if err != nil {
		t.Fatalf("造不出同机传输: %v", err)
	}
	transports := map[string]transport.Transport{
		"unix":   unix,
		"memory": transport.NewMemory(),
	}

	for name, tr := range transports {
		t.Run(name, func(t *testing.T) {
			s := startTableOn(t, tr, 10*time.Millisecond, 0)

			alice := dial(t, s, "alice")
			aliceEvents := autoPlay(t, alice, 1)
			bob := dial(t, s, "bob")
			bobEvents := autoPlay(t, bob, 1)

			for who, events := range map[string][]poker.Event{
				"alice": waitEvents(t, aliceEvents),
				"bob":   waitEvents(t, bobEvents),
			} {
				if firstOf(events, poker.EventHandStart) == nil {
					t.Fatalf("%s 没看到开局", who)
				}
				if countOf(events, poker.EventHoleCards) != 1 {
					t.Fatalf("%s 该只收到一份底牌，得到 %d 份", who, countOf(events, poker.EventHoleCards))
				}
				end := firstOf(events, poker.EventHandEnd)
				if end == nil {
					t.Fatalf("%s 没看到牌局结束", who)
				}
				total := 0
				for _, sv := range end.Seats {
					total += sv.Stack
				}
				if total != 2*testBuyin {
					t.Fatalf("%s 看到的筹码总额是 %d，该是 %d", who, total, 2*testBuyin)
				}
				for _, ev := range events {
					if ev.Type == poker.EventError {
						t.Fatalf("%s 收到错误事件：%s %s", who, ev.Code, ev.Message)
					}
				}
			}
		})
	}
}

// TestYourTurnCarriesLegalActions：轮到你的时候，服务端必须把合法动作一起递过来（ADR-0007）。
func TestYourTurnCarriesLegalActions(t *testing.T) {
	s := startTable(t, 10*time.Millisecond)
	alice := dial(t, s, "alice")
	bob := dial(t, s, "bob")
	autoPlay(t, bob, 20)

	// 按「读到 alice 真的被问一次」收敛，而不是按手数：单挑时 button 先说话，
	// 他一弃牌，另一个人这手就完全轮不到——按手数等会等在一局正确的牌上。
	got := make(chan poker.Event, 1)
	go func() {
		for {
			ev, _, err := alice.Next()
			if err != nil {
				close(got)
				return
			}
			if ev.Type == poker.EventYourTurn {
				got <- ev
				return
			}
			_ = err
		}
	}()

	var turn poker.Event
	select {
	case ev, ok := <-got:
		if !ok {
			t.Fatal("连接断了，alice 一次都没被问过")
		}
		turn = ev
	case <-time.After(10 * time.Second):
		t.Fatal("等 alice 的 your_turn 超时")
	}

	if turn.Snapshot == nil {
		t.Fatal("your_turn 没带快照")
	}
	if len(turn.Snapshot.Legal) == 0 {
		t.Fatal("快照里没有合法动作列表")
	}
	if len(turn.Snapshot.Hole) != 2 {
		t.Fatalf("快照里该有自己的两张底牌，得到 %v", turn.Snapshot.Hole)
	}
	if len(turn.Snapshot.Seats) != 2 {
		t.Fatalf("快照该带上全桌的筹码，得到 %d 个座位", len(turn.Snapshot.Seats))
	}
	if turn.Snapshot.Stack <= 0 {
		t.Fatalf("快照该带上自己的筹码，得到 %d", turn.Snapshot.Stack)
	}
}

// TestSocketNeverLeaksOtherHoleCards 是 ADR-0006 在真实投递路径上的那一道：
// 摊牌之前，从 socket 上流过来的字节里不能出现对手的底牌。
func TestSocketNeverLeaksOtherHoleCards(t *testing.T) {
	s := startTable(t, 10*time.Millisecond)
	alice := dial(t, s, "alice")
	aliceEvents := autoPlay(t, alice, 2)
	bob := dial(t, s, "bob")
	bobEvents := autoPlay(t, bob, 2)

	aliceSaw := waitEvents(t, aliceEvents)
	bobSaw := waitEvents(t, bobEvents)

	// 按手数把各自的底牌收集起来。
	holeByHand := func(events []poker.Event) map[int][]poker.Card {
		out := map[int][]poker.Card{}
		for _, ev := range events {
			if ev.Type == poker.EventHoleCards {
				out[ev.Hand] = ev.Cards
			}
		}
		return out
	}
	aliceHole, bobHole := holeByHand(aliceSaw), holeByHand(bobSaw)
	if len(aliceHole) < 2 || len(bobHole) < 2 {
		t.Fatalf("两手牌该各发一次底牌，得到 %d / %d", len(aliceHole), len(bobHole))
	}

	check := func(viewer string, events []poker.Event, secret map[int][]poker.Card, owner string) {
		hand := 0
		shown := false
		for _, ev := range events {
			if ev.Hand > hand {
				hand, shown = ev.Hand, false
			}
			if ev.Type == poker.EventShowdown {
				shown = true // 摊牌之后底牌本就公开
				continue
			}
			if shown {
				continue
			}
			for _, c := range append(append([]poker.Card(nil), ev.Cards...), ev.Board...) {
				for _, s := range secret[hand] {
					if c == s {
						t.Fatalf("第 %d 手：%s 在事件 %s 里看到了 %s 的底牌 %s", hand, viewer, ev.Type, owner, c)
					}
				}
			}
			// 注意这里查的是 Player 而不是 To：To 带 json:"-"，根本不上线路，
			// 客户端压根看不到「这条是发给谁的」。也正因为如此，投递过滤只可能在
			// 服务端那一处生效——这条测试守的就是它真的生效了。
			if ev.Snapshot != nil && ev.Player != viewer {
				t.Fatalf("%s 收到了发给 %s 的快照", viewer, ev.Player)
			}
		}
	}
	check("alice", aliceSaw, bobHole, "bob")
	check("bob", bobSaw, aliceHole, "alice")
}

// TestDisconnectDoesNotStallTheTable：一个人拔网线，牌桌不能就此停住。
//
// 没有这条接管，掉线的玩家会让整张桌子永远停在他那一轮——而这在无人值守的
// agent 自对弈里是必然会发生的事。
func TestDisconnectDoesNotStallTheTable(t *testing.T) {
	s := startTable(t, 10*time.Millisecond)
	alice := dial(t, s, "alice")
	bob := dial(t, s, "bob")

	// alice 一个动作都不做，第一次轮到她的时候直接断开。
	// 等到「轮到她」再断，是为了确保服务端真的需要她那一下——
	// 她要是大盲而对手弃牌，这手牌压根不需要她说话，也就测不到接管。
	go func() {
		for {
			ev, _, err := alice.Next()
			if err != nil {
				return
			}
			if ev.Type == poker.EventYourTurn {
				_ = alice.Close()
				return
			}
		}
	}()

	// bob 照常打，读到「有人替 alice 做了动作」为止。
	got := make(chan []poker.Event, 1)
	go func() {
		var events []poker.Event
		for {
			ev, _, err := bob.Next()
			if err != nil {
				got <- events
				return
			}
			events = append(events, ev)
			if ev.Type == poker.EventYourTurn && ev.Snapshot != nil {
				if err := bob.Send(protocol.CommandOf(client.Decide(ev.Snapshot))); err != nil {
					got <- events
					return
				}
			}
			if ev.Type == poker.EventAction && ev.Player == "alice" && ev.Forced {
				got <- events
				return
			}
		}
	}()

	events := waitEvents(t, got)
	var forced *poker.Event
	for i := range events {
		if events[i].Type == poker.EventAction && events[i].Player == "alice" && events[i].Forced {
			forced = &events[i]
		}
	}
	if forced == nil {
		t.Fatal("alice 掉线之后没人接手，牌桌会永远停在她那一轮")
	}
	if forced.Action != "fold" && forced.Action != "check" {
		t.Fatalf("替人做的决定只能是过牌或弃牌，得到 %s", forced.Action)
	}
}

// TestIdleClientDoesNotStallTheTable：连着却不说话的客户端，不能把整张牌桌冻住。
//
// 这跟掉线是两回事：掉线还有连接关闭可以察觉，装死没有任何信号。
// 无人值守的自对弈里，一个卡住的 agent 会让所有人陪着它一起停住，
// 所以 ADR-0011 那条行动超时规则必须真的有人执行。
func TestIdleClientDoesNotStallTheTable(t *testing.T) {
	s := startTableWithTimeout(t, 10*time.Millisecond, 80*time.Millisecond)
	idle := dial(t, s, "idle")
	bob := dial(t, s, "bob")

	// idle 只读不说话，一个字节都不回。
	go func() {
		for {
			if _, _, err := idle.Next(); err != nil {
				return
			}
		}
	}()

	got := make(chan []poker.Event, 1)
	go func() {
		var events []poker.Event
		for {
			ev, _, err := bob.Next()
			if err != nil {
				got <- events
				return
			}
			events = append(events, ev)
			if ev.Type == poker.EventYourTurn && ev.Snapshot != nil {
				if err := bob.Send(protocol.CommandOf(client.Decide(ev.Snapshot))); err != nil {
					got <- events
					return
				}
			}
			if ev.Type == poker.EventAction && ev.Player == "idle" && ev.Forced {
				got <- events
				return
			}
		}
	}()

	events := waitEvents(t, got)
	var forced *poker.Event
	for i := range events {
		if events[i].Type == poker.EventAction && events[i].Player == "idle" && events[i].Forced {
			forced = &events[i]
		}
	}
	if forced == nil {
		t.Fatal("idle 一直不说话，牌桌就永远停在他那一轮了")
	}
	if forced.Action != "fold" && forced.Action != "check" {
		t.Fatalf("超时替他做的决定只能是过牌或弃牌，得到 %s", forced.Action)
	}
}

// TestReconnectReclaimsSeatAndStack：用同一个名字回来，认回原座位与筹码（ADR-0009）。
func TestReconnectReclaimsSeatAndStack(t *testing.T) {
	s := startTable(t, time.Hour) // 别开局，这个测试只关心座位
	alice := dial(t, s, "alice")
	if _, _, err := alice.Next(); err != nil {
		t.Fatalf("alice 没收到牌桌快照: %v", err)
	}
	_ = alice.Close()

	// 等服务端处理完断线。
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		again, err := client.Dial(s.Transport(), s.Code(), "alice", 0)
		if err != nil {
			t.Fatalf("alice 重连失败: %v", err)
		}
		ev, _, err := again.Next()
		if err != nil {
			_ = again.Close()
			continue
		}
		if ev.Type == poker.EventError {
			_ = again.Close()
			time.Sleep(20 * time.Millisecond)
			continue // 服务端还没处理完上一条连接的断开
		}
		defer again.Close()
		if ev.Type != poker.EventTable {
			t.Fatalf("重连该先收到牌桌快照，得到 %+v", ev)
		}
		if len(ev.Seats) != 1 {
			t.Fatalf("牌桌上该还是那一个座位，得到 %d 个", len(ev.Seats))
		}
		if ev.Seats[0].Stack != testBuyin {
			t.Fatalf("筹码该原样留着（%d），得到 %d", testBuyin, ev.Seats[0].Stack)
		}
		return
	}
	t.Fatal("等重连超时")
}

// TestSittingOutPlayerSeesOnlyPublicInfo：ADR-0006 说 Sitting Out 的玩家
// 能看到的信息严格等于一个弃牌后旁观的在座玩家。
//
// carol 带入一个大盲，而且轮到就推光，所以她很快会输光并自动进入 Sitting Out。
// 之后她该继续收到每一条广播，一条定向事件都不该再有——不多一格，也不少一格。
func TestSittingOutPlayerSeesOnlyPublicInfo(t *testing.T) {
	s := startTable(t, 10*time.Millisecond)
	// 三个带够筹码的人，是为了 carol 输光之后牌桌还开得下去——
	// 只有两个人的话，随便谁先破产都会让牌局停下，这条测试就会等在一个正确的状态上。
	alice := dial(t, s, "alice")
	keepPlaying(t, alice)
	bob := dial(t, s, "bob")
	keepPlaying(t, bob)
	dave := dial(t, s, "dave")
	keepPlaying(t, dave)

	carol, err := client.Dial(s.Transport(), s.Code(), "carol", 2)
	if err != nil {
		t.Fatalf("carol 加入失败: %v", err)
	}
	defer carol.Close()

	// 收到 carol 的 sit_out，再往后多看两手，确认她还看得见牌桌。
	out := make(chan []poker.Event, 1)
	go func() {
		var events []poker.Event
		satOut := false
		handsAfter := 0
		for {
			ev, _, err := carol.Next()
			if err != nil {
				out <- events
				return
			}
			events = append(events, ev)
			switch {
			case ev.Type == poker.EventYourTurn && ev.Snapshot != nil:
				// 推光。别的策略她可能靠弃牌苟很久，那这条测试就永远等不到 Sitting Out。
				_ = carol.Send(protocol.Command{Type: protocol.CmdAllIn})
			case ev.Type == poker.EventSitOut && ev.Player == "carol":
				satOut = true
			case ev.Type == poker.EventHandEnd && satOut:
				handsAfter++
				if handsAfter >= 1 {
					out <- events
					return
				}
			}
		}
	}()

	events := waitEvents(t, out)

	sitOutAt := -1
	for i, ev := range events {
		if ev.Type == poker.EventSitOut && ev.Player == "carol" {
			sitOutAt = i
			break
		}
	}
	if sitOutAt < 0 {
		t.Fatal("carol 输光之后该自动进入 Sitting Out")
	}

	after := events[sitOutAt:]
	for _, ev := range after {
		if ev.Type == poker.EventHoleCards || ev.Type == poker.EventYourTurn {
			t.Fatalf("暂离的 carol 还收到了 %s——她本该只看得到公开信息", ev.Type)
		}
		if ev.Snapshot != nil {
			t.Fatal("暂离的 carol 还收到了快照")
		}
	}
	// 但公开信息还得照收，否则她就成了瞎子而不是旁观者。
	if countOf(after, poker.EventHandStart) == 0 {
		t.Fatal("暂离的人应该还能看到牌桌上在发生什么")
	}
}

// TestButtonRotates：庄家位每手往左移一位（CONTEXT）。
func TestButtonRotates(t *testing.T) {
	s := startTable(t, 10*time.Millisecond)
	alice := dial(t, s, "alice")
	aliceEvents := autoPlay(t, alice, 3)
	bob := dial(t, s, "bob")
	autoPlay(t, bob, 3)

	events := waitEvents(t, aliceEvents)
	var buttons []string
	for _, ev := range events {
		if ev.Type == poker.EventHandStart {
			buttons = append(buttons, ev.Button)
		}
	}
	if len(buttons) < 3 {
		t.Fatalf("该看到三次开局，得到 %d 次", len(buttons))
	}
	if buttons[0] == buttons[1] {
		t.Fatalf("庄家位没动过：%v", buttons)
	}
	if buttons[0] != buttons[2] {
		t.Fatalf("两个人的桌子上庄家位该一手一换、第三手转回来，得到 %v", buttons)
	}
}

// TestLoneJoinerGetsNoHand：一个人的牌桌不开牌（ADR-0014 说的是「凑够两个」）。
func TestLoneJoinerGetsNoHand(t *testing.T) {
	s := startTable(t, 10*time.Millisecond)
	alice := dial(t, s, "alice")

	events := make(chan poker.Event, 8)
	go func() {
		for {
			ev, _, err := alice.Next()
			if err != nil {
				close(events)
				return
			}
			events <- ev
		}
	}()

	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("连接断了")
			}
			if ev.Type == poker.EventHandStart {
				t.Fatal("只有一个人也开牌了")
			}
		case <-deadline:
			return
		}
	}
}

// TestDuplicateNameIsRejected：名字即身份（ADR-0009），同一张桌上就不能重名，
// 而且要给一条带 code 的结构化错误（ADR-0011），别只丢一句人话。
func TestDuplicateNameIsRejected(t *testing.T) {
	s := startTable(t, time.Hour)
	alice := dial(t, s, "alice")
	if _, _, err := alice.Next(); err != nil {
		t.Fatalf("alice 没收到牌桌快照: %v", err)
	}

	impostor, err := client.Dial(s.Transport(), s.Code(), "alice", 0)
	if err != nil {
		t.Fatalf("第二个 alice 连接失败: %v", err)
	}
	defer impostor.Close()
	waitForError(t, impostor, "name_taken")
}

// waitForError 读到第一条错误事件为止，路过的普通事件（table、joined……）不算数。
func waitForError(t *testing.T, s *client.Session, code string) {
	t.Helper()
	got := make(chan poker.Event, 1)
	go func() {
		for {
			ev, _, err := s.Next()
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
			t.Fatalf("连接断了，没等到 %s 错误", code)
		}
		if ev.Code != code {
			t.Fatalf("想要 %s 错误，得到 %+v", code, ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("等 %s 错误超时", code)
	}
}

// TestBadTableCodeNeverTouchesTheFilesystem：Table Code 是要拼进路径的，
// 所以非法的码必须在拨号之前就被挡掉。
func TestBadTableCodeNeverTouchesTheFilesystem(t *testing.T) {
	s := startTable(t, time.Hour)
	for _, code := range []string{"../etc", "abc", "", "ABCDE1"} {
		if _, err := client.Dial(s.Transport(), code, "alice", 0); err == nil {
			t.Fatalf("%q 这种码不该连得上", code)
		}
	}
}

// TestUnknownCommandGetsStructuredError：坏掉的 agent 会发出奇怪的命令，
// 它得到的必须是一条能据以自动纠错的东西（ADR-0011），而不是静默或断线。
func TestUnknownCommandGetsStructuredError(t *testing.T) {
	s := startTable(t, time.Hour)
	alice := dial(t, s, "alice")
	if _, _, err := alice.Next(); err != nil {
		t.Fatalf("alice 没收到牌桌快照: %v", err)
	}
	if err := alice.Send(protocol.Command{Type: "teleport"}); err != nil {
		t.Fatalf("发命令失败: %v", err)
	}
	waitForError(t, alice, "unknown_command")
}

// TestActionWithoutHandIsRejected：没在打牌的时候乱发动作，要拿到一条结构化错误。
func TestActionWithoutHandIsRejected(t *testing.T) {
	s := startTable(t, time.Hour)
	alice := dial(t, s, "alice")
	if _, _, err := alice.Next(); err != nil {
		t.Fatalf("alice 没收到牌桌快照: %v", err)
	}
	if err := alice.Send(protocol.Command{Type: protocol.CmdCall}); err != nil {
		t.Fatalf("发命令失败: %v", err)
	}
	waitForError(t, alice, "no_hand")
}

// TestTinyBuyinIsRejected：带入连一个大盲都不够的话，当场说清楚，别让他坐下再发现打不了。
func TestTinyBuyinIsRejected(t *testing.T) {
	s := startTable(t, time.Hour)
	sess, err := client.Dial(s.Transport(), s.Code(), "alice", 1)
	if err != nil {
		t.Fatalf("连接失败: %v", err)
	}
	defer sess.Close()
	waitForError(t, sess, "buyin_too_small")
}
