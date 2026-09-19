package poker

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// TestNewTableCodeIsReadable：自己生成的码要念得清楚。
//
// 生成时避开 I/O/0/1 是对自己的要求——这个码的用处就是口头报给隔壁桌的人，
// 「零还是欧」来回问两遍就把这点方便全赔掉了。校验不跟着这条走，见下一条。
func TestNewTableCodeIsReadable(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 200; i++ {
		code := NewTableCode(r)
		if len(code) != TableCodeLength {
			t.Fatalf("%q 的长度是 %d", code, len(code))
		}
		if strings.ContainsAny(code, "IO01") {
			t.Fatalf("%q 里有读起来会混的字符", code)
		}
		if !ValidTableCode(code) {
			t.Fatalf("自己生成的 %q 没通过自己的校验", code)
		}
	}
}

// TestValidTableCodeTakesAnyLetterOrDigit：别人递过来的码，字母数字一律收。
//
// 生成时挑字符是对自己的要求，不是拦别人的理由。`--code POKER0` 是人自己挑的名字，
// 一眼就懂，没道理被自己的程序拒之门外——这条以前是错的，而且专挑手写码的时候咬人。
func TestValidTableCodeTakesAnyLetterOrDigit(t *testing.T) {
	ok := []string{
		"POKER0", // 含 0
		"POKERO", // 含 O
		"TABLE1", // 含 1
		"ABCIDE", // 含 I
		"IO01IO", // 四个全齐
		"000000",
		"ZZZZZZ",
		"A1B2C3",
	}
	for _, c := range ok {
		if !ValidTableCode(c) {
			t.Fatalf("%q 该是合法的 Table Code", c)
		}
	}
}

// TestValidTableCodeRejectsJunk：Table Code 会直接拼进 socket 路径（ADR-0013），
// 所以这道校验同时是那次路径拼接的安全带。
//
// 放开字符集的时候最容易顺手放开的就是这一条，而它松了就不是「码不好念」的问题了。
func TestValidTableCodeRejectsJunk(t *testing.T) {
	bad := []string{
		"",
		"ABC12",   // 短了
		"ABC1234", // 长了
		"abcdef",  // 小写：规范化那一步该先转大写，到这里还是小写就是没走那一步
		"../etc",  // 路径穿越
		"AB/DEF",  // 路径分隔符
		"AB\\DEF", // 反斜杠
		"A.CDEF",  // 点
		"AB CDE",  // 空格
		"ABCDE\n", // 换行
		"ABCDE\x00",
		"ABCD-E",
		"牌桌码", // 非 ASCII（长度也不对，但意思在这儿）
	}
	for _, c := range bad {
		if ValidTableCode(c) {
			t.Fatalf("%q 不该是合法的 Table Code", c)
		}
	}
}
