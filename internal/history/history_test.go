package history

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xujnan/poker-cli/internal/poker"
)

// playHand 用给定种子打完一手牌，整理成一条历史记录——
// 跟服务端 startHand / settleHand 走的是同一套（同一个 RandFor、同一个 Of）。
func playHand(t *testing.T, seed uint64, stacks []int, button int, r *rand.Rand) Record {
	t.Helper()
	names := []string{"alice", "bob", "carol", "dave"}
	seats := make([]poker.Seat, len(stacks))
	before := make([]Seat, len(stacks))
	for i, st := range stacks {
		seats[i] = poker.Seat{Player: names[i], Stack: st}
		before[i] = Seat{Player: names[i], Stack: st}
	}
	blinds := poker.Blinds{Small: 1, Big: 2}

	hand, events := poker.NewHand(1, seats, button, blinds, DeckFor(seed))
	for guard := 0; !hand.Done(); guard++ {
		if guard > 500 {
			t.Fatal("牌局停不下来")
		}
		player := hand.Turn()
		snap := snapshotFor(events, player)
		if snap == nil {
			t.Fatalf("轮到 %s 却没给他快照", player)
		}
		events = append(events, hand.Apply(player, pickLegal(r, snap))...)
	}
	return Of("TEST01", seed, blinds, names[button], before, time.Now(), hand.Result())
}

func snapshotFor(events []poker.Event, player string) *poker.Snapshot {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Type == poker.EventYourTurn && events[i].Player == player {
			return events[i].Snapshot
		}
	}
	return nil
}

func pickLegal(r *rand.Rand, snap *poker.Snapshot) poker.Action {
	l := snap.Legal[r.IntN(len(snap.Legal))]
	switch l.Action {
	case "check":
		return poker.Action{Kind: poker.Check}
	case "call":
		return poker.Action{Kind: poker.Call}
	case "allin":
		return poker.Action{Kind: poker.AllIn}
	case "bet":
		amount := l.Min
		if l.Max > l.Min {
			amount += r.IntN(l.Max - l.Min + 1)
		}
		return poker.Action{Kind: poker.BetTo, Amount: amount}
	default:
		return poker.Action{Kind: poker.Fold}
	}
}

// TestVerifyReplaysRealHands 是这一刀的正题：记下来的东西必须真的还原得回去。
//
// 没有这条，历史就只是一堆看着像那么回事的 JSON——等到真要复现某个 bug 的时候
// 才发现记漏了一个字段，那份攒了几万手的历史就已经白攒了。
func TestVerifyReplaysRealHands(t *testing.T) {
	configs := [][]int{
		{200, 200},
		{200, 150, 80},
		{200, 200, 200, 200},
		{50, 200, 35, 500}, // 筹码悬殊，专门制造边池
		{7, 200, 13},       // 连盲注都交不满的小筹码
	}
	for ci, stacks := range configs {
		for seed := uint64(1); seed <= 40; seed++ {
			r := rand.New(rand.NewPCG(seed, uint64(ci)+900))
			rec := playHand(t, seed, stacks, int(seed)%len(stacks), r)
			if err := Verify(rec); err != nil {
				t.Fatalf("组 %d seed %d：记录还原不回去：%v", ci, seed, err)
			}
		}
	}
}

// TestVerifyCatchesTampering：记录被改过就得报出来。
//
// 一个什么都说「对」的校验器比没有校验器更糟——它会让人以为历史是可信的。
func TestVerifyCatchesTampering(t *testing.T) {
	base := playHand(t, 12345, []int{200, 150, 80}, 0, rand.New(rand.NewPCG(1, 2)))
	if err := Verify(base); err != nil {
		t.Fatalf("原始记录就该是对的：%v", err)
	}

	cases := []struct {
		name   string
		break_ func(*Record)
	}{
		{"种子被改", func(r *Record) { r.Seed++ }},
		{"庄家位被改", func(r *Record) { r.Button = r.Seats[1].Player }},
		{"有人的初始筹码被改", func(r *Record) { r.Seats[0].Stack += 10 }},
		{"结束筹码被改", func(r *Record) {
			for p := range r.Stacks {
				r.Stacks[p]++
				break
			}
		}},
		{"底池被改", func(r *Record) { r.Pot++ }},
		{"动作序列被截断", func(r *Record) {
			if len(r.Actions) > 0 {
				r.Actions = r.Actions[:len(r.Actions)-1]
			}
		}},
		{"底牌被改", func(r *Record) {
			for p := range r.Hole {
				r.Hole[p] = poker.MustParseCards("2c 3d")
				break
			}
		}},
		{"座位少了一个", func(r *Record) { r.Seats = r.Seats[:1] }},
		{"庄家不在座位表里", func(r *Record) { r.Button = "查无此人" }},
		{"盲注写坏了", func(r *Record) { r.Blinds = "什么都不是" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := clone(t, base)
			c.break_(&bad)
			if err := Verify(bad); err == nil {
				t.Fatal("被改过的记录居然通过了校验")
			}
		})
	}
}

// clone 深拷一份记录，免得各个子测试互相污染。
func clone(t *testing.T, r Record) Record {
	t.Helper()
	out := r
	out.Seats = append([]Seat(nil), r.Seats...)
	out.Actions = append([]poker.ActionRecord(nil), r.Actions...)
	out.Community = append([]poker.Card(nil), r.Community...)
	out.Winners = append([]string(nil), r.Winners...)
	out.Hole = make(map[string][]poker.Card, len(r.Hole))
	for k, v := range r.Hole {
		out.Hole[k] = append([]poker.Card(nil), v...)
	}
	out.Stacks = make(map[string]int, len(r.Stacks))
	for k, v := range r.Stacks {
		out.Stacks[k] = v
	}
	return out
}

