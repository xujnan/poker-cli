package poker

import (
	"strings"
	"testing"
)

// 全拼和单字母必须解析成同一个 Action。写成表格是为了让「加了别名却忘了让它跟
// 全拼走同一条路」这种错当场就露出来。
func TestShortcutsMeanTheSameThing(t *testing.T) {
	for _, c := range []struct {
		short, full string
	}{
		{"f", "fold"},
		{"a", "allin"},
		{"b 100", "bet 100"},
	} {
		got, err := ParseAction(c.short)
		if err != nil {
			t.Fatalf("ParseAction(%q)：%v", c.short, err)
		}
		want, err := ParseAction(c.full)
		if err != nil {
			t.Fatalf("ParseAction(%q)：%v", c.full, err)
		}
		if got != want {
			t.Errorf("%q 解析成 %v，%q 解析成 %v，两者应当一致", c.short, got, c.full, want)
		}
	}
}

// check 和 call 一个单字母都没有，而且是明着拒绝，不是撞进「不认识的动作」里。
//
// 这两个是命令表上唯一一对看着像的动作，都以 c 开头，而认错的代价是本该过牌
// 却跟了注。给其中一个而另一个没有更糟：那种规则得先想一下才敢敲。
// 所以 c 和 k 都要回一句说得清的话——这跟 sitout/sitin 不给 s 是同一条规矩。
func TestLookalikeActionsGetNoShortcut(t *testing.T) {
	for _, s := range []string{"c", "k"} {
		_, err := ParseAction(s)
		if err == nil {
			t.Fatalf("%q 不该被认成一个动作", s)
		}
		if !strings.Contains(err.Error(), "check") || !strings.Contains(err.Error(), "call") {
			t.Errorf("%q 的错误话没说清该写什么：%v", s, err)
		}
		if strings.Contains(err.Error(), "不认识的动作") {
			t.Errorf("%q 是故意不给的，不是没见过的，话要说得不一样：%v", s, err)
		}
	}
	// 写全了当然认。
	if a, err := ParseAction("check"); err != nil || a.Kind != Check {
		t.Errorf("check 该是过牌，得到 %v（err=%v）", a, err)
	}
	if a, err := ParseAction("call"); err != nil || a.Kind != Call {
		t.Errorf("call 该是跟注，得到 %v（err=%v）", a, err)
	}
}

func TestShortcutsObeyTheSameArgumentRules(t *testing.T) {
	if _, err := ParseAction("f 100"); err == nil {
		t.Error("f 不带参数，f 100 应当报错")
	}
	if _, err := ParseAction("b"); err == nil {
		t.Error("b 后面少了数额，应当报错")
	}
	if _, err := ParseAction("b 0"); err == nil {
		t.Error("数额必须是正数")
	}
}

// 没进表的字母不能因为「看起来像」就被认下来：认错一个字母的代价是弃掉一手牌。
func TestUnlistedLettersStayUnknown(t *testing.T) {
	for _, s := range []string{"F", "ca", "fo", "r", "x", "al", "ch"} {
		if _, err := ParseAction(s); err == nil {
			t.Errorf("%q 不该被认出来", s)
		}
	}
}
