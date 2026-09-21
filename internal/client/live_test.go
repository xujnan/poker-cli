package client

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/textui"
)

func ptr(n int) *int { return &n }

// seatRows 从一帧里取出座位区那几行。
//
// 按结构取，不靠字符串长相猜：座位区是标题底下那一块，到空行或公共牌那行为止。
// 猜过三次都猜错了——流水行长得跟座位行很像（都以两格缩进加人名开头，
// 现在名字后面还跟着括号里的位置），而它们说的是两件完全不同的事。
func seatRows(frame string) []string {
	lines := strings.Split(frame, "\n")
	var out []string
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "公共牌") {
			if len(out) > 0 {
				break
			}
			continue
		}
		out = append(out, l)
	}
	return out
}

// currentBlock 从一帧里取出「正在打的这一手」那部分，把上一手那一小块甩掉。
//
// 两块长得像但说的是两回事：上面那块是已经打完的，留着就是给人回头看的；
// 下面这块是正在动的，它里面混进上一手的任何东西都是 bug。断言必须分开。
func currentBlock(frame string) string {
	lines := strings.Split(frame, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "第 ") {
			return strings.Join(lines[i:], "\n")
		}
	}
	return frame
}

// feedRender 走真正那条路：并进状态之后重画一帧。
//
// feed 只攒状态不画，断言帧内容时够用；但凡断言跟重画有关的东西（上移几行、
// 有没有把记录留在终端上），就必须走这条。
func feedRender(v *liveView, evs ...poker.Event) {
	for _, ev := range evs {
		_ = v.event(ev, nil)
	}
}

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

