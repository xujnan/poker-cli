package history

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// TestFormatShowsWhatHappened：复盘里该有的东西都得有。
//
// 不跟一份「标准输出」逐字比——那种测试改一个空格就红，红了也说明不了什么。
// 这里挨个问「这件事在不在里面」。
func TestFormatShowsWhatHappened(t *testing.T) {
	rec := playHand(t, 4242, []int{200, 150, 80}, 0, rand.New(rand.NewPCG(4242, 1)))
	out := Format(rec)

	must := []string{
		"第 1 手", rec.Table, rec.Blinds, rec.Button,
	}
	for _, s := range rec.Seats {
		must = append(must, s.Player, cards(rec.Hole[s.Player]))
	}
	for _, w := range rec.Winners {
		must = append(must, w)
	}
	for _, want := range must {
		if !strings.Contains(out, want) {
			t.Fatalf("复盘里没有 %q：\n%s", want, out)
		}
	}
	// 每个动作都得露面。
	for _, a := range rec.Actions {
		if !strings.Contains(out, a.Player) {
			t.Fatalf("复盘里没有 %s 的动作：\n%s", a.Player, out)
		}
	}
	// 每个人的净变化都得标出来，复盘的人最先看的就是这个。
	for _, s := range rec.Seats {
		if !strings.Contains(out, "(+") && !strings.Contains(out, "(-") && !strings.Contains(out, "(+0") {
			t.Fatalf("复盘里没有 %s 的净变化：\n%s", s.Player, out)
		}
	}
}

// TestFormatShowsEveryStreetThatWasDealt：发到哪条街，复盘就该显示到哪条街。
func TestFormatShowsEveryStreetThatWasDealt(t *testing.T) {
	// 让两个人一路跟到河牌。
	rec := playHand(t, 9, []int{200, 200}, 0, rand.New(rand.NewPCG(9, 9)))
	for len(rec.Community) < 5 {
		// 换个种子直到抓到一手打到河牌的。
		rec = playHand(t, uint64(len(rec.Community))+100, []int{200, 200}, 0, rand.New(rand.NewPCG(77, 3)))
		break
	}
	out := Format(rec)
	if !strings.Contains(out, "翻牌前") {
		t.Fatalf("复盘里没有翻牌前：\n%s", out)
	}
	if len(rec.Community) >= 3 && !strings.Contains(out, "翻牌") {
		t.Fatalf("发了翻牌却没显示：\n%s", out)
	}
	if len(rec.Community) == 5 && !strings.Contains(out, "河牌") {
		t.Fatalf("发到河牌却没显示：\n%s", out)
	}
	// 没发出来的街不该凭空出现。
	if len(rec.Community) == 0 && strings.Contains(out, "翻牌 ") {
		t.Fatalf("没发翻牌却显示了：\n%s", out)
	}
}

// TestSummaryCountsNetChips：战绩表按每手的筹码变化累加。
func TestSummaryCountsNetChips(t *testing.T) {
	s := NewSummary()
	s.Add(Record{
		Seats:  []Seat{{Player: "alice", Stack: 100}, {Player: "bob", Stack: 100}},
		Stacks: map[string]int{"alice": 130, "bob": 70},
		Payout: map[string]int{"alice": 60},
	})
	s.Add(Record{
		Seats:  []Seat{{Player: "alice", Stack: 130}, {Player: "bob", Stack: 70}},
		Stacks: map[string]int{"alice": 120, "bob": 80},
		Payout: map[string]int{"bob": 20},
	})

	if s.Hands != 2 {
		t.Fatalf("该是 2 手，得到 %d", s.Hands)
	}
	got := map[string]PlayerStats{}
	for _, p := range s.Players() {
		got[p.Player] = p
	}
	if got["alice"].Net != 20 {
		t.Fatalf("alice 净筹码该是 +20，得到 %+d", got["alice"].Net)
	}
	if got["bob"].Net != -20 {
		t.Fatalf("bob 净筹码该是 -20，得到 %+d", got["bob"].Net)
	}
	if got["alice"].Won != 1 || got["bob"].Won != 1 {
		t.Fatalf("两人该各赢一手，得到 alice %d / bob %d", got["alice"].Won, got["bob"].Won)
	}
	// 按净筹码从多到少排。
	if s.Players()[0].Player != "alice" {
		t.Fatalf("赢得最多的该排第一，得到 %v", s.Players())
	}
}

// TestSummaryDoesNotCountTopUpsAsProfit：补码不是赢来的钱。
//
// 净筹码按「每手结束减每手开局」累加，补码发生在两手牌之间，不在任何一手的账里。
// 要是改成「最后筹码减最初带入」，一个输光十次又补十次的人会显示成小赚。
func TestSummaryDoesNotCountTopUpsAsProfit(t *testing.T) {
	s := NewSummary()
	// 第一手：alice 带 100 输光。
	s.Add(Record{
		Seats:  []Seat{{Player: "alice", Stack: 100}, {Player: "bob", Stack: 100}},
		Stacks: map[string]int{"alice": 0, "bob": 200},
	})
	// 两手牌之间她补了 100 回来，然后第二手打平。
	s.Add(Record{
		Seats:  []Seat{{Player: "alice", Stack: 100}, {Player: "bob", Stack: 200}},
		Stacks: map[string]int{"alice": 100, "bob": 200},
	})

	for _, p := range s.Players() {
		if p.Player == "alice" && p.Net != -100 {
			t.Fatalf("alice 输了 100 又补了 100，净筹码该是 -100，得到 %+d", p.Net)
		}
	}
}

// TestVerifyErrorsReadLikeTheScreen：校验失败的消息是给人读的，牌按终端的样子印。
//
// 它对照的确实是文件内容（文件里是 "As"），但排查时是按手号和种子去定位那一手，
// 不会拿牌去 grep 文件——所以这里跟着屏幕走，而不是跟着文件走。
func TestVerifyErrorsReadLikeTheScreen(t *testing.T) {
	rec := playHand(t, 606, []int{200, 200}, 0, rand.New(rand.NewPCG(606, 1)))
	rec.Seed++ // 种子一改，重放出来的底牌就对不上了

	err := Verify(rec)
	if err == nil {
		t.Fatal("改了种子该报错")
	}
	if !strings.ContainsAny(err.Error(), "♠♥♦♣") {
		t.Fatalf("报错里该用花色符号：%v", err)
	}
}
