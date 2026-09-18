package server

import (
	"io"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/xujnan/poker-cli/internal/client"
	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/protocol"
)

// 这个文件测的是真的跑起来的那条路：真的 Unix socket、真的两个客户端、真的事件流。
// 纯核心里的可见性测试守的是事件该长什么样，这里守的是投递关口有没有照着做。

func startTable(t *testing.T, handDelay time.Duration) *Server {
	t.Helper()
	s, err := New(Options{
		Dir:       t.TempDir(),
		Rand:      rand.New(rand.NewPCG(20240918, 5)),
		HandDelay: handDelay,
		Log:       io.Discard,
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

// collect 在后台读一个客户端的事件流，读到 hand_end 或连接断开为止。
func collect(t *testing.T, s *client.Session) <-chan []poker.Event {
	t.Helper()
	out := make(chan []poker.Event, 1)
	go func() {
		var events []poker.Event
		for {
			ev, _, err := s.Next()
			if err != nil {
				out <- events
				return
			}
			events = append(events, ev)
			if ev.Type == poker.EventHandEnd {
				out <- events
				return
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
	case <-time.After(5 * time.Second):
		t.Fatal("等事件超时")
		return nil
	}
}

// TestTwoPlayersPlayAHand 是第一刀的贯通测试：serve → join → 事件流 → 摊牌。
func TestTwoPlayersPlayAHand(t *testing.T) {
	s := startTable(t, 10*time.Millisecond)

	alice, err := client.Dial(s.SocketDir(), s.Code(), "alice")
	if err != nil {
		t.Fatalf("alice 加入失败: %v", err)
	}
	defer alice.Close()
	aliceEvents := collect(t, alice)

	bob, err := client.Dial(s.SocketDir(), s.Code(), "bob")
	if err != nil {
		t.Fatalf("bob 加入失败: %v", err)
	}
	defer bob.Close()
	bobEvents := collect(t, bob)

	got := map[string][]poker.Event{
		"alice": waitEvents(t, aliceEvents),
		"bob":   waitEvents(t, bobEvents),
	}

	for name, events := range got {
		var sawStart, sawShowdown, sawEnd bool
		var holeCount int
		for _, ev := range events {
			switch ev.Type {
			case poker.EventHandStart:
				sawStart = true
			case poker.EventHoleCards:
				holeCount++
				if ev.Player != name {
					t.Fatalf("%s 收到了 %s 的底牌事件", name, ev.Player)
				}
			case poker.EventShowdown:
				sawShowdown = true
			case poker.EventHandEnd:
				sawEnd = true
				if len(ev.Winners) == 0 {
					t.Fatalf("%s 看到的 hand_end 没有赢家", name)
				}
				if ev.Pot == nil || *ev.Pot != 0 {
					t.Fatalf("%s 看到的底池 = %v，第一刀应该恒为 0", name, ev.Pot)
				}
			case poker.EventError:
				t.Fatalf("%s 收到错误事件：%s %s", name, ev.Code, ev.Message)
			}
		}
		if !sawStart || !sawShowdown || !sawEnd {
			t.Fatalf("%s 的事件流不完整：%+v", name, events)
		}
		if holeCount != 1 {
			t.Fatalf("%s 收到了 %d 条底牌事件，应该正好 1 条", name, holeCount)
		}
	}

	// 两边看到的赢家必须一致——服务端是唯一权威（ADR-0003），两个客户端不该各有各的结论。
	if a, b := lastEnd(got["alice"]), lastEnd(got["bob"]); a != b {
		t.Fatalf("两边看到的赢家不同：alice 看到 %q，bob 看到 %q", a, b)
	}
}

func lastEnd(events []poker.Event) string {
	for _, ev := range events {
		if ev.Type == poker.EventHandEnd && len(ev.Winners) > 0 {
			return ev.Winners[0]
		}
	}
	return ""
}

// TestSocketNeverLeaksOtherHoleCards 是 ADR-0006 在真实投递路径上的那一道：
// 摊牌之前，从 socket 上流过来的字节里不能出现对手的底牌。
func TestSocketNeverLeaksOtherHoleCards(t *testing.T) {
	s := startTable(t, 10*time.Millisecond)

	alice, err := client.Dial(s.SocketDir(), s.Code(), "alice")
	if err != nil {
		t.Fatalf("alice 加入失败: %v", err)
	}
	defer alice.Close()
	aliceEvents := collect(t, alice)

	bob, err := client.Dial(s.SocketDir(), s.Code(), "bob")
	if err != nil {
		t.Fatalf("bob 加入失败: %v", err)
	}
	defer bob.Close()
	bobEvents := collect(t, bob)

	aliceSaw := waitEvents(t, aliceEvents)
	bobSaw := waitEvents(t, bobEvents)

	hole := func(events []poker.Event) []poker.Card {
		for _, ev := range events {
			if ev.Type == poker.EventHoleCards {
				return ev.Cards
			}
		}
		t.Fatal("没收到自己的底牌")
		return nil
	}
	bobHole := hole(bobSaw)
	aliceHole := hole(aliceSaw)

	check := func(viewer string, events []poker.Event, secret []poker.Card, owner string) {
		for _, ev := range events {
			if ev.Type == poker.EventShowdown {
				return // 摊牌之后底牌本就公开
			}
			if ev.Type == poker.EventHoleCards && ev.Player == viewer {
				continue
			}
			for _, c := range ev.Cards {
				for _, s := range secret {
					if c == s {
						t.Fatalf("%s 在事件 %s 里看到了 %s 的底牌 %s", viewer, ev.Type, owner, c)
					}
				}
			}
		}
	}
	check("alice", aliceSaw, bobHole, "bob")
	check("bob", bobSaw, aliceHole, "alice")
}

// TestLoneJoinerGetsNoHand：一个人的牌桌不开牌（ADR-0014 说的是「凑够两个」）。
func TestLoneJoinerGetsNoHand(t *testing.T) {
	s := startTable(t, 10*time.Millisecond)

	alice, err := client.Dial(s.SocketDir(), s.Code(), "alice")
	if err != nil {
		t.Fatalf("alice 加入失败: %v", err)
	}
	defer alice.Close()

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
	s := startTable(t, time.Hour) // 别让它开局，这个测试只关心加入

	alice, err := client.Dial(s.SocketDir(), s.Code(), "alice")
	if err != nil {
		t.Fatalf("alice 加入失败: %v", err)
	}
	defer alice.Close()
	if _, _, err := alice.Next(); err != nil { // 等 table 快照，确认她真的坐下了
		t.Fatalf("alice 没收到牌桌快照: %v", err)
	}

	impostor, err := client.Dial(s.SocketDir(), s.Code(), "alice")
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
		if _, err := client.Dial(s.SocketDir(), code, "alice"); err == nil {
			t.Fatalf("%q 这种码不该连得上", code)
		}
	}
}

// TestUnknownCommandGetsStructuredError：坏掉的 agent 会发出奇怪的命令，
// 它得到的必须是一条能据以自动纠错的东西（ADR-0011），而不是静默或断线。
func TestUnknownCommandGetsStructuredError(t *testing.T) {
	s := startTable(t, time.Hour)

	alice, err := client.Dial(s.SocketDir(), s.Code(), "alice")
	if err != nil {
		t.Fatalf("alice 加入失败: %v", err)
	}
	defer alice.Close()
	if _, _, err := alice.Next(); err != nil {
		t.Fatalf("alice 没收到牌桌快照: %v", err)
	}

	if err := alice.Send(protocol.Command{Type: "teleport"}); err != nil {
		t.Fatalf("发命令失败: %v", err)
	}
	waitForError(t, alice, "unknown_command")
}