// myTurn 是「轮到我了」那条事件：快照带着整桌，因为视图收到它时会整表覆盖。
func myTurn() poker.Event {
	return poker.Event{Type: poker.EventYourTurn, Snapshot: &poker.Snapshot{
		Street: "flop", Pot: 6, Community: poker.MustParseCards("2c 7h 9s"),
		Hole: poker.MustParseCards("As Kd"),
		Seats: []poker.SeatView{
			{Player: "我", Stack: 198, Position: "BTN"},
			{Player: "bot1", Stack: 199, Position: "SB"},
			{Player: "bot2", Stack: 198, Position: "BB"},
		},
	}}
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

	// 正在打的那一块必须干净。上一手的结尾另有一块专门留着（见
	// TestPreviousHandStaysOnScreen），那块里有这些是应该的。
	got := currentBlock(v.frame())
	for _, stale := range []string{"2♣", "7♥", "9♠", "A♠", "K♦", "跟注", "底池"} {
		if strings.Contains(got, stale) {
			t.Fatalf("第 2 手这一块里还留着上一手的 %q:\n%s", stale, got)
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

// TestStreetRuleSpansTheLine：街分隔线要铺成一整行，不是前面点两个横杠。
//
// 一条街打完进下一条，是这手牌里最需要一眼看见的断点：上一条街的下注全部结清，
// 牌面变了，重新开始说话。它要跟上下那些动作行明显不是一类东西。
func TestStreetRuleSpansTheLine(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, handOne()...)

	var rule string
	for _, l := range v.log {
		if strings.Contains(l, "翻牌") {
			rule = l
		}
	}
	if rule == "" {
		t.Fatalf("流水里没有翻牌那一行：%v", v.log)
	}
	if !strings.HasPrefix(rule, "────") || !strings.HasSuffix(rule, "─") {
		t.Fatalf("分隔线该两头都是横线：%q", rule)
	}
	// 街名和新翻开的牌都得留在线上——横线好看，但不能把信息挤掉。
	for _, want := range []string{"翻牌", "2♣ 7♥ 9♠"} {
		if !strings.Contains(rule, want) {
			t.Fatalf("分隔线里缺了 %q：%q", want, rule)
		}
	}
	if got := textui.Width(rule); got != streetRuleWidth {
		t.Fatalf("分隔线该占 %d 列，得到 %d（%q）", streetRuleWidth, got, rule)
	}
}

// TestStreetRuleShrinksWithTheTerminal：窄终端上分隔线要跟着缩。
//
// 画过头了终端会折行，而折了一行，就地重画时「上移 N 行」就再也对不上了——
// 一条为了好看画出去的横线，能把整块屏幕毁掉。
func TestStreetRuleShrinksWithTheTerminal(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("开不了 pty: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	go io.Copy(io.Discard, ptmx)

	for _, cols := range []int{100, 46, 30, 20} {
		if err := pty.Setsize(tty, &pty.Winsize{Rows: 40, Cols: uint16(cols)}); err != nil {
			t.Fatal(err)
		}
		v := newLiveView(tty, "我")
		feed(v, handOne()...)

		var rule string
		for _, l := range v.log {
			if strings.HasPrefix(l, "────") {
				rule = l
			}
		}
		if rule == "" {
			t.Fatalf("终端 %d 列时找不到分隔线：%v", cols, v.log)
		}
		// 加 2 是 frame 给每条流水的那两格缩进。
		if w := textui.Width(rule) + 2; w > cols {
			t.Fatalf("终端 %d 列，分隔线连缩进占了 %d 列：%q", cols, w, rule)
		}
		// 缩到再窄，街名和牌也得留着——横线是装饰，那两样是信息。
		for _, want := range []string{"翻牌", "2♣ 7♥ 9♠"} {
			if !strings.Contains(rule, want) {
				t.Fatalf("终端 %d 列时分隔线里缺了 %q：%q", cols, want, rule)
			}
		}
	}
}

// actionRows 从流水里挑出动作行，丢掉街分隔线。
//
// 分隔线是横跨整行的一条线，本来就不分列——把它混进对齐检查里，量的就不是
// 「动作有没有排成列」了。
func actionRows(log []string) []string {
	var out []string
	for _, l := range log {
		if strings.Contains(l, "────") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// colOf 返回 sub 在这一行里从第几**显示列**开始；找不到返回 -1。
func colOf(line, sub string) int {
	i := strings.Index(line, sub)
	if i < 0 {
		return -1
	}
	return textui.Width(line[:i])
}

// TestLogColumnsLineUp：流水排成列，动作那一列不随名字长短飘，
// 而且跟上面座位表共用同一条竖线。
//
// 名字是 --as 传进来的，ee 和 Christopher 能同桌；再加上括号里的位置，长度差得更远。
// 不补齐的话「弃牌」「下注到 30」各自从不同的列开始，一眼扫下来看不出谁做了什么——
// 而看流水本来就是在扫，不是在读。
func TestLogColumnsLineUp(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	seats := []poker.SeatView{
		{Player: "ee", Stack: 200, Position: "SB"},
		{Player: "Lily", Stack: 200, Position: "BB"},
		{Player: "Marco", Stack: 200, Position: "UTG"},
		{Player: "Ken", Stack: 200, Position: "BTN"},
	}
	feed(v,
		poker.Event{Type: poker.EventHandStart, Hand: 1, Blinds: "1/2", Seats: seats},
		poker.Event{Type: poker.EventBlind, Player: "ee", Action: "small_blind", Amount: 1},
		poker.Event{Type: poker.EventBlind, Player: "Lily", Action: "big_blind", Amount: 2},
		poker.Event{Type: poker.EventAction, Player: "Marco", Action: "bet", Committed: 6, Stack: ptr(194), Pot: ptr(9)},
		poker.Event{Type: poker.EventAction, Player: "Ken", Action: "fold", Stack: ptr(200)},
	)

	// 每个人名后面都该跟着他的位置。
	for _, want := range []string{"ee (SB)", "Lily (BB)", "Marco (UTG)", "Ken (BTN)"} {
		if !strings.Contains(strings.Join(v.log, "\n"), want) {
			t.Fatalf("流水里该有 %q：\n%s", want, strings.Join(v.log, "\n"))
		}
	}

	// 动作全都从同一列开始。
	for _, l := range actionRows(v.log) {
		var at int
		for _, verb := range []string{"小盲", "大盲", "下注到", "弃牌"} {
			if c := colOf(l, verb); c >= 0 {
				at = c
				break
			}
		}
		if at != logNameWidth {
			t.Fatalf("这一行的动作从第 %d 列开始，该是第 %d 列：%q", at, logNameWidth, l)
		}
	}

	// 跟座位表共用一条竖线：流水的动作列落在座位表的筹码列上。
	// 筹码是右对齐的，所以比的是那一列的起点——正好是名字列加位置列。
	frame := v.frame()
	var seatLine string
	for _, l := range seatRows(frame) {
		if strings.Contains(l, "Ken") {
			seatLine = l
		}
	}
	if seatLine == "" {
		t.Fatalf("没找到 Ken 那一行座位：\n%s", frame)
	}
	// 座位行前面有两格 marker，流水行前面有两格缩进，两边一样，所以直接比列。
	if got := colOf(seatLine, "BTN") + seatPosWidth; got != logNameWidth+2 {
		t.Fatalf("座位表的筹码列在第 %d 列，流水的动作列在第 %d 列：\n%s", got, logNameWidth+2, frame)
	}
}

// TestScrollingFormatIsUnchanged：把人名从动作里拆出来是给重画那一版用的，
// 滚动那一版的每一行必须一个字节都不变——转录、日志、测试断言都在那条路上。
func TestScrollingFormatIsUnchanged(t *testing.T) {
	cases := []struct {
		ev   poker.Event
		want string
	}{
		{poker.Event{Type: poker.EventAction, Player: "ee", Action: "fold"}, "ee 弃牌"},
		{poker.Event{Type: poker.EventAction, Player: "Lily", Action: "check"}, "Lily 过牌"},
		{poker.Event{Type: poker.EventAction, Player: "Marco", Action: "bet", Committed: 6, Pot: ptr(9)}, "Marco 下注到 6，底池 9"},
		{poker.Event{Type: poker.EventAction, Player: "Ken", Action: "call", Amount: 2, Pot: ptr(4)}, "Ken 跟注 2，底池 4"},
		{poker.Event{Type: poker.EventAction, Player: "ee", Action: "fold", Forced: true}, "ee 弃牌（超时代打）"},
		{poker.Event{Type: poker.EventAction, Player: "ee", Action: "allin", Amount: 30, Committed: 30, Pot: ptr(38)}, "ee 全下 30（本轮共 30），底池 38"},
	}
	for _, c := range cases {
		if got := renderAction(c.ev); got != c.want {
			t.Fatalf("得到 %q，该是 %q", got, c.want)
		}
	}
}

// TestSpacersYieldOnShortTerminals：空行是奢侈品，屏幕矮了就让给内容。
//
// 留白让几块内容分得开，读起来松快。但一屏只剩十来行还拿三行画空白，
// 那是好看压过了好用——真正要看的流水反而被挤没了。
func TestSpacersYieldOnShortTerminals(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("开不了 pty: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	go io.Copy(io.Discard, ptmx)

	blanksAt := func(rows int) int {
		if err := pty.Setsize(tty, &pty.Winsize{Rows: uint16(rows), Cols: 90}); err != nil {
			t.Fatal(err)
		}
		v := newLiveView(tty, "我")
		feed(v, handOne()...)
		n := 0
		for _, l := range strings.Split(v.frame(), "\n") {
			if strings.TrimSpace(l) == "" {
				n++
			}
		}
		return n
	}
	if got := blanksAt(30); got == 0 {
		t.Fatal("屏幕够高时该有留白")
	}
	if got := blanksAt(12); got != 0 {
		t.Fatalf("12 行的终端不该还留 %d 个空行", got)
	}
}

// TestLogPotColumnLinesUp：底池那一列也得对齐。
//
// 「下注到 2，底池 8」读起来是一句话，要一个字一个字看；排成列之后是一张表，
// 扫一眼就知道池子怎么涨起来的——看流水本来就是在扫。
func TestLogPotColumnLinesUp(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v,
		poker.Event{Type: poker.EventHandStart, Hand: 1, Blinds: "1/2"},
		poker.Event{Type: poker.EventAction, Player: "Lily", Action: "bet", Committed: 2, Stack: ptr(198), Pot: ptr(14)},
		poker.Event{Type: poker.EventAction, Player: "Marco", Action: "call", Amount: 2, Stack: ptr(198), Pot: ptr(16)},
		poker.Event{Type: poker.EventAction, Player: "ee", Action: "allin", Amount: 30, Committed: 30, Stack: ptr(0), Pot: ptr(46)},
	)
	want := -1
	rows := actionRows(v.log)
	for _, l := range rows {
		at := strings.Index(l, "底池")
		if at < 0 {
			t.Fatalf("这一行该有底池那一列：%q", l)
		}
		col := textui.Width(l[:at])
		if want < 0 {
			want = col
		}
		// 全下那一行的动作超宽，允许它把底池往右顶——宁可歪一行，也不能把话切一半。
		if l == rows[len(rows)-1] {
			if col <= want {
				t.Fatalf("超宽的动作该把底池顶出去：%q", l)
			}
			continue
		}
		if col != want {
			t.Fatalf("底池从第 %d 列开始，别的行是第 %d 列：%q", col, want, l)
		}
	}
}

// TestActingPlayerIsMarked：牌桌在等谁，座位表上要看得出来。
//
// 这条信息只能来自服务端广播的 turn 事件。客户端自己按座位顺序推「下一个该谁」
// 是行得通但错的：弃牌、全下、暂离都会让顺序拐弯，推错了屏幕就在骗人，
// 而权威只有一个（ADR-0003）。
func TestActingPlayerIsMarked(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, handOne()...)
	feed(v, poker.Event{Type: poker.EventTurn, Player: "bot1"})

	frame := v.frame()
	for _, l := range seatRows(frame) {
		marked := strings.Contains(l, "行动中")
		isBot1 := strings.Contains(l, "bot1")
		if isBot1 && !marked {
			t.Fatalf("bot1 那一行该标着行动中：%q\n%s", l, frame)
		}
		if !isBot1 && marked {
			t.Fatalf("不该是 bot1 之外的人在行动：%q\n%s", l, frame)
		}
	}

	// 他动完了，牌桌就不再等他——下一条 turn 会说出在等谁。
	feed(v, poker.Event{Type: poker.EventAction, Player: "bot1", Action: "fold", Stack: ptr(200)})
	if strings.Contains(v.frame(), "行动中") {
		t.Fatalf("动作已经发生，不该还挂着行动中：\n%s", v.frame())
	}
}

// TestPreviousHandStaysOnScreen：上一手的结尾留在屏幕上，再往前的不留。
//
// 一手打完到下一手开始只隔 --hand-delay，屏幕一清，「谁赢了这个底池」就没了。
// 但也只留一手：留全部的话实时帧就退化成滚动流水账，而那正是这一版要避开的
// 东西（ADR-0017）。想看全部有 --format=text 和手牌历史文件两条现成的路。
func TestPreviousHandStaysOnScreen(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, handOne()...)
	feed(v,
		poker.Event{Type: poker.EventPotAwarded, Pots: []poker.Pot{{Amount: 6, Winners: []string{"bot1"}}}},
		poker.Event{Type: poker.EventHandEnd, Hand: 1, Pot: ptr(6)},
		poker.Event{Type: poker.EventHandStart, Hand: 2, Blinds: "1/2", Seats: []poker.SeatView{
			{Player: "我", Stack: 198, Position: "SB"},
		}})

	got := v.frame()
	if !strings.Contains(got, "上一手（第 1 手") {
		t.Fatalf("该留着上一手：\n%s", got)
	}
	if !strings.Contains(got, "底池 6 → bot1") {
		t.Fatalf("留的该是结尾——谁赢了多少：\n%s", got)
	}

	// 再开一手，留的就该换成第 2 手，第 1 手不再占地方。
	feed(v,
		poker.Event{Type: poker.EventBlind, Player: "我", Action: "small_blind", Amount: 1},
		poker.Event{Type: poker.EventPotAwarded, Pots: []poker.Pot{{Amount: 3, Winners: []string{"我"}}}},
		poker.Event{Type: poker.EventHandEnd, Hand: 2, Pot: ptr(3)},
		poker.Event{Type: poker.EventHandStart, Hand: 3, Blinds: "1/2", Seats: []poker.SeatView{
			{Player: "我", Stack: 201, Position: "BB"},
		}})
	got = v.frame()
	if !strings.Contains(got, "上一手（第 2 手") {
		t.Fatalf("该换成上一手是第 2 手了：\n%s", got)
	}
	if strings.Contains(got, "第 1 手") || strings.Contains(got, "底池 6 → bot1") {
		t.Fatalf("再往前那手不该还占着地方：\n%s", got)
	}
	// 地方够就整手都留着，不砍成固定的几行。
	if n := strings.Count(v.prevBlock(99), "\n"); n != len(v.prev)+1+strings.Count(v.spacer(), "\n") {
		t.Fatalf("地方够的时候该把上一手画全，画了 %d 行、存着 %d 条：\n%s",
			n, len(v.prev), v.prevBlock(99))
	}
}

// TestTrimmedPreviousHandSaysSo：上一手被裁了，标题里要说一声。
//
// 一块砍过头的记录和一手本来就短的牌长得一模一样。不说的话，人会以为自己
// 看到的就是全部——这一屏说的每一句话都得是真的，包括「这是全部」这句。
func TestTrimmedPreviousHandSaysSo(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, poker.Event{Type: poker.EventHandStart, Hand: 1, Blinds: "1/2"})
	for i := 0; i < 12; i++ {
		v.addLog("第 %d 条", i)
	}
	feed(v, poker.Event{Type: poker.EventHandStart, Hand: 2, Blinds: "1/2"})

	full := v.prevBlock(99)
	if strings.Contains(full, "略去") {
		t.Fatalf("地方够的时候不该说裁过：\n%s", full)
	}
	if !strings.Contains(full, "第 0 条") {
		t.Fatalf("地方够就该从头画：\n%s", full)
	}

	// 只给 6 行：标题 + 4 条 + 空行，开头那 9 条得裁掉。
	cut := v.prevBlock(6)
	if !strings.Contains(cut, "略去开头 9 行") {
		t.Fatalf("裁了 9 条就要说裁了 9 条：\n%s", cut)
	}
	if strings.Contains(cut, "第 0 条") {
		t.Fatalf("裁的该是开头：\n%s", cut)
	}
	if !strings.Contains(cut, "第 12 条") && !strings.Contains(cut, "第 11 条") {
		t.Fatalf("结尾必须留着——回头看的就是那儿：\n%s", cut)
	}
}

// TestPreflopHasItsOwnRule：四条街都该有分隔线，翻牌前也不例外。
//
// 只有它没有的话，盲注和动作直接贴在上一手的记录底下，一眼看不出这手牌从哪儿开始。
func TestPreflopHasItsOwnRule(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, handOne()...)
	if len(v.log) == 0 || !strings.HasPrefix(v.log[0], "────") || !strings.Contains(v.log[0], "翻牌前") {
		t.Fatalf("这一手的第一条流水该是翻牌前那条分隔线：%v", v.log)
	}
	// 翻牌前没有新牌翻开，就别画那个占位的短横——它会让人以为牌面上有东西。
	if strings.Contains(v.log[0], "-") {
		t.Fatalf("翻牌前那条不该有占位的短横：%q", v.log[0])
	}
}

// TestPendingRowClosesTheLog：流水末尾也要说出牌桌在等谁。
//
// 座位表上那个标记得先找到他在哪一行才看得见；而在等的时候眼睛盯的是流水最后
// 一行——下一条记录会从那儿冒出来。这一行就是在那个位置上回答「还差谁」。
func TestPendingRowClosesTheLog(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, handOne()...)
	feed(v, poker.Event{Type: poker.EventTurn, Player: "bot1"})

	rows := logRows(v.frame())
	if len(rows) == 0 {
		t.Fatal("流水是空的")
	}
	last := rows[len(rows)-1]
	if !strings.Contains(last, "bot1") || !strings.Contains(last, "行动中") {
		t.Fatalf("流水最后一行该是「bot1 行动中…」：%q\n%s", last, v.frame())
	}
	// 只能有这一行在等：每多一个都是在说牌桌同时等着两个人。
	var waiting int
	for _, l := range rows {
		if strings.Contains(l, "行动中") {
			waiting++
		}
	}
	if waiting != 1 {
		t.Fatalf("流水里有 %d 行在等人，该只有 1 行：\n%s", waiting, v.frame())
	}

	// 它不是记录，所以不进 v.log——不然上一手留下的结尾会以一句永远不会
	// 兑现的「轮到某某」收尾。
	for _, l := range v.log {
		if strings.Contains(l, "行动中") {
			t.Fatalf("v.log 里不该留下这一行：%q", l)
		}
	}

	// 他动完了，这一行就该换成他真做了什么。
	feed(v, poker.Event{Type: poker.EventAction, Player: "bot1", Action: "fold", Stack: ptr(200)})
	rows = logRows(v.frame())
	if last := rows[len(rows)-1]; !strings.Contains(last, "弃牌") || strings.Contains(last, "行动中") {
		t.Fatalf("动作到了，末行该变成弃牌：%q\n%s", last, v.frame())
	}
}

