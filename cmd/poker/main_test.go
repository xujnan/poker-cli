package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xujnan/poker-cli/internal/history"
)

// 这个文件跑的是真的编译出来的二进制，起真的进程。
//
// 它测的是别处结构上看不到的东西：进程什么时候退出、退出之前有没有把事情做完。
// 「最后一手的历史没落盘」那个 bug 就活在这里——包内的测试全绿，
// 因为它们都是自己同步调用 Close 的，而真实的 main 是在另一个 goroutine 里关的。
var pokerBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "poker-bin")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)

	pokerBin = filepath.Join(dir, "poker")
	build := exec.Command("go", "build", "-o", pokerBin, ".")
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		panic("编译不出来: " + err.Error())
	}
	os.Exit(m.Run())
}

// TestServeStopsAfterMaxHandsAndFlushesHistory 是那个 bug 的回归测试：
// 服务端自己收桌之后，每一手牌都得已经落盘。
//
// Serve 在监听器一关就返回了，而关监听器只是 Close 的第一步。main 不等 Close 做完
// 就返回的话，进程会赶在最后一手落盘之前退出——没有报错，只是历史里少一条。
func TestServeStopsAfterMaxHandsAndFlushesHistory(t *testing.T) {
	const hands = 6
	dir := t.TempDir()

	serve := exec.Command(pokerBin, "serve",
		"--dir", dir, "--code", "TSTAAA", "--blinds", "1/2", "--seed", "7",
		"--hand-delay", "5ms", "--hands", strconv.Itoa(hands), "--timeout", "200ms")
	serve.Stderr = os.Stderr
	if err := serve.Start(); err != nil {
		t.Fatalf("起不来服务端: %v", err)
	}
	defer func() { _ = serve.Process.Kill() }()

	// 等 socket 出现再放机器人进去。
	sock := filepath.Join(dir, "TSTAAA.sock")
	waitFor(t, func() bool { _, err := os.Stat(sock); return err == nil })

	var bots []*exec.Cmd
	for _, name := range []string{"bot1", "bot2"} {
		bot := exec.Command(pokerBin, "bot", "TSTAAA", "--dir", dir, "--as", name, "--rebuy")
		if err := bot.Start(); err != nil {
			t.Fatalf("起不来机器人 %s: %v", name, err)
		}
		bots = append(bots, bot)
	}
	defer func() {
		for _, b := range bots {
			_ = b.Process.Kill()
		}
	}()

	// 服务端该自己退出，不用谁去杀它。
	done := make(chan error, 1)
	go func() { done <- serve.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("服务端非正常退出: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("打满手数之后服务端没有自己退出")
	}

	// 手数要不多不少，而且每一手都能重放回去。
	path := findHistory(t, dir)
	n := 0
	if err := history.Scan(path, func(_ int, r history.Record) error {
		n++
		if r.Hand != n {
			t.Fatalf("第 %d 条记的是第 %d 手，手数该是连号的", n, r.Hand)
		}
		if err := history.Verify(r); err != nil {
			t.Fatalf("第 %d 手还原不回去：%v", r.Hand, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("读历史失败: %v", err)
	}
	if n != hands {
		t.Fatalf("打了 %d 手，历史里却有 %d 条——最后一手多半是在落盘前就退出了", hands, n)
	}
}

// TestVerifyAndHistorySubcommands：两个只读子命令在真二进制上跑得通。
func TestVerifyAndHistorySubcommands(t *testing.T) {
	dir := t.TempDir()
	serve := exec.Command(pokerBin, "serve",
		"--dir", dir, "--code", "TSTBBB", "--blinds", "1/2", "--seed", "3",
		"--hand-delay", "5ms", "--hands", "4", "--timeout", "200ms")
	if err := serve.Start(); err != nil {
		t.Fatalf("起不来服务端: %v", err)
	}
	defer func() { _ = serve.Process.Kill() }()

	waitFor(t, func() bool { _, err := os.Stat(filepath.Join(dir, "TSTBBB.sock")); return err == nil })
	for _, name := range []string{"bot1", "bot2"} {
		bot := exec.Command(pokerBin, "bot", "TSTBBB", "--dir", dir, "--as", name, "--rebuy")
		if err := bot.Start(); err != nil {
			t.Fatalf("起不来机器人: %v", err)
		}
		defer func() { _ = bot.Process.Kill() }()
	}
	if err := serve.Wait(); err != nil {
		t.Fatalf("服务端非正常退出: %v", err)
	}

	path := findHistory(t, dir)

	out, err := exec.Command(pokerBin, "verify", path).CombinedOutput()
	if err != nil {
		t.Fatalf("verify 失败: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "0 手对不上") {
		t.Fatalf("verify 该说全对，得到：%s", out)
	}

	out, err = exec.Command(pokerBin, "history", path, "--stats").CombinedOutput()
	if err != nil {
		t.Fatalf("history --stats 失败: %v\n%s", err, out)
	}
	for _, want := range []string{"共 4 手牌", "bot1", "bot2", "净筹码"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("战绩表里没有 %q：\n%s", want, out)
		}
	}

	out, err = exec.Command(pokerBin, "history", path, "--hand", "1").CombinedOutput()
	if err != nil {
		t.Fatalf("history --hand 失败: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "第 1 手") || strings.Contains(string(out), "第 2 手") {
		t.Fatalf("--hand 1 该只出第 1 手：\n%s", out)
	}
}

// TestVerifyFailsOnTamperedHistory：校验不过的时候要非零退出，脚本才拦得住。
func TestVerifyFailsOnTamperedHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(path, []byte(`{"hand":1,"seed":1,"blinds":"1/2","button":"a","seats":[{"player":"a","stack":100},{"player":"b","stack":100}],"stacks_after":{"a":100,"b":100}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(pokerBin, "verify", path).CombinedOutput()
	if err == nil {
		t.Fatalf("对不上的历史该非零退出：\n%s", out)
	}
}

// TestUsageOnBadInput：参数不对时说人话并非零退出。
func TestUsageOnBadInput(t *testing.T) {
	cases := [][]string{
		{},
		{"不认识的子命令"},
		{"join"},           // 少了 CODE
		{"join", "ABC234"}, // 少了 --as
		{"verify"},         // 少了文件
		{"history"},        // 少了文件
	}
	for _, args := range cases {
		out, err := exec.Command(pokerBin, args...).CombinedOutput()
		if err == nil {
			t.Fatalf("%v 该失败，却成功了：\n%s", args, out)
		}
		if len(out) == 0 {
			t.Fatalf("%v 失败了却什么都没说", args)
		}
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

func findHistory(t *testing.T, dir string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "history", "*.jsonl"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("该正好有一个历史文件，得到 %v（%v）", matches, err)
	}
	return matches[0]
}
