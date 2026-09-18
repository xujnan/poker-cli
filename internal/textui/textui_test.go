package textui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xujnan/poker-cli/internal/poker"
)

func TestCardShowsSuitSymbols(t *testing.T) {
	cases := map[string]string{
		"As": "A♠",
		"Kh": "K♥",
		"Td": "T♦",
		"2c": "2♣",
	}
	for in, want := range cases {
		c, err := poker.ParseCard(in)
		if err != nil {
			t.Fatalf("解析 %q 失败: %v", in, err)
		}
		if got := Card(c); got != want {
			t.Fatalf("%q 该显示成 %q，得到 %q", in, want, got)
		}
	}
}

func TestCardsJoinsAndMarksEmpty(t *testing.T) {
	if got := Cards(poker.MustParseCards("As Kh")); got != "A♠ K♥" {
		t.Fatalf("得到 %q", got)
	}
	// 空的时候给个短横，免得那一栏看起来像漏了。
	if got := Cards(nil); got != "-" {
		t.Fatalf("空牌该显示成 -，得到 %q", got)
	}
}

// TestWireFormatStaysLetters 是这次改动最要紧的一条守卫。
//
// 花色符号只能出现在给人看的那一层。牌的规范文本形式（"As"）同时是 JSONL 的
// wire 格式和手牌历史的存储格式：改了它，照着 docs/agent.md 写的 agent 会当场
// 解析失败，而所有已经存下来的历史文件再也 verify 不过——两样都不会有编译错误，
// 只会在别人用的时候炸。
func TestWireFormatStaysLetters(t *testing.T) {
	c, err := poker.ParseCard("As")
	if err != nil {
		t.Fatal(err)
	}

	if got := c.String(); got != "As" {
		t.Fatalf("牌的规范文本形式必须还是 %q，得到 %q", "As", got)
	}

	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `"As"` {
		t.Fatalf("JSON 里必须还是 %q，得到 %s", "As", b)
	}
	for _, sym := range suitSymbols {
		if strings.Contains(string(b), sym) {
			t.Fatalf("花色符号漏进 wire 格式了：%s", b)
		}
	}

	// 反过来也得成立：线路上传回来的还是解析得动。
	var back poker.Card
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("解析不回来: %v", err)
	}
	if back != c {
		t.Fatalf("来回一趟变了：%v → %v", c, back)
	}
}

func TestWidthCountsColumnsNotBytes(t *testing.T) {
	cases := map[string]int{
		"bot1":  4,
		"我":     2, // 三个字节，两列
		"净筹码":   6,
		"A♠":    2, // 花色符号按一列算
		"A♠ K♥": 5,
		"":      0,
		"玩家abc": 7,
	}
	for in, want := range cases {
		if got := Width(in); got != want {
			t.Fatalf("%q 该占 %d 列，得到 %d", in, want, got)
		}
	}
}

// TestPadAlignsByColumns：中文名字和 ASCII 名字要对得齐。
//
// 用 %-10s 的话不会——那个按字节补，「我」是三个字节两列宽，于是永远差一格。
func TestPadAlignsByColumns(t *testing.T) {
	rows := []string{"bot1", "我", "alice", "机器人"}
	for _, r := range rows {
		got := Pad(r, 10)
		if Width(got) != 10 {
			t.Fatalf("%q 补完该是 10 列，得到 %d 列（%q）", r, Width(got), got)
		}
	}
	// 太长的不截断——宁可这一行歪掉，也不能把名字切一半。
	long := "这是一个很长很长的名字"
	if got := Pad(long, 4); got != long {
		t.Fatalf("超宽的不该被动，得到 %q", got)
	}
}

func TestPadLeftRightAligns(t *testing.T) {
	if got := PadLeft("12", 6); got != "    12" {
		t.Fatalf("得到 %q", got)
	}
	if got := PadLeft("净筹码", 8); Width(got) != 8 || !strings.HasSuffix(got, "净筹码") {
		t.Fatalf("得到 %q（%d 列）", got, Width(got))
	}
}