// logRows 从一帧里挑出流水那几行。
//
// 座位行和流水行都是两格缩进加人名，光看长相分不开（这个坑踩过三次），
// 所以按结构来：先让 seatRows 认出座位区，剩下的缩进行才是流水。
func logRows(frame string) []string {
	cur := currentBlock(frame)
	seats := map[string]bool{}
	for _, l := range seatRows(cur) {
		seats[l] = true
	}
	var out []string
	for _, l := range strings.Split(cur, "\n") {
		if !strings.HasPrefix(l, "  ") || seats[l] {
			continue
		}
		out = append(out, strings.TrimSpace(l))
	}
	return out
}

// TestSelfSeatSaysItOnce：轮到自己时，座位表那一行只标一次。
//
// ▶ 和「行动中…」说的是同一件事。一行说两遍不会更醒目，而这一行还要塞位置、
// 筹码、本轮投入和底牌——每多一截都在把它往折行的边上推。
func TestSelfSeatSaysItOnce(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, handOne()...)
	feed(v,
		poker.Event{Type: poker.EventTurn, Player: "我"},
		myTurn(),
	)
	frame := v.frame()
	var mine string
	for _, l := range seatRows(frame) {
		if strings.Contains(l, "我") {
			mine = l
		}
	}
	if !strings.HasPrefix(mine, "▶") {
		t.Fatalf("轮到自己了，这一行该有 ▶：%q\n%s", mine, frame)
	}
	if strings.Contains(mine, "行动中") {
		t.Fatalf("▶ 已经说过了，不该再跟一个行动中：%q\n%s", mine, frame)
	}
	// 流水末尾那一行是另一回事：它回答的是「下一条记录在等谁」，照样要有。
	rows := logRows(frame)
	if last := rows[len(rows)-1]; !strings.Contains(last, "行动中") {
		t.Fatalf("流水末尾该还在等：%q\n%s", last, frame)
	}
}

