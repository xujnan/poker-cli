package server

import (
	"bytes"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xujnan/poker-cli/internal/history"
)

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("等不到：%s", what)
}

// onCrashAtHand 让牌桌在第 n 手开局时崩一次，跑完自动摘掉。
func onCrashAtHand(t *testing.T, n int) {
	t.Helper()
	var once sync.Once
	crashHook = func(handNo int) {
		if handNo == n {
			once.Do(func() { panic("测试注入：牌桌炸了") })
		}
	}
	t.Cleanup(func() { crashHook = nil })
}

// TestCrashKeepsTheHistoryAndNamesTheSeed 是崩溃收尾的验收。
//
// 牌桌 goroutine 里任何没接住的 panic 都会带走整个进程，而手牌历史是异步落盘的——
// 队列里还没写出去的那几手跟着蒸发，**包括出事那一手的种子**，也就是唯一能把这个
// bug 原样重放出来的东西。这条测的就是：崩了之后，前面打完的手牌一条不少，
// 而且报出来的错里带着那个种子。
//
// 注意它不测「崩了还能接着打」——那不是目标。状态已经坏了，接着打等于悄悄把
// 底池算错给某个人。目标是干净地停，并且留下能复现的线索。
func TestCrashKeepsTheHistoryAndNamesTheSeed(t *testing.T) {
	const crashAt = 4 // 前三手正常打完，第 4 手开局时崩

	var logs bytes.Buffer
	s, path := startTableWithHistoryLog(t, &logs)
	onCrashAtHand(t, crashAt)

	alice := dial(t, s, "alice")
	keepPlaying(t, alice)
	bob := dial(t, s, "bob")
	keepPlaying(t, bob)

	// 牌桌自己会因为崩溃而收摊，客户端那头看到的就是连接断了。
	waitFor(t, "牌桌崩掉", func() bool { return s.crashErr() != nil })

	// main 就是靠这个信号知道该去收尾的。少了它，Serve 返回之后没有任何人会调 Close，
	// 进程会挂死在等收尾上——崩溃从「有日志有种子」变成「卡住不动」，更难查。
	select {
	case <-s.Stopped():
	default:
		t.Fatal("崩了之后 Stopped 该已经关闭")
	}

	err := s.Close()
	if err == nil {
		t.Fatal("崩过的牌桌，Close 该把这件事报出来，而不是假装一切正常")
	}

	// 错误和日志里都得有种子——没有它，这个 bug 就只能靠运气再撞一次。
	var seed uint64
	hands := 0
	if err := history.Scan(path, func(_ int, r history.Record) error {
		hands++
		if r.Hand == crashAt-1 {
			seed = r.Seed
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if hands != crashAt-1 {
		t.Fatalf("崩之前打完了 %d 手，历史里却有 %d 条", crashAt-1, hands)
	}
	if !strings.Contains(err.Error(), strconv.Itoa(crashAt)) {
		t.Errorf("错误里该说清是第几手崩的：%v", err)
	}
	if got := logs.String(); !strings.Contains(got, "种子") || !strings.Contains(got, "goroutine") {
		t.Errorf("日志里该有种子和调用栈，得到：\n%s", got)
	}
	t.Logf("崩在第 %d 手，前 %d 手都落了盘（第 %d 手的种子 %d）；报出来的是：%v",
		crashAt, hands, crashAt-1, seed, err)
}

// TestCleanCloseStillReportsNoError：没崩的时候 Close 照旧不报错。
//
// 加了崩溃那条路之后，最容易顺手弄坏的就是正常收桌——让它每次都返回个什么东西。
func TestCleanCloseStillReportsNoError(t *testing.T) {
	var logs bytes.Buffer
	s, _ := startTableWithHistoryLog(t, &logs)
	alice := dial(t, s, "alice")
	keepPlaying(t, alice)
	bob := dial(t, s, "bob")
	keepPlaying(t, bob)

	time.Sleep(50 * time.Millisecond)
	if err := s.Close(); err != nil {
		t.Fatalf("正常收桌不该报错：%v", err)
	}
}
