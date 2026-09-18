package poker

import (
	"math/rand/v2"
	"testing"
)

// harness 是驱动状态机的测试夹具：喂动作，收事件，随时能问「现在轮到谁」。
type harness struct {
	t      *testing.T
	h      *Hand
	events []Event
	start  int // 开局时桌上的筹码总额
}

func newHarness(t *testing.T, stacks []int, button int, blinds Blinds, seed uint64) *harness {
	t.Helper()
	names := []string{"alice", "bob", "carol", "dave"}
	seats := make([]Seat, len(stacks))
	total := 0
	for i, s := range stacks {
		seats[i] = Seat{Player: names[i], Stack: s}
		total += s
	}
	d := NewDeck(rand.New(rand.NewPCG(seed, 7)))
	h, events := NewHand(1, seats, button, blinds, d)
	return &harness{t: t, h: h, events: events, start: total}
}

// do 让当前行动者做一个动作。
func (x *harness) do(action string) {
	x.t.Helper()
	player := x.h.Turn()
	if player == "" {
		x.t.Fatalf("牌局已经结束了，没人能做 %q", action)
	}
	a, err := ParseAction(action)
	if err != nil {
		x.t.Fatalf("解析动作 %q 失败: %v", action, err)
	}
	x.events = append(x.events, x.h.Apply(player, a)...)
}

// doAs 指定某个玩家做动作，用来测「不是你的回合」这类情形。
func (x *harness) doAs(player, action string) {
	x.t.Helper()
	a, err := ParseAction(action)
	if err != nil {
		x.t.Fatalf("解析动作 %q 失败: %v", action, err)
	}
	x.events = append(x.events, x.h.Apply(player, a)...)
}

func (x *harness) last() Event { return x.events[len(x.events)-1] }

// snapshotOf 取某人最近一次 your_turn 里的快照。
func (x *harness) snapshotOf(player string) *Snapshot {
	for i := len(x.events) - 1; i >= 0; i-- {
		if x.events[i].Type == EventYourTurn && x.events[i].Player == player {
			return x.events[i].Snapshot
		}
	}
	return nil
}

func (x *harness) typesOf(kind EventType) []Event {
	var out []Event
	for _, e := range x.events {
		if e.Type == kind {
			out = append(out, e)
		}
	}
	return out
}

// checkChipsConserved 是最硬的那条不变量：筹码既不会凭空产生，也不会凭空消失。
func (x *harness) checkChipsConserved() {
	x.t.Helper()
	if !x.h.Done() {
		x.t.Fatal("牌还没打完")
	}
	total := 0
	for _, stack := range x.h.Result().Stacks {
		total += stack
	}
	if total != x.start {
		x.t.Fatalf("筹码对不上：开局 %d，结束 %d", x.start, total)
	}
}

// TestBlindPositions 盯住盲注位置与首个行动者。
//
// 单挑的特例（button 自己是小盲、preflop 先说话、翻牌后后说话）跟多人局是反的，
// 写错了不会崩，只会让两人局从头到尾都在错误的位置上下注。
func TestBlindPositions(t *testing.T) {
	t.Run("三人局", func(t *testing.T) {
		x := newHarness(t, []int{200, 200, 200}, 0, Blinds{1, 2}, 1)
		blinds := x.typesOf(EventBlind)
		if blinds[0].Player != "bob" || blinds[0].Action != "small_blind" {
			t.Fatalf("小盲应该是 button 左手第一位 bob，得到 %+v", blinds[0])
		}
		if blinds[1].Player != "carol" || blinds[1].Action != "big_blind" {
			t.Fatalf("大盲应该是 carol，得到 %+v", blinds[1])
		}
		if x.h.Turn() != "alice" {
			t.Fatalf("preflop 应该从大盲左手第一位 alice 开始，得到 %s", x.h.Turn())
		}
	})

	t.Run("单挑", func(t *testing.T) {
		x := newHarness(t, []int{200, 200}, 0, Blinds{1, 2}, 1)
		blinds := x.typesOf(EventBlind)
		if blinds[0].Player != "alice" {
			t.Fatalf("单挑时 button 自己就是小盲，得到 %s", blinds[0].Player)
		}
		if blinds[1].Player != "bob" {
			t.Fatalf("大盲应该是 bob，得到 %s", blinds[1].Player)
		}
		if x.h.Turn() != "alice" {
			t.Fatalf("单挑 preflop 应该 button 先说话，得到 %s", x.h.Turn())
		}
		// 翻牌后反过来：大盲先说话。
		x.do("call")
		x.do("check")
		if x.h.Turn() != "bob" {
			t.Fatalf("单挑翻牌后应该大盲 bob 先说话，得到 %s", x.h.Turn())
		}
	})
}