// TestWriterRoundTrip：写进去的能原样读回来。
func TestWriterRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "hands.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("开不了历史文件: %v", err)
	}

	want := make([]Record, 0, 5)
	for seed := uint64(1); seed <= 5; seed++ {
		rec := playHand(t, seed, []int{200, 150}, 0, rand.New(rand.NewPCG(seed, 5)))
		rec.Hand = int(seed)
		want = append(want, rec)
		w.Append(rec)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("关不掉历史文件: %v", err)
	}
	if n := w.Dropped() + w.Failed(); n != 0 {
		t.Fatalf("不该有丢弃或失败，得到 %d", n)
	}

	var got []Record
	if err := Scan(path, func(line int, r Record) error {
		if r.Hand != line {
			t.Fatalf("第 %d 行记的是第 %d 手，顺序乱了", line, r.Hand)
		}
		got = append(got, r)
		return nil
	}); err != nil {
		t.Fatalf("读不回来: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("写了 %d 条，读回 %d 条", len(want), len(got))
	}
	for i := range got {
		if err := Verify(got[i]); err != nil {
			t.Fatalf("第 %d 条读回来之后还原不回去：%v", i+1, err)
		}
		if got[i].Seed != want[i].Seed || got[i].Pot != want[i].Pot {
			t.Fatalf("第 %d 条读回来的内容对不上", i+1)
		}
	}
}

// TestWriterAppendsInsteadOfTruncating：历史是只追加的（ADR-0008）。
//
// 重开一次就把上一场清空的话，这个文件就不是事实日志，而是一份随时会消失的临时文件。
func TestWriterAppendsInsteadOfTruncating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hands.jsonl")
	rec := playHand(t, 7, []int{200, 200}, 0, rand.New(rand.NewPCG(7, 7)))

	for round := 0; round < 3; round++ {
		w, err := NewWriter(path)
		if err != nil {
			t.Fatalf("第 %d 轮开不了文件: %v", round, err)
		}
		w.Append(rec)
		if err := w.Close(); err != nil {
			t.Fatalf("第 %d 轮关不掉: %v", round, err)
		}
	}

	count := 0
	if err := Scan(path, func(int, Record) error { count++; return nil }); err != nil {
		t.Fatalf("读不回来: %v", err)
	}
	if count != 3 {
		t.Fatalf("开关三次该攒下 3 条记录，得到 %d 条", count)
	}
}

// TestWriterDropsInsteadOfBlocking：写不动的时候丢记录，绝不卡住调用方。
//
// 牌桌是正在发生的事，历史是它的副产物。让一次磁盘故障把牌局卡死，
// 是拿主要的东西去赔次要的（ADR-0016）。
func TestWriterDropsInsteadOfBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hands.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatalf("开不了历史文件: %v", err)
	}
	defer w.Close()

	rec := playHand(t, 3, []int{200, 200}, 0, rand.New(rand.NewPCG(3, 3)))
	// 灌远超队列容量的量。关键是这个循环得能跑完——卡住的话测试会直接超时。
	done := make(chan struct{})
	go func() {
		for i := 0; i < queueSize*20; i++ {
			w.Append(rec)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Append 把调用方卡住了")
	}
}

// TestDefaultPathKeepsRunsApart：同一个 Table Code 反复开桌，历史不该混在一个文件里。
func TestDefaultPathKeepsRunsApart(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 9, 18, 11, 22, 33, 0, time.UTC)
	got := DefaultPath(dir, "ABC234", at)
	want := filepath.Join(dir, "history", "ABC234-20260918-112233.jsonl")
	if got != want {
		t.Fatalf("路径是 %s，想要 %s", got, want)
	}
	later := DefaultPath(dir, "ABC234", at.Add(time.Second))
	if got == later {
		t.Fatal("两次开桌该落在不同的文件里")
	}
}

// TestScanRejectsGarbage：文件坏了要报错，不能装作读完了。
func TestScanRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.jsonl")
	if err := os.WriteFile(path, []byte("{\"hand\":1}\n这不是 JSON\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Scan(path, func(int, Record) error { return nil }); err == nil {
		t.Fatal("坏掉的文件该报错")
	}
}

// TestHistoryFileKeepsLetterCards：落盘的牌是机器形式（"As"），不是终端上那个 A♠。
//
// 历史是只追加的事实日志，格式一变，已经存下来的文件就再也读不回去了。
// 花色符号只该活在给人看的那一层（textui），这条测试守的就是那道界线。
func TestHistoryFileKeepsLetterCards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hands.jsonl")
	w, err := NewWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	w.Append(playHand(t, 31, []int{200, 200}, 0, rand.New(rand.NewPCG(31, 1))))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, sym := range []string{"♠", "♥", "♦", "♣"} {
		if strings.Contains(string(raw), sym) {
			t.Fatalf("花色符号漏进历史文件了：%s", sym)
		}
	}
	// 而复盘出来的那份是给人看的，符号该在。
	var rec Record
	if err := Scan(path, func(_ int, r Record) error { rec = r; return nil }); err != nil {
		t.Fatal(err)
	}
	if out := Format(rec); !strings.ContainsAny(out, "♠♥♦♣") {
		t.Fatalf("复盘里该用花色符号：\n%s", out)
	}
}