// fakeClock 是一个只在测试里被手推着走的钟。
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }

// countdownView 造一个接好假钟、已经从 table 事件里知道时限的视图。
func countdownView(t *testing.T, timeout time.Duration) (*liveView, *fakeClock) {
	t.Helper()
	clk := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	v := newLiveView(&strings.Builder{}, "我")
	v.now = clk.now
	feed(v, poker.Event{Type: poker.EventTable, Player: "我", TimeoutMS: int(timeout.Milliseconds())})
	feed(v, handOne()...)
	return v, clk
}

// actingLine 取出标着「行动中」的那一行座位。
func actingLine(t *testing.T, v *liveView) string {
	t.Helper()
	frame := v.frame()
	for _, l := range seatRows(frame) {
		if strings.Contains(l, "行动中") {
			return l
		}
	}
	t.Fatalf("没有哪一行在行动中：\n%s", frame)
	return ""
}

// TestCountdownTicksDown：别人在想的时候，屏幕上那个数要跟着钟走。
func TestCountdownTicksDown(t *testing.T) {
	v, clk := countdownView(t, 30*time.Second)
	feed(v, poker.Event{Type: poker.EventTurn, Player: "bot1"})

	if got := actingLine(t, v); !strings.Contains(got, "行动中… 30s") {
		t.Fatalf("刚轮到他，该是 30s：%q", got)
	}
	clk.add(12 * time.Second)
	if got := actingLine(t, v); !strings.Contains(got, "行动中… 18s") {
		t.Fatalf("过了 12 秒，该是 18s：%q", got)
	}
	// 向上取整：还剩一丁点也是 1s，走到 0 才是 0s。
	clk.add(17*time.Second + 800*time.Millisecond)
	if got := actingLine(t, v); !strings.Contains(got, "行动中… 1s") {
		t.Fatalf("还剩 0.2 秒，该显示 1s：%q", got)
	}
	// 超时之后钟不会往负数走——服务端这时正在替他代打。
	clk.add(5 * time.Second)
	if got := actingLine(t, v); !strings.Contains(got, "行动中… 0s") {
		t.Fatalf("已经超时，该停在 0s：%q", got)
	}
}

