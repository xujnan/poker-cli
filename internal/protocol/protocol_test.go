package protocol

import "testing"

// 终端里的单字母快捷输入不许渗到线路上。
//
// 人敲 "f" 是 internal/poker.ParseAction 的事，它把 f 翻成 Fold，发出去的仍然是
// {"type":"fold"}。线路这一侧必须只认一种拼法——写 agent 的人照着 your_turn 里
// 的动作名发命令就该一次成功，不必知道还有第二种写法，也不必处理第二种写法
// （ADR-0005 修订）。
func TestWireHasExactlyOneSpellingPerAction(t *testing.T) {
	for _, alias := range []string{"f", "b", "a"} {
		cmd := Command{Type: CommandType(alias), Amount: 100}
		if cmd.IsAction() {
			t.Errorf("%q 不该被线路当成动作", alias)
		}
		if _, err := cmd.Action(); err == nil {
			t.Errorf("%q 不该能翻译成动作", alias)
		}
	}
}