// TestBigBlindGetsOption：所有人只是跟到大盲时，大盲仍有一次行动机会。
// 这一条最容易漏——漏了的话 preflop 会少一轮，而且没有任何报错。
func TestBigBlindGetsOption(t *testing.T) {
	x := newHarness(t, []int{200, 200, 200}, 0, Blinds{1, 2}, 3)
	x.do("call") // alice (UTG)
	x.do("call") // bob (小盲补齐)
	if x.h.Turn() != "carol" {
		t.Fatalf("大盲 carol 应该还有一次 option，得到轮到 %s", x.h.Turn())
	}
	snap := x.snapshotOf("carol")
	if snap.ToCall != 0 {
		t.Fatalf("大盲此刻不欠注，to_call 应该是 0，得到 %d", snap.ToCall)
	}
	if !hasLegal(snap, "check") {
		t.Fatalf("大盲应该能 check，合法动作是 %+v", snap.Legal)
	}
	if !hasLegal(snap, "bet") {
		t.Fatalf("大盲应该还能加注，合法动作是 %+v", snap.Legal)
	}
	// 他 check 之后 preflop 才结束。
	x.do("check")
	if len(x.typesOf(EventStreet)) != 1 {
		t.Fatalf("大盲 check 之后应该翻 flop，得到 %d 条 street 事件", len(x.typesOf(EventStreet)))
	}
}

func hasLegal(s *Snapshot, action string) bool {
	for _, l := range s.Legal {
		if l.Action == action {
			return true
		}
	}
	return false
}

func legalOf(s *Snapshot, action string) (LegalAction, bool) {
	for _, l := range s.Legal {
		if l.Action == action {
			return l, true
		}
	}
	return LegalAction{}, false
}

// TestFoldToOneLeavesNoShowdown：弃到只剩一人时不摊牌，
// 而且谁的底牌都不会出现在事件里——ADR-0006 说的「弃牌者底牌永不公开」。
func TestFoldToOneLeavesNoShowdown(t *testing.T) {
	x := newHarness(t, []int{200, 200, 200}, 0, Blinds{1, 2}, 5)
	x.do("fold") // alice
	x.do("fold") // bob（小盲）
	if !x.h.Done() {
		t.Fatal("只剩大盲一个人，这手牌该结束了")
	}
	if len(x.typesOf(EventShowdown)) != 0 {
		t.Fatal("没人需要亮牌的时候不该有摊牌事件")
	}
	res := x.h.Result()
	if len(res.Winners) != 1 || res.Winners[0] != "carol" {
		t.Fatalf("底池该归唯一没弃牌的 carol，得到 %v", res.Winners)
	}
	// carol 只赢下两个盲注：1 + 2 = 3，她自己投了 2，所以净赚 1。
	if got := res.Stacks["carol"]; got != 201 {
		t.Fatalf("carol 应该有 201，得到 %d", got)
	}
	x.checkChipsConserved()

	// 逐条事件扫一遍，任何人的底牌都不许露面。
	for _, ev := range x.events {
		if ev.To != "" {
			continue
		}
		for _, p := range []string{"alice", "bob", "carol"} {
			for _, c := range res.Hole[p] {
				for _, shown := range ev.Cards {
					if shown == c {
						t.Fatalf("广播事件 %s 泄漏了 %s 的底牌 %s", ev.Type, p, c)
					}
				}
			}
		}
	}
}

