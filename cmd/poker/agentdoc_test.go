package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/xujnan/poker-cli/internal/history"
)

// TestDocumentedPythonAgentCanActuallyPlay 把 docs/agent.md 里那段 Python 抠出来，
// 起真服务端、真对手，让它真打几手牌。
//
// 这条测的是对外的承诺。README 写着「一个 20 行、不依赖任何库、真能和 poker bot
// 对打的 Python agent」——在这条测试之前，这句话的真实性只到我上次手跑那一刻为止。
// 事件里改个字段名、加一条必填的键、把 legal 的形状动一动，那段代码当场解析失败，
// 而仓库里没有任何东西会红：它不是 Go，编译器看不见它。
//
// 关键在于代码是**从文档里抠出来的**，不是抄一份放进测试。抄一份的话，
// 文档和测试会各自演化，而烂掉的恰恰是别人照着抄的那一份。
func TestDocumentedPythonAgentCanActuallyPlay(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("没有 python3")
	}

	src := pythonFromDoc(t, filepath.Join("..", "..", "docs", "agent.md"))
	// 文档里那段是照着 ~/.poker 找 socket 的，所以给它一个自己的 HOME，
	// 服务端就开在那底下。改文档去迁就测试是本末倒置：要测的就是照抄能不能用。
	home := t.TempDir()
	dir := filepath.Join(home, ".poker")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	agent := filepath.Join(t.TempDir(), "agent.py")
	if err := os.WriteFile(agent, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	const code, hands = "DCAGT2", 8
	serve := exec.Command(pokerBin, "serve",
		"--dir", dir, "--code", code, "--blinds", "1/2", "--seed", "11",
		"--hand-delay", "5ms", "--hands", strconv.Itoa(hands), "--timeout", "2s")
	serve.Stderr = os.Stderr
	if err := serve.Start(); err != nil {
		t.Fatalf("起不来服务端: %v", err)
	}
	defer func() { _ = serve.Process.Kill() }()
	waitFor(t, func() bool {
		_, err := os.Stat(filepath.Join(dir, code+".sock"))
		return err == nil
	})

	// 对手是 poker bot，走的是和这个 Python 完全相同的接口（ADR-0010）。
	bot := exec.Command(pokerBin, "bot", code, "--dir", dir, "--as", "bot1", "--rebuy")
	if err := bot.Start(); err != nil {
		t.Fatalf("起不来机器人: %v", err)
	}
	defer func() { _ = bot.Process.Kill() }()

	py := exec.Command(python, agent, code, "agent1")
	py.Env = append(os.Environ(), "HOME="+home)
	var pyErr strings.Builder
	py.Stderr = &pyErr
	if err := py.Start(); err != nil {
		t.Fatalf("起不来 Python agent: %v", err)
	}
	defer func() { _ = py.Process.Kill() }()

	// 服务端打满 --hands 自己收桌，不用谁去杀它。
	if err := serve.Wait(); err != nil {
		t.Fatalf("服务端非正常退出: %v\nPython 那边说：%s", err, pyErr.String())
	}
	// 服务端一关，agent 那条 `for line in conn` 就到头了，它该自己干净退出。
	if err := py.Wait(); err != nil {
		t.Fatalf("Python agent 没能正常收场: %v\n它的 stderr：\n%s", err, pyErr.String())
	}

	// 「没崩」还不够，得证明它真的在打牌：历史里必须有它自己按下的动作。
	// 只断言进程活着的话，一个从头到尾被超时代打的 agent 也能过。
	var own, forced int
	seen := false
	if err := history.Scan(findHistory(t, dir), func(_ int, r history.Record) error {
		if _, ok := r.Hole["agent1"]; ok {
			seen = true
		}
		for _, a := range r.Actions {
			if a.Player != "agent1" {
				continue
			}
			if a.Forced {
				forced++
			} else {
				own++
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("历史里根本没有 agent1，它没坐下")
	}
	if own == 0 {
		t.Fatalf("agent1 一个自己的动作都没有（被代打 %d 次）——文档里那段没在真的做决定", forced)
	}
	if forced > 0 {
		t.Errorf("agent1 有 %d 次是被代打的（自己做了 %d 次）：文档里那段没跟上轮次", forced, own)
	}
	t.Logf("文档里的 agent 打了 %d 手，自己做了 %d 个动作", hands, own)
}

// pythonFromDoc 从 Markdown 里取出第一段 ```python 代码块。
//
// 取第一段而不是全部：文档里只该有一个「完整可跑的 agent」，要是哪天多出第二段，
// 这里取到的仍然是那个完整的。真多出来了，是文档该拆，不是这里该猜。
func pythonFromDoc(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	start := -1
	for i, l := range lines {
		if start < 0 {
			if strings.TrimSpace(l) == "```python" {
				start = i + 1
			}
			continue
		}
		if strings.TrimSpace(l) == "```" {
			src := strings.Join(lines[start:i], "\n")
			if !strings.Contains(src, `"join"`) {
				t.Fatalf("%s 里第一段 Python 看着不像那个 agent（没有 join）", path)
			}
			return src
		}
	}
	t.Fatalf("%s 里找不到 ```python 代码块", path)
	return ""
}