// TestIllegalActionDoesNotRestartTheClock：同一个人的第二条 turn 不重新计时。
//
// 发了个非法动作，服务端会把合法动作表重新告诉他一次，于是又来一条 turn；
// 但服务端那边的钟是不重置的（不然一个只会发非法动作的客户端就能无限拖下去）。
// 屏幕要是在这儿重来一遍，它显示的时间就和真正要被代打的时刻对不上了。
func TestIllegalActionDoesNotRestartTheClock(t *testing.T) {
	v, clk := countdownView(t, 30*time.Second)
	feed(v, poker.Event{Type: poker.EventTurn, Player: "bot1"})
	clk.add(20 * time.Second)
	feed(v, poker.Event{Type: poker.EventTurn, Player: "bot1"})
	if got := actingLine(t, v); !strings.Contains(got, "行动中… 10s") {
		t.Fatalf("还是同一个人在想，钟该继续走到 10s：%q", got)
	}
	// 换人了才重新给一份时间。
	feed(v, poker.Event{Type: poker.EventTurn, Player: "bot2"})
	if got := actingLine(t, v); !strings.Contains(got, "行动中… 30s") {
		t.Fatalf("换人了，该重新是 30s：%q", got)
	}
}

// TestNoCountdownForYourself：轮到自己时不画倒计时。
//
// 这一屏在你打字的时候不重画（重画会把终端刚回显的半行命令抹掉），
// 所以画上去的数字会停在你开始打字的那一刻——一个不走的秒表比没有秒表更骗人。
// 时限改由「轮到你了」那行静态地说。
func TestNoCountdownForYourself(t *testing.T) {
	v, _ := countdownView(t, 30*time.Second)
	feed(v,
		poker.Event{Type: poker.EventTurn, Player: "我"},
		myTurn(),
	)
	frame := v.frame()
	rows := logRows(frame)
	last := rows[len(rows)-1]
	if !strings.Contains(last, "行动中") {
		t.Fatalf("流水末尾该还在等自己：%q\n%s", last, frame)
	}
	if strings.ContainsAny(strings.TrimSuffix(last, "行动中…"), "0123456789") {
		t.Fatalf("轮到自己不该画倒计时：%q\n%s", last, frame)
	}
	if !strings.Contains(frame, "限时 30s") {
		t.Fatalf("该在「轮到你了」那行说清时限：\n%s", frame)
	}
}