// TestMinRaiseIsEnforced：加注不足额要被挡回来，而且回的是一条带 min 的结构化错误，
// 行动权仍归他（ADR-0011）。
func TestMinRaiseIsEnforced(t *testing.T) {
	x := newHarness(t, []int{200, 200, 200}, 0, Blinds{1, 2}, 9)
	// preflop 最高注是 2，最小加注到 4。推到 3 应该被拒。
	x.doAs("alice", "bet 3")
	last := x.events[len(x.events)-2] // 错误事件，后面跟着重发的 your_turn
	if last.Type != EventError || last.Code != "min_raise" {
		t.Fatalf("想要 min_raise 错误，得到 %+v", last)
	}
	if last.Min != 4 {
		t.Fatalf("错误里该告诉他最小要推到 4，得到 %d", last.Min)
	}
	if x.h.Turn() != "alice" {
		t.Fatalf("非法动作之后轮次仍该归 alice，得到 %s", x.h.Turn())
	}
	if x.last().Type != EventYourTurn {
		t.Fatalf("该重发一次 your_turn，得到 %s", x.last().Type)
	}
	// 推到 4 就该过。
	x.do("bet 4")
	if x.h.Turn() != "bob" {
		t.Fatalf("alice 加注之后该轮到 bob，得到 %s", x.h.Turn())
	}
}

// TestThreeIllegalActionsGetForced：连续三次非法就替他做决定（ADR-0011）。
func TestThreeIllegalActionsGetForced(t *testing.T) {
	x := newHarness(t, []int{200, 200, 200}, 0, Blinds{1, 2}, 11)
	// alice 欠 2 块，check 是非法的。连做三次。
	x.doAs("alice", "check")
	x.doAs("alice", "check")
	if x.h.Turn() != "alice" {
		t.Fatalf("两次非法之后轮次仍该归 alice，得到 %s", x.h.Turn())
	}
	x.doAs("alice", "check")

	var forced *Event
	for i := range x.events {
		if x.events[i].Type == EventAction && x.events[i].Forced {
			forced = &x.events[i]
		}
	}
	if forced == nil {
		t.Fatal("第三次非法之后该有一个被强制的动作")
	}
	// 她欠着注，能 check 就 check、否则 fold —— 这里只能 fold。
	if forced.Action != "fold" || forced.Player != "alice" {
		t.Fatalf("想要替 alice fold，得到 %+v", forced)
	}
	if x.h.Turn() != "bob" {
		t.Fatalf("强制动作之后该轮到 bob，得到 %s", x.h.Turn())
	}
}

// TestForcedActionPrefersCheck：能过牌的时候，替他做的决定是 check 而不是 fold。
func TestForcedActionPrefersCheck(t *testing.T) {
	x := newHarness(t, []int{200, 200, 200}, 0, Blinds{1, 2}, 13)
	x.do("call")  // alice
	x.do("call")  // bob
	x.do("check") // carol，preflop 结束，翻 flop
	// flop 上 bob 先说话，他不欠注，call 是非法的。
	x.doAs("bob", "call")
	x.doAs("bob", "call")
	x.doAs("bob", "call")
	var forced *Event
	for i := range x.events {
		if x.events[i].Type == EventAction && x.events[i].Forced {
			forced = &x.events[i]
		}
	}
	if forced == nil || forced.Action != "check" {
		t.Fatalf("不欠注时该替他 check，得到 %+v", forced)
	}
}

