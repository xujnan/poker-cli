package client

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/creack/pty"

	"github.com/xujnan/poker-cli/internal/poker"
)

func ptr(n int) *int { return &n }

// feed 把一串事件喂进视图，只攒状态不画，方便断言 frame 的内容。
func feed(v *liveView, evs ...poker.Event) {
	for _, ev := range evs {
		v.apply(ev)
	}
}

func handOne() []poker.Event {
	return []poker.Event{
		{Type: poker.EventHandStart, Hand: 1, Button: "bot1", Blinds: "1/2", Seats: []poker.SeatView{
			{Player: "我", Stack: 200, Position: "BTN"},
			{Player: "bot1", Stack: 200, Position: "SB"},
			{Player: "bot2", Stack: 200, Position: "BB"},
		}},
		{Type: poker.EventBlind, Player: "bot1", Action: "small_blind", Amount: 1},
		{Type: poker.EventBlind, Player: "bot2", Action: "big_blind", Amount: 2},
		{Type: poker.EventHoleCards, Player: "我", Cards: poker.MustParseCards("As Kd")},
		{Type: poker.EventAction, Player: "我", Action: "call", Amount: 2, Committed: 2, Stack: ptr(198), Pot: ptr(5)},
		{Type: poker.EventStreet, Street: "flop", Cards: poker.MustParseCards("2c 7h 9s"),
			Board: poker.MustParseCards("2c 7h 9s"), Pot: ptr(6)},
	}
}

// TestLiveViewShowsOnlyTheHandInProgress 是这一版视图存在的全部理由。
//
// 新的一手开始时，上一手的公共牌、流水、底池必须干净地消失——否则「只展示正在玩的」
// 就退化成了「把流水账重画一遍」，屏幕上还是越堆越多。
func TestLiveViewShowsOnlyTheHandInProgress(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, handOne()...)

	if got := v.frame(); !strings.Contains(got, "2♣") {
		t.Fatalf("第 1 手打到翻牌，帧里该有翻牌:\n%s", got)
	}

	feed(v, poker.Event{Type: poker.EventHandEnd, Hand: 1, Pot: ptr(6)})
	feed(v, poker.Event{Type: poker.EventHandStart, Hand: 2, Button: "bot2", Blinds: "1/2", Seats: []poker.SeatView{
		{Player: "我", Stack: 198, Position: "SB"},
	}})

	got := v.frame()
	for _, stale := range []string{"2♣", "7♥", "9♠", "A♠", "K♦", "跟注", "小盲", "底池"} {
		if strings.Contains(got, stale) {
			t.Fatalf("第 2 手的帧里还留着上一手的 %q:\n%s", stale, got)
		}
	}
	if !strings.Contains(got, "第 2 手") {
		t.Fatalf("帧里该写着第 2 手:\n%s", got)
	}
}

// TestLiveFrameCarriesWhatYouNeedToAct：轮到你的时候，决定要做什么所需的东西
// 必须全在这一帧里——不能要求人往上翻屏。
func TestLiveFrameCarriesWhatYouNeedToAct(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, handOne()...)
	feed(v, poker.Event{Type: poker.EventYourTurn, Player: "我", Snapshot: &poker.Snapshot{
		Hand: 1, Street: "flop",
		Hole:      poker.MustParseCards("As Kd"),
		Community: poker.MustParseCards("2c 7h 9s"),
		Pot:       10, ToCall: 4, Stack: 194,
		Seats: []poker.SeatView{
			{Player: "我", Stack: 194, Position: "BTN", Committed: 2},
			{Player: "bot2", Stack: 190, Position: "BB", Committed: 6},
		},
		Legal: []poker.LegalAction{
			{Action: "fold"}, {Action: "call", Amount: 4},
			{Action: "bet", Min: 10, Max: 194}, {Action: "allin", Amount: 194},
		},
	}})

	got := v.frame()
	for _, want := range []string{
		"A♠ K♦",        // 底牌
		"2♣ 7♥ 9♠",     // 公共牌
		"底池 10",        // 底池
		"要跟 4",         // 要跟多少
		"call（跟 4）",    // 合法动作，带算好的额度
		"bet <10-194>", // 加注区间
		"BTN",          // 位置
		"▶ ",           // 该我了的标记
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("帧里缺了 %q:\n%s", want, got)
		}
	}
	// 最后一行是输入提示，光标停在它后面。
	if !strings.HasSuffix(got, "> ") {
		t.Fatalf("帧该以输入提示收尾:\n%q", got)
	}
}