// TestNoTimeoutNoCountdown：--timeout 0 的桌子上没有钟，也就没有数字。
func TestNoTimeoutNoCountdown(t *testing.T) {
	v, _ := countdownView(t, 0)
	feed(v, poker.Event{Type: poker.EventTurn, Player: "bot1"})
	got := actingLine(t, v)
	if !strings.Contains(got, "行动中…") {
		t.Fatalf("还是该标出在等谁：%q", got)
	}
	if strings.Contains(got, "s") && strings.ContainsAny(got, "0123456789") {
		// 座位表上本来就有筹码数，所以只盯「行动中…」后面那一截。
		tail := got[strings.Index(got, "行动中…"):]
		if strings.ContainsAny(tail, "0123456789") {
			t.Fatalf("不限时的桌子上不该有倒计时：%q", got)
		}
	}
	// tick 也不该动：没有会走的东西，就别每秒往终端里灌一帧。
	var out strings.Builder
	v.out = &out
	v.tick()
	if out.Len() != 0 {
		t.Fatalf("没有倒计时的时候 tick 不该画：%q", out.String())
	}
}

// TestTickStaysQuietOnYourTurn：轮到自己时秒针不许动。
//
// 动一下就把终端刚回显出来的那半行命令抹掉了，而你正在敲它。
func TestTickStaysQuietOnYourTurn(t *testing.T) {
	v, _ := countdownView(t, 30*time.Second)
	feed(v,
		poker.Event{Type: poker.EventTurn, Player: "我"},
		myTurn(),
	)
	var out strings.Builder
	v.out = &out
	v.tick()
	if out.Len() != 0 {
		t.Fatalf("轮到自己时 tick 不该重画：%q", out.String())
	}

	// 轮到别人就该动了。
	feed(v, poker.Event{Type: poker.EventAction, Player: "我", Action: "check", Stack: ptr(198)})
	feed(v, poker.Event{Type: poker.EventTurn, Player: "bot1"})
	out.Reset()
	v.tick()
	if out.Len() == 0 {
		t.Fatal("轮到别人了，秒针该走")
	}
}

// TestTickLeavesTypedTextAlone：秒针走一格不许碰输入行。
//
// 这是倒计时最容易毁掉的东西：终端把你敲的半行命令回显在输入行上，而整帧重画
// 会连它一起抹掉——每秒一次。所以秒针走的是局部重画：存光标、只改变了的那几行、
// 把光标放回去，从头到尾没有清屏，也没写过输入行。
func TestTickLeavesTypedTextAlone(t *testing.T) {
	v, clk := countdownView(t, 30*time.Second)
	feed(v, poker.Event{Type: poker.EventTurn, Player: "bot1"})
	v.render() // 先让屏幕上有一帧，下面才有得比

	var out strings.Builder
	v.out = &out
	clk.add(time.Second)
	v.tick()

	got := out.String()
	if got == "" {
		t.Fatal("秒针该走一格")
	}
	if !strings.HasPrefix(got, "\0337") || !strings.HasSuffix(got, "\0338") {
		t.Fatalf("要先存光标、最后放回去：%q", got)
	}
	if strings.Contains(got, "\033[J") {
		t.Fatalf("局部重画不许清屏——清了就把用户正在敲的字清掉了：%q", got)
	}
	if strings.Contains(got, "> ") {
		t.Fatalf("输入行一个字节都不该写：%q", got)
	}
	// 变了的就那两行（座位表一行、流水末尾一行），别的不许重写。
	if n := strings.Count(got, "\033[K"); n != 2 {
		t.Fatalf("该只重写 2 行，重写了 %d 行：%q", n, got)
	}
	if !strings.Contains(got, "行动中… 29s") {
		t.Fatalf("重写的内容不对：%q", got)
	}
}