// TestNotYourTurn：没轮到就别说话，而且这不该影响真正该行动的人。
func TestNotYourTurn(t *testing.T) {
	x := newHarness(t, []int{200, 200, 200}, 0, Blinds{1, 2}, 17)
	x.doAs("carol", "fold")
	if x.last().Type != EventError || x.last().Code != "not_your_turn" {
		t.Fatalf("想要 not_your_turn，得到 %+v", x.last())
	}
	if x.h.Turn() != "alice" {
		t.Fatalf("轮次不该被打断，得到 %s", x.h.Turn())
	}
	// 而且这条错误是定向的，别人看不到某人在乱按键。
	if x.last().To != "carol" {
		t.Fatalf("错误事件该只发给 carol，得到 To=%q", x.last().To)
	}
}

// TestCallBeyondStackBecomesAllIn：筹码不够跟满就是推光，不是错误。
func TestCallBeyondStackBecomesAllIn(t *testing.T) {
	x := newHarness(t, []int{200, 200, 30}, 0, Blinds{1, 2}, 19)
	x.do("bet 100") // alice 加注到 100
	x.do("fold")    // bob
	x.do("call")    // carol 只有 30-2=28 可跟，直接推光

	var allin *Event
	for i := range x.events {
		if x.events[i].Type == EventAction && x.events[i].Player == "carol" {
			allin = &x.events[i]
		}
	}
	if allin.Action != "allin" {
		t.Fatalf("carol 跟不满，这一下该记成 allin，得到 %+v", allin)
	}
	if *allin.Stack != 0 {
		t.Fatalf("推光之后筹码该是 0，得到 %d", *allin.Stack)
	}
	if !x.h.Done() {
		t.Fatal("两人都无法再行动，这手牌该一路打到摊牌")
	}
	// alice 推到 100，carol 只跟得起 30，多出来的 70 没人匹配，必须原样退回——
	// 不管这手牌谁赢，alice 至少得剩 200-100+70 = 170。
	res := x.h.Result()
	if res.Stacks["alice"] < 170 {
		t.Fatalf("没被匹配的 70 该退回给 alice，她却只剩 %d", res.Stacks["alice"])
	}
	// bob 弃牌时留下的 1 块小盲也在池里，所以赢家那边合计是 231 而不是 230。
	if got := res.Stacks["alice"] + res.Stacks["carol"]; got != 231 {
		t.Fatalf("alice 与 carol 的筹码合计该是 231，得到 %d", got)
	}
	x.checkChipsConserved()
}

// TestShortAllInDoesNotReopenBetting：不足额的 all-in 不重开下注轮。
//
// 这条规则写错的症状是凭空多出一轮下注，牌局照样能走完，没人会发现。
func TestShortAllInDoesNotReopenBetting(t *testing.T) {
	// dave 只有 5 块，preflop 推光只到 5，相对最高注 2 只加了 3，不足一个大盲。
	x := newHarness(t, []int{200, 200, 200, 5}, 0, Blinds{1, 2}, 23)
	// 座位顺序 alice(button) bob(SB) carol(BB) dave(UTG)
	if x.h.Turn() != "dave" {
		t.Fatalf("该从 dave 开始，得到 %s", x.h.Turn())
	}
	x.do("allin")  // dave 推到 5
	x.do("bet 10") // alice 加注到 10
	x.do("call")   // bob
	x.do("call")   // carol
	// alice 已经行动过，而且 dave 那次是不足额 all-in，不该让 alice 重新获得行动权。
	if !x.h.Done() && x.h.Turn() == "alice" {
		t.Fatal("不足额的 all-in 不该重开下注轮，alice 不该被再问一次")
	}
	if len(x.typesOf(EventStreet)) == 0 {
		t.Fatal("preflop 该结束并翻出 flop")
	}
	x.do("check")
	x.do("check")
	x.do("check")
	if len(x.typesOf(EventStreet)) != 2 {
		t.Fatalf("该翻到 turn 了，得到 %d 条 street 事件", len(x.typesOf(EventStreet)))
	}
}

