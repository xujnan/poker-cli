package poker

import "testing"

// 全拼和单字母必须解析成同一个 Action。写成表格是为了让「加了别名却忘了让它跟
// 全拼走同一条路」这种错当场就露出来。
func TestShortcutsMeanTheSameThing(t *testing.T) {
	for _, c := range []struct {
		short, full string
	}{
		{"f", "fold"},
		{"k", "check"},
		{"c", "call"},
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

// c 是 call、k 是 check。这是牌桌上的习惯，不是首字母——写反了没人会报错，
// 只会在某一手牌里让人本该过牌却跟了注，所以钉死。
func TestCallTakesTheCLetter(t *testing.T) {
	if a, err := ParseAction("c"); err != nil || a.Kind != Call {
		t.Errorf("c 应当是 call，得到 %v（err=%v）", a, err)
	}
	if a, err := ParseAction("k"); err != nil || a.Kind != Check {
		t.Errorf("k 应当是 check，得到 %v（err=%v）", a, err)
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
	for _, s := range []string{"F", "ca", "fo", "r", "x", "al"} {
		if _, err := ParseAction(s); err == nil {
			t.Errorf("%q 不该被认出来", s)
		}
	}
}