// TestHeightChangeFallsBackToFullRedraw：帧高变了就得整帧重画。
//
// 局部重画靠「每一行还在原来那个位置」，多一行流水就全乱了。
func TestHeightChangeFallsBackToFullRedraw(t *testing.T) {
	v, _ := countdownView(t, 30*time.Second)
	v.render()

	// 没人在等，这条动作是净多出来的一行。
	var out strings.Builder
	v.out = &out
	_ = v.event(poker.Event{Type: poker.EventAction, Player: "bot1", Action: "fold", Stack: ptr(199)}, nil)
	if got := out.String(); !strings.Contains(got, "\r\033[J") {
		t.Fatalf("多了一条流水，该整帧重画：%q", got)
	}
}

// TestActionKeepsHeightAndSpareTheTypedLine：动作替掉「在等谁」那一行，帧高没变。
//
// 这是局部重画白赚的一个好处：别人弃牌时流水末尾那行「行动中…」正好被他的
// 「弃牌」顶掉，一出一进帧高不变，于是你打到一半的那行命令也活下来了。
func TestActionKeepsHeightAndSparesTheTypedLine(t *testing.T) {
	v, _ := countdownView(t, 30*time.Second)
	feed(v, poker.Event{Type: poker.EventTurn, Player: "bot1"})
	v.render()

	var out strings.Builder
	v.out = &out
	_ = v.event(poker.Event{Type: poker.EventAction, Player: "bot1", Action: "fold", Stack: ptr(199)}, nil)
	got := out.String()
	if strings.Contains(got, "\033[J") {
		t.Fatalf("帧高没变，不该清屏：%q", got)
	}
	if !strings.Contains(got, "弃牌") {
		t.Fatalf("该把那一行改成弃牌：%q", got)
	}
}

// bigTable 造一手五个人的牌，并且上一手留着结尾——人多的桌子上座位表本身就占五行，
// 这正是「一帧塞不塞得进一屏」最容易出事的形状。
func bigTable(v *liveView) {
	seats := []poker.SeatView{
		{Player: "Lily", Stack: 131, Position: "UTG"},
		{Player: "Marco", Stack: 257, Position: "CO"},
		{Player: "Ken", Stack: 329, Position: "BTN"},
		{Player: "Dora", Stack: 41, Position: "SB"},
		{Player: "我", Stack: 242, Position: "BB"},
	}
	feed(v,
		poker.Event{Type: poker.EventHandStart, Hand: 7, Blinds: "1/2", Seats: seats},
		poker.Event{Type: poker.EventBlind, Player: "Dora", Action: "small_blind", Amount: 1},
		poker.Event{Type: poker.EventBlind, Player: "我", Action: "big_blind", Amount: 2},
		poker.Event{Type: poker.EventAction, Player: "Marco", Action: "check", Stack: ptr(257)},
		poker.Event{Type: poker.EventShowdown, Showdown: []poker.ShowdownEntry{
			{Player: "Marco", Cards: poker.MustParseCards("8s 5s"), Category: "高牌"},
			{Player: "Dora", Cards: poker.MustParseCards("4h Ad"), Category: "一对"},
		}},
		poker.Event{Type: poker.EventPotAwarded, Pots: []poker.Pot{{Amount: 19, Winners: []string{"Dora"}}}},
		poker.Event{Type: poker.EventHandEnd},
		poker.Event{Type: poker.EventHandStart, Hand: 8, Blinds: "1/2", Seats: seats},
		poker.Event{Type: poker.EventBlind, Player: "Dora", Action: "small_blind", Amount: 1},
		poker.Event{Type: poker.EventBlind, Player: "我", Action: "big_blind", Amount: 2},
		poker.Event{Type: poker.EventHoleCards, Player: "我", Cards: poker.MustParseCards("Kh Th")},
		poker.Event{Type: poker.EventAction, Player: "Lily", Action: "fold", Stack: ptr(131)},
		poker.Event{Type: poker.EventStreet, Street: "flop", Cards: poker.MustParseCards("2c 7h 9s"),
			Board: poker.MustParseCards("2c 7h 9s"), Pot: ptr(6)},
		poker.Event{Type: poker.EventAction, Player: "Marco", Action: "bet", Committed: 6, Stack: ptr(251), Pot: ptr(12)},
		poker.Event{Type: poker.EventTurn, Player: "Ken"},
	)
}

