package server

import (
	"io"
	"math/rand/v2"
	"path/filepath"
	"testing"
	"time"

	"github.com/xujnan/poker-cli/internal/client"
	"github.com/xujnan/poker-cli/internal/history"
	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/protocol"
)

// startTableWithHistory 开一张会写历史的牌桌，并把历史文件路径一并给出。
func startTableWithHistory(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := New(Options{
		Dir:           dir,
		Rand:          rand.New(rand.NewPCG(20240918, 5)),
		HandDelay:     5 * time.Millisecond,
		ActionTimeout: 50 * time.Millisecond,
		Blinds:        poker.Blinds{Small: 1, Big: 2},
		Buyin:         testBuyin,
		Log:           io.Discard,
	})
	if err != nil {
		t.Fatalf("开桌失败: %v", err)
	}
	path := filepath.Join(dir, "hands.jsonl")
	if err := s.UseHistory(path); err != nil {
		t.Fatalf("开不了历史文件: %v", err)
	}
	go func() {
		if err := s.Serve(); err != nil {
			t.Errorf("Serve 出错: %v", err)
		}
	}()
	return s, path
}

// TestHistoryIsWrittenAndReplayable 是这一刀的正题：
// 真牌桌上打出来的牌，写下来的记录必须能一手一手重放回去。
//
// 历史存在的理由是「agent 的训练评测数据和 bug 复现」（ADR-0008）。
// 记了一堆还原不回去的 JSON，等于什么都没记——而且要等到真去复现 bug 的那天才发现。
func TestHistoryIsWrittenAndReplayable(t *testing.T) {
	s, path := startTableWithHistory(t)

	const wantHands = 6
	alice := dial(t, s, "alice")
	aliceEvents := autoPlay(t, alice, wantHands)
	bob := dial(t, s, "bob")
	autoPlay(t, bob, wantHands)
	carol := dial(t, s, "carol")
	autoPlay(t, carol, wantHands)

	waitEvents(t, aliceEvents)
	// 关掉牌桌，让写 goroutine 把队里剩下的冲出去。
	if err := s.Close(); err != nil {
		t.Fatalf("关桌失败: %v", err)
	}

	var records []history.Record
	if err := history.Scan(path, func(line int, r history.Record) error {
		if err := history.Verify(r); err != nil {
			t.Fatalf("第 %d 行（第 %d 手，种子 %d）还原不回去：%v", line, r.Hand, r.Seed, err)
		}
		records = append(records, r)
		return nil
	}); err != nil {
		t.Fatalf("读历史失败: %v", err)
	}

	if len(records) < wantHands {
		t.Fatalf("打了至少 %d 手，历史里却只有 %d 条", wantHands, len(records))
	}
	for i, r := range records {
		if r.Hand != i+1 {
			t.Fatalf("第 %d 条记的是第 %d 手，手数该是连号的", i+1, r.Hand)
		}
		if r.Seed == 0 {
			t.Fatalf("第 %d 手没记种子，这条记录就不自足了", r.Hand)
		}
		if r.Table != s.Code() {
			t.Fatalf("第 %d 手记的牌桌是 %q，该是 %q", r.Hand, r.Table, s.Code())
		}
		// 历史是上帝视角：每个参与这手牌的人的底牌都得在（CONTEXT 里 Hand History 的定义）。
		for _, seat := range r.Seats {
			if len(r.Hole[seat.Player]) != 2 {
				t.Fatalf("第 %d 手没记 %s 的底牌", r.Hand, seat.Player)
			}
		}
		if len(r.Actions) == 0 {
			t.Fatalf("第 %d 手一个动作都没记", r.Hand)
		}
	}
}

// TestEachHandHasItsOwnSeed：每手牌的种子各不相同，而且单独就能重放（ADR-0016）。
//
// 整张桌共用一条随机流的话，「第 37 手的种子」根本不存在——要复现它，
// 就得连同前 36 手的座位顺序和每个人的每个动作原样重来。
func TestEachHandHasItsOwnSeed(t *testing.T) {
	s, path := startTableWithHistory(t)

	const wantHands = 5
	alice := dial(t, s, "alice")
	aliceEvents := autoPlay(t, alice, wantHands)
	bob := dial(t, s, "bob")
	autoPlay(t, bob, wantHands)
	waitEvents(t, aliceEvents)
	if err := s.Close(); err != nil {
		t.Fatalf("关桌失败: %v", err)
	}

	seen := map[uint64]int{}
	if err := history.Scan(path, func(_ int, r history.Record) error {
		if prev, dup := seen[r.Seed]; dup {
			t.Fatalf("第 %d 手和第 %d 手用了同一个种子 %d", prev, r.Hand, r.Seed)
		}
		seen[r.Seed] = r.Hand
		return nil
	}); err != nil {
		t.Fatalf("读历史失败: %v", err)
	}
	if len(seen) < wantHands {
		t.Fatalf("该有至少 %d 个种子，得到 %d 个", wantHands, len(seen))
	}
}

// TestHistoryRecordsForcedActions：代打的那一下要标出来。
//
// 「他弃了」和「他没说话、系统替他弃了」在事后分析 agent 行为时是两回事，
// 混在一起的话，一个卡住的 agent 会看起来像个打得很紧的 agent。
func TestHistoryRecordsForcedActions(t *testing.T) {
	s, path := startTableWithHistory(t)

	alice := dial(t, s, "alice")
	bob := dial(t, s, "bob")

	// alice 只读不说话，让行动超时替她做决定。
	go func() {
		for {
			if _, _, err := alice.Next(); err != nil {
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
			if ev.Type == poker.EventAction && ev.Player == "alice" && ev.Forced {
				got <- events
				return
			}
		}
	}()
	waitEvents(t, got)
	if err := s.Close(); err != nil {
		t.Fatalf("关桌失败: %v", err)
	}

	var forced int
	if err := history.Scan(path, func(_ int, r history.Record) error {
		for _, a := range r.Actions {
			if a.Forced {
				forced++
			}
		}
		// 带着代打动作的记录同样得能重放。
		if err := history.Verify(r); err != nil {
			t.Fatalf("第 %d 手还原不回去：%v", r.Hand, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("读历史失败: %v", err)
	}
	if forced == 0 {
		t.Fatal("历史里一个代打动作都没标出来")
	}
}