// TestLiveNeverShowsOthersHoleCards：重画这条路也在 ADR-0006 的管辖之内。
//
// 视图是攒状态的，这正是可见性最容易破的地方——只要有一处把别人的牌记进了状态，
// 它就会一直画在屏幕上。所以这里从事件层面走一遍：没投递给我的底牌，帧里不该有。
func TestLiveNeverShowsOthersHoleCards(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, handOne()...)
	// 服务端绝不会把别人的 hole_cards 投给我（server_test 守着那一条），
	// 这里退一万步：就算收到了，视图也只认自己那一份。
	feed(v, poker.Event{Type: poker.EventHoleCards, Player: "bot1", To: "bot1",
		Cards: poker.MustParseCards("Qh Qc")})

	got := v.frame()
	for _, his := range []string{"Q♥", "Q♣"} {
		if strings.Contains(got, his) {
			t.Fatalf("别人的底牌 %s 画到屏幕上了：\n%s", his, got)
		}
	}
	// 自己那一份不能被冲掉。
	if !strings.Contains(got, "A♠ K♦") {
		t.Fatalf("自己的底牌没了：\n%s", got)
	}
}

// TestLiveRedrawRewindsExactlyOneFrame：重画前上移的行数必须正好等于上一帧的行数。
//
// 少一行，屏幕上就会每帧留下一条残渣，越积越多；多一行，就会把上面的历史吃掉。
// 这个数没法靠肉眼校对，只能算。
func TestLiveRedrawRewindsExactlyOneFrame(t *testing.T) {
	var out strings.Builder
	v := newLiveView(&out, "我")
	v.render()
	first := v.frame()

	if strings.Contains(out.String(), "\033[") && strings.Contains(out.String(), "A") &&
		strings.HasPrefix(out.String(), "\033[") {
		t.Fatalf("第一帧不该上移光标，上面还没有东西:\n%q", out.String())
	}

	out.Reset()
	feed(v, handOne()...)
	v.render()
	want := fmt.Sprintf("\033[%dA", strings.Count(first, "\n"))
	if !strings.HasPrefix(out.String(), want) {
		t.Fatalf("该上移 %d 行（上一帧的行数），得到:\n%q", strings.Count(first, "\n"), out.String())
	}
	// 清屏那一下也得在：不清的话，新帧比旧帧短的时候底下会留着旧内容。
	if !strings.Contains(out.String(), "\r\033[J") {
		t.Fatalf("重画前该清掉旧内容:\n%q", out.String())
	}
}

// TestLiveRedrawCountsTheEchoedInputLine：用户敲的那一行也占一行。
//
// 终端把 "call\n" 回显出来之后，光标已经比上一帧多下去一行了。不把这一笔记上，
// 下一帧就会往回少挪一行，于是每敲一条命令屏幕上就多一条残渣。
func TestLiveRedrawCountsTheEchoedInputLine(t *testing.T) {
	var out strings.Builder
	v := newLiveView(&out, "我")
	v.render()
	lines := strings.Count(v.frame(), "\n")

	v.typed()
	out.Reset()
	v.render()
	want := fmt.Sprintf("\033[%dA", lines+1)
	if !strings.HasPrefix(out.String(), want) {
		t.Fatalf("敲过一行之后该上移 %d 行，得到:\n%q", lines+1, out.String())
	}
}

// TestLiveDisconnectDropsThePrompt：断开之后那个 "> " 不能还在。
//
// 留着的话，屏幕看起来还能敲命令，其实什么都发不出去了。
func TestLiveDisconnectDropsThePrompt(t *testing.T) {
	var out strings.Builder
	v := newLiveView(&out, "我")
	feed(v, handOne()...)
	v.disconnected()
	if strings.HasSuffix(strings.TrimRight(out.String(), "\n"), "> ") {
		t.Fatalf("断开之后不该再画输入提示:\n%q", out.String())
	}
	if !strings.Contains(out.String(), "连接已断开") {
		t.Fatalf("该说一声断开了:\n%q", out.String())
	}
}