// TestFrameNeverOutgrowsTheTerminal 是这一版的地基。
//
// 一帧要是比屏幕高，终端就会滚动；滚动之后「上移 N 行」回到的不再是原来那个
// 位置，于是最上面那一块被推出屏幕、再也收不回来，往下每一帧都画在错的地方。
// 屏幕从此一路烂下去，而且自己好不了——这不是不好看，是坏了。
//
// 所以这条不是抽查几个高度，是把「人多的桌子 + 留着上一手」这个最挤的形状
// 放到一排高度上挨个量。以前只有一个 3 行终端的边角测试，量的是「别把流水删光」，
// 没有人量过上限；五个人的桌子配一个矮窗口就真的顶出去了。
func TestFrameNeverOutgrowsTheTerminal(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("开不了 pty: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	go io.Copy(io.Discard, ptmx)

	for rows := 14; rows <= 40; rows++ {
		if err := pty.Setsize(tty, &pty.Winsize{Rows: uint16(rows), Cols: 100}); err != nil {
			t.Fatal(err)
		}
		v := newLiveView(tty, "我")
		bigTable(v)

		frame := v.frame()
		// 最后一行不带换行，光标停在它上面——所以行数是换行符加一。
		if got := strings.Count(frame, "\n") + 1; got > rows {
			t.Fatalf("终端 %d 行，这一帧却有 %d 行：\n%s", rows, got, frame)
		}
		// 正在打的这一手一行都不能少：上一手让位，不是反过来。
		for _, must := range []string{"第 8 手", "Ken", "K♥ T♥", "公共牌", "> "} {
			if !strings.Contains(frame, must) {
				t.Fatalf("终端 %d 行时把 %q 挤掉了：\n%s", rows, must, frame)
			}
		}
	}
}

// TestPreviousHandYieldsFirst：屏幕不够时，先砍上一手，不砍正在打的。
func TestPreviousHandYieldsFirst(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("开不了 pty: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	go io.Copy(io.Discard, ptmx)

	at := func(rows int) string {
		if err := pty.Setsize(tty, &pty.Winsize{Rows: uint16(rows), Cols: 100}); err != nil {
			t.Fatal(err)
		}
		v := newLiveView(tty, "我")
		bigTable(v)
		return v.frame()
	}
	if f := at(40); !strings.Contains(f, "上一手（第 7 手") || !strings.Contains(f, "Marco (CO)         过牌") {
		t.Fatalf("屏幕够高就该把上一手留全：\n%s", f)
	}
	if f := at(9); strings.Contains(f, "上一手") {
		t.Fatalf("9 行的终端上，上一手该整块让位：\n%s", f)
	}

	// 中间那一段是逐行让的，不是一刀切。但让掉的必须是开头那几行：prev 存的是
	// 上一手的结尾，而结尾里那句「底池归了谁」正是留着它的全部理由，砍到只剩
	// 一个标题就是白占地方。
	var prevRows []int
	for rows := 9; rows <= 40; rows++ {
		f := at(rows)
		n := 0
		for _, l := range strings.Split(f, "\n") {
			if strings.HasPrefix(l, "上一手") {
				n = 1
				continue
			}
			if n > 0 && strings.TrimSpace(l) != "" && !strings.HasPrefix(l, "第 ") {
				n++
				continue
			}
			if n > 0 {
				break
			}
		}
		prevRows = append(prevRows, n)
		if n == 1 {
			t.Fatalf("终端 %d 行时上一手只剩个标题：\n%s", rows, f)
		}
		if n > 0 && !strings.Contains(f, "底池 19 → Dora") {
			t.Fatalf("终端 %d 行时把上一手的结果砍掉了：\n%s", rows, f)
		}
	}
	// 屏幕越高留得越多——但只在同一档里比。跨过 roomyHeight 那一行时留白重新
	// 出现，一下子多占三行，上一手就得让回去几行：那是「够宽敞了就该好看点」
	// 这条规则的明码标价，不是账算错了。
	for i := 1; i < len(prevRows); i++ {
		rows := 9 + i
		if rows == roomyHeight {
			continue // 留白在这一行回来，允许它一次性少留几行
		}
		if prevRows[i] < prevRows[i-1] {
			t.Fatalf("终端 %d 行留了 %d 行，%d 行反而只留 %d 行",
				rows-1, prevRows[i-1], rows, prevRows[i])
		}
	}
}

// TestErrorSurvivesTheTurnThatFollowsIt：动作非法时那句话必须留在屏幕上。
//
// 服务端的非法动作处理是「回一条 error，紧跟着把 turn 和 your_turn 再发一遍」，
// 好让你重新看到合法动作表。提醒要是被下一条事件无差别地清掉，这条错误就只在
// 屏幕上活几毫秒——人看到的是「敲了 c，什么也没发生」，而真正的原因被自己
// 后面那两条事件擦掉了。这是真发生过的一个 bug，不是假想。
func TestErrorSurvivesTheTurnThatFollowsIt(t *testing.T) {
	v := newLiveView(&strings.Builder{}, "我")
	feed(v, handOne()...)

	// 服务端对一个不合法的 call 的完整回应，顺序和条数都照抄 poker.Hand.Apply。
	feed(v,
		poker.Event{Type: poker.EventError, Code: "nothing_to_call", Message: "没有注要跟，用 check"},
		poker.Event{Type: poker.EventTurn, Player: "我"},
		myTurn(),
	)
	if got := v.frame(); !strings.Contains(got, "nothing_to_call") {
		t.Fatalf("重新轮到你了，但那句「为什么不行」得还在：\n%s", got)
	}

	// 牌桌真的动了，提醒才过期。
	feed(v, poker.Event{Type: poker.EventAction, Player: "我", Action: "check", Stack: ptr(198)})
	if got := v.frame(); strings.Contains(got, "nothing_to_call") {
		t.Fatalf("已经做出合法动作了，不该还挂着上一句错误：\n%s", got)
	}
}
