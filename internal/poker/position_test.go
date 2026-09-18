package poker

import (
	"strings"
	"testing"
)

// TestPositionsFollowTheConvention 把整张惯例表钉死。
//
// 位置命名是约定俗成的，各家还略有出入——把每一档人数的完整结果写出来，
// 是为了让「我们选的是哪一套」有个能指着看的地方，改动时也一眼看得出动了什么。
func TestPositionsFollowTheConvention(t *testing.T) {
	// button 固定在 0 号座位，从它开始顺时针读。
	want := map[int][]string{
		2: {"BTN/SB", "BB"},
		3: {"BTN", "SB", "BB"},
		4: {"BTN", "SB", "BB", "UTG"},
		5: {"BTN", "SB", "BB", "UTG", "CO"},
		6: {"BTN", "SB", "BB", "UTG", "HJ", "CO"},
		7: {"BTN", "SB", "BB", "UTG", "LJ", "HJ", "CO"},
		8: {"BTN", "SB", "BB", "UTG", "UTG+1", "LJ", "HJ", "CO"},
		9: {"BTN", "SB", "BB", "UTG", "UTG+1", "UTG+2", "LJ", "HJ", "CO"},
	}
	for n, expect := range want {
		got := Positions(n, 0)
		if strings.Join(got, " ") != strings.Join(expect, " ") {
			t.Fatalf("%d 人桌：\n  得到 %v\n  想要 %v", n, got, expect)
		}
	}
}

// TestPositionsRotateWithTheButton：换个庄家位，每个人的位置跟着转。
func TestPositionsRotateWithTheButton(t *testing.T) {
	const n = 6
	for button := 0; button < n; button++ {
		got := Positions(n, button)
		if got[button] != "BTN" {
			t.Fatalf("button=%d 时 %d 号座位该是 BTN，得到 %s", button, button, got[button])
		}
		if got[(button+1)%n] != "SB" {
			t.Fatalf("button=%d 时 button 左手该是 SB，得到 %s", button, got[(button+1)%n])
		}
		if got[(button+2)%n] != "BB" {
			t.Fatalf("button=%d 时该是 BB，得到 %s", button, got[(button+2)%n])
		}
		// 每个位置名只出现一次。
		seen := map[string]bool{}
		for _, p := range got {
			if seen[p] {
				t.Fatalf("button=%d 时位置 %s 出现了两次：%v", button, p, got)
			}
			seen[p] = true
		}
	}
}

// TestPositionsMatchWhoActsFirst：UTG 这个名字说的就是「第一个说话」，得跟状态机对得上。
//
// 位置表和真正的行动顺序要是各算各的，那这个标注就是在骗人。
func TestPositionsMatchWhoActsFirst(t *testing.T) {
	for n := 2; n <= MaxSeats; n++ {
		for button := 0; button < n; button++ {
			seats := make([]Seat, n)
			for i := range seats {
				seats[i] = Seat{Player: string(rune('a' + i)), Stack: 200}
			}
			h, _ := dealFor(t, seats, button)
			pos := Positions(n, button)

			first := h.Turn()
			// 谁第一个说话取决于人数：
			//   2 人 button 自己就是小盲，他先说话
			//   3 人 大盲左手正好绕回 button，所以也是 button 先说话——三人桌没有 UTG 这个位置
			//   4 人及以上 才有 UTG，而 UTG 的字面意思就是第一个说话的人
			want := "UTG"
			switch n {
			case 2:
				want = "BTN/SB"
			case 3:
				want = "BTN"
			}
			for i, s := range seats {
				if s.Player == first && pos[i] != want {
					t.Fatalf("%d 人桌 button=%d：第一个说话的是 %s，位置标的却是 %s（该是 %s）",
						n, button, first, pos[i], want)
				}
			}
		}
	}
}

// TestPositionsAppearInSeatViews：位置要跟着事件一起发出去，agent 不该自己去推。
func TestPositionsAppearInSeatViews(t *testing.T) {
	seats := []Seat{{"a", 200}, {"b", 200}, {"c", 200}, {"d", 200}, {"e", 200}, {"f", 200}}
	_, events := dealFor(t, seats, 2)

	var start *Event
	for i := range events {
		if events[i].Type == EventHandStart {
			start = &events[i]
		}
	}
	if start == nil {
		t.Fatal("没有开局事件")
	}
	want := Positions(len(seats), 2)
	for i, sv := range start.Seats {
		if sv.Position != want[i] {
			t.Fatalf("%s 的位置是 %q，该是 %q", sv.Player, sv.Position, want[i])
		}
	}
}

func dealFor(t *testing.T, seats []Seat, button int) (*Hand, []Event) {
	t.Helper()
	d := NewDeck(RandFor(uint64(len(seats)*100 + button)))
	return NewHand(1, seats, button, Blinds{1, 2}, d)
}