// TestAllInRunsOutTheBoard：所有人都 all-in 之后，剩下的公共牌一路发完再摊牌。
func TestAllInRunsOutTheBoard(t *testing.T) {
	x := newHarness(t, []int{100, 100}, 0, Blinds{1, 2}, 29)
	x.do("allin") // alice
	x.do("call")  // bob 跟到底
	if !x.h.Done() {
		t.Fatal("两人都推光了，这手牌该直接走完")
	}
	streets := x.typesOf(EventStreet)
	if len(streets) != 3 {
		t.Fatalf("该发出 flop / turn / river 三条街，得到 %d 条", len(streets))
	}
	if got := len(x.h.Result().Community); got != 5 {
		t.Fatalf("公共牌该有 5 张，得到 %d 张", got)
	}
	if len(x.typesOf(EventShowdown)) != 1 {
		t.Fatal("两人都没弃牌，该摊牌")
	}
	x.checkChipsConserved()
}

// TestCheckAroundAdvancesStreets：一路 check 到 river，四条街都得走到。
func TestCheckAroundAdvancesStreets(t *testing.T) {
	x := newHarness(t, []int{200, 200}, 0, Blinds{1, 2}, 31)
	x.do("call")  // alice(button/小盲) 补齐
	x.do("check") // bob(大盲) option
	for street := 0; street < 3; street++ {
		x.do("check")
		x.do("check")
	}
	if !x.h.Done() {
		t.Fatal("river 也 check 完了，该摊牌结束")
	}
	streets := x.typesOf(EventStreet)
	if len(streets) != 3 || streets[0].Street != "flop" || streets[1].Street != "turn" || streets[2].Street != "river" {
		t.Fatalf("三条街的名字不对：%v", streets)
	}
	x.checkChipsConserved()
}

// TestSnapshotTellsEverythingNeeded：your_turn 的快照要自带决策所需的一切（ADR-0007），
// agent 不该需要自己累积状态。
func TestSnapshotTellsEverythingNeeded(t *testing.T) {
	x := newHarness(t, []int{200, 150, 80}, 0, Blinds{1, 2}, 37)
	x.do("bet 20") // alice
	snap := x.snapshotOf("bob")
	if snap == nil {
		t.Fatal("bob 没收到 your_turn")
	}
	if len(snap.Hole) != 2 {
		t.Fatalf("快照里该有自己的两张底牌，得到 %v", snap.Hole)
	}
	if snap.Street != "preflop" {
		t.Fatalf("街名不对：%s", snap.Street)
	}
	if snap.ToCall != 19 { // bob 是小盲，已投 1，要补到 20
		t.Fatalf("to_call 该是 19，得到 %d", snap.ToCall)
	}
	if snap.Pot != 23 { // 1 + 2 + 20
		t.Fatalf("底池该是 23，得到 %d", snap.Pot)
	}
	if len(snap.Seats) != 3 {
		t.Fatalf("快照该带上全部三个座位，得到 %d 个", len(snap.Seats))
	}
	bet, ok := legalOf(snap, "bet")
	if !ok {
		t.Fatal("bob 该能加注")
	}
	if bet.Min != 38 { // 最高 20 + 上一次加注增量 18
		t.Fatalf("最小加注该推到 38，得到 %d", bet.Min)
	}
	if bet.Max != 150 {
		t.Fatalf("最多能推到 150（他的全部筹码），得到 %d", bet.Max)
	}
	// 快照里绝不能有别人的底牌。
	for _, s := range snap.Seats {
		_ = s // SeatView 结构里根本没有放底牌的地方，这里靠类型守住
	}
}

