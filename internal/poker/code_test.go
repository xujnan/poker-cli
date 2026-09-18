package poker

import (
	"math/rand/v2"
	"strings"
	"testing"
)

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

// TestValidTableCodeRejectsJunk：Table Code 会直接拼进 socket 路径（ADR-0013），
// 所以这道校验同时是那次路径拼接的安全带。
func TestValidTableCodeRejectsJunk(t *testing.T) {
	bad := []string{
		"",
		"ABC12",   // 短了
		"ABC1234", // 长了
		"abcdef",  // 小写
		"ABC1DE",  // 含 1
		"ABC0DE",  // 含 0
		"ABCIDE",  // 含 I
		"ABCODE",  // 含 O
		"../etc",  // 路径穿越
		"AB/DEF",  // 路径分隔符
		"AB CDE",  // 空格
		"ABCDE\n", // 换行
	}
	for _, c := range bad {
		if ValidTableCode(c) {
			t.Fatalf("%q 不该是合法的 Table Code", c)
		}
	}
}