// TestLiveNoticeLastsUntilTheNextEvent：提醒显示到下一条事件为止。
//
// 「动作非法」那条错误必须挂到你真的做出一个合法动作——而轮到你的时候不会有任何
// 事件进来，所以这条规则天然给足了时间。
func TestLiveNoticeLastsUntilTheNextEvent(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, handOne()...)
	v.notice("bet 要跟一个正数额")
	if !strings.Contains(v.frame(), "bet 要跟一个正数额") {
		t.Fatal("提醒该显示出来")
	}
	v.refresh()
	if !strings.Contains(v.frame(), "bet 要跟一个正数额") {
		t.Fatal("只是重画一下，提醒不该消失")
	}
	feed(v, poker.Event{Type: poker.EventAction, Player: "bot1", Action: "fold"})
	if strings.Contains(v.frame(), "bet 要跟一个正数额") {
		t.Fatal("来了新事件，提醒该让位了")
	}
}

// TestLiveLogIsCappedInMemory：攒着的流水有个上限，一手长牌不会越攒越多。
func TestLiveLogIsCappedInMemory(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, poker.Event{Type: poker.EventHandStart, Hand: 1})
	for i := 0; i < logCap*3; i++ {
		v.addLog("第 %d 条", i)
	}
	if len(v.log) != logCap {
		t.Fatalf("流水该被截到 %d 条，得到 %d 条", logCap, len(v.log))
	}
}

// TestLiveFrameFitsTheTerminal：一帧必须塞得进一屏，屏幕多高就画多高。
//
// 塞不进时终端会滚动，而滚动之后「上移 N 行」回到的就不是原来那个位置，
// 屏幕会一路烂下去。这一条得对着真 pty 跑：高度是问出来的，不是常数。
func TestLiveFrameFitsTheTerminal(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("开不了 pty: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	// pty 里没人读的话写会堵住，开一个只管丢的读者。
	go io.Copy(io.Discard, ptmx)

	v := newLiveView(tty, "我")
	feed(v, handOne()...)
	for i := 0; i < 60; i++ {
		v.addLog("第 %d 条流水", i)
	}

	for _, rows := range []int{40, 24, 12, 8} {
		if err := pty.Setsize(tty, &pty.Winsize{Rows: uint16(rows), Cols: 100}); err != nil {
			t.Fatal(err)
		}
		lines := strings.Count(v.frame(), "\n") + 1 // 最后一行提示符没有换行
		if lines > rows {
			t.Fatalf("终端 %d 行，画了 %d 行：\n%s", rows, lines, v.frame())
		}
		// 最新的那条流水永远得在——截的是旧的那头。
		if !strings.Contains(v.frame(), "第 59 条流水") {
			t.Fatalf("终端 %d 行时把最新的一条截掉了：\n%s", rows, v.frame())
		}
	}
}

// TestLiveKeepsSomeLogEvenOnATinyTerminal：终端矮到离谱时，宁可让它滚一下，
// 也不能把「刚才发生了什么」删干净——那时屏幕上只剩一个静止的局面。
func TestLiveKeepsSomeLogEvenOnATinyTerminal(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("开不了 pty: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	go io.Copy(io.Discard, ptmx)

	if err := pty.Setsize(tty, &pty.Winsize{Rows: 3, Cols: 100}); err != nil {
		t.Fatal(err)
	}
	v := newLiveView(tty, "我")
	feed(v, handOne()...)
	if got := strings.Count(v.frame(), "条流水"); got != 0 {
		t.Fatalf("这一手还没有流水才对")
	}
	v.addLog("最后一条")
	if !strings.Contains(v.frame(), "最后一条") {
		t.Fatalf("三行高的终端也得留下点流水：\n%s", v.frame())
	}
}

// TestLiveFallsBackWhenHeightIsUnknown：问不出高度（对面不是终端）时用保守的默认值。
func TestLiveFallsBackWhenHeightIsUnknown(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, poker.Event{Type: poker.EventHandStart, Hand: 1})
	for i := 0; i < 30; i++ {
		v.addLog("第 %d 条", i)
	}
	if got := strings.Count(v.frame(), " 条"); got != fallbackLogLines {
		t.Fatalf("该退回 %d 条，画了 %d 条", fallbackLogLines, got)
	}
}