// TestChipsAreConservedAcrossRandomHands 是最硬的一条：
// 让随机 agent 从合法动作列表里瞎选，跑几百手牌，筹码总额一分都不能变。
//
// 这条测试不关心谁赢，它盯的是「钱有没有算错」——边池、退还、平分余数这些最易错的地方，
// 错了就会在总额上露出来。
func TestChipsAreConservedAcrossRandomHands(t *testing.T) {
	stacks := [][]int{
		{200, 200},
		{200, 150, 80},
		{200, 200, 200, 200},
		{50, 200, 35, 500}, // 筹码差距悬殊，专门制造边池
		{7, 200, 13},       // 连盲注都交不满的小筹码
	}
	for si, base := range stacks {
		for seed := uint64(1); seed <= 60; seed++ {
			r := rand.New(rand.NewPCG(seed, uint64(si)+100))
			seats := make([]Seat, len(base))
			names := []string{"alice", "bob", "carol", "dave"}
			total := 0
			for i, s := range base {
				seats[i] = Seat{Player: names[i], Stack: s}
				total += s
			}
			d := NewDeck(rand.New(rand.NewPCG(seed, 999)))
			h, events := NewHand(1, seats, int(seed)%len(base), Blinds{1, 2}, d)

			guard := 0
			for !h.Done() {
				guard++
				if guard > 500 {
					t.Fatalf("组 %d seed %d：牌局停不下来", si, seed)
				}
				player := h.Turn()
				snap := lastSnapshotFor(events, player)
				if snap == nil {
					t.Fatalf("组 %d seed %d：轮到 %s 却没给他快照", si, seed, player)
				}
				action := randomLegalAction(r, snap)
				events = append(events, h.Apply(player, action)...)
			}

			res := h.Result()
			got := 0
			for _, s := range res.Stacks {
				got += s
			}
			if got != total {
				t.Fatalf("组 %d seed %d：筹码对不上，开局 %d，结束 %d", si, seed, total, got)
			}
			for p, s := range res.Stacks {
				if s < 0 {
					t.Fatalf("组 %d seed %d：%s 的筹码成了负数 %d", si, seed, p, s)
				}
			}
		}
	}
}

func lastSnapshotFor(events []Event, player string) *Snapshot {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == EventYourTurn && events[i].Player == player {
			return events[i].Snapshot
		}
	}
	return nil
}

// randomLegalAction 严格只从快照给出的合法动作里选，选完的动作必须被接受——
// 要是这里选出的东西被判非法，那说明合法动作列表本身在撒谎。
func randomLegalAction(r *rand.Rand, snap *Snapshot) Action {
	l := snap.Legal[r.IntN(len(snap.Legal))]
	switch l.Action {
	case "fold":
		return Action{Kind: Fold}
	case "check":
		return Action{Kind: Check}
	case "call":
		return Action{Kind: Call}
	case "allin":
		return Action{Kind: AllIn}
	case "bet":
		amount := l.Min
		if l.Max > l.Min {
			amount += r.IntN(l.Max - l.Min + 1)
		}
		return Action{Kind: BetTo, Amount: amount}
	}
	return Action{Kind: Fold}
}

// TestLegalActionsNeverLie：合法动作列表里的东西，做下去必须真的被接受。
//
// 这是 ADR-0007 那句「直接给出合法动作列表砍掉一整类 agent 侧 bug」的前提——
// 列表一旦撒谎，agent 照着做反而会被判非法，那还不如不给。
func TestLegalActionsNeverLie(t *testing.T) {
	for seed := uint64(1); seed <= 80; seed++ {
		r := rand.New(rand.NewPCG(seed, 4242))
		d := NewDeck(rand.New(rand.NewPCG(seed, 31)))
		h, events := NewHand(1, []Seat{
			{"alice", 200}, {"bob", 90}, {"carol", 40},
		}, int(seed)%3, Blinds{1, 2}, d)

		for !h.Done() {
			player := h.Turn()
			snap := lastSnapshotFor(events, player)
			action := randomLegalAction(r, snap)
			got := h.Apply(player, action)
			for _, ev := range got {
				if ev.Type == EventError {
					t.Fatalf("seed %d：合法动作列表说 %v 可以做，实际却被判 %s（%s）",
						seed, action, ev.Code, ev.Message)
				}
			}
			events = append(events, got...)
		}
	}
}
