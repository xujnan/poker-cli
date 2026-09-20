package textui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/creack/pty"

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

// TestWidthHandlesNamesNobodyPlannedFor：名字是 `--as` 传进来的任意字符串。
//
// 下面这四类以前全算错，每一类都让座位表歪一格：自己手写的那张范围表
// 把组合符号当成各占一列，把 ZWJ 拼出来的 emoji 当成好几个字符，
// 还漏掉了扑克牌 emoji 所在的 U+1F0A0 那一块——一个扑克程序漏了它。
func TestWidthHandlesNamesNobodyPlannedFor(t *testing.T) {
	cases := map[string]int{
		"café":                4, // e + 组合尖音符，组合符号不占列
		"ที่":                  1, // 泰文：一个辅音带两个组合符
		"🃏":                    2, // U+1F0CF 扑克牌 emoji
		"👩‍💻":                  2, // ZWJ 序列是一个字素簇
		"김선수":                  6, // 韩文音节
		"\U0001F1E8\U0001F1F3": 2, // 区域指示符拼出来的国旗
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

// withColor 临时打开彩色，跑完还原——别的测试默认是不上色的。
func withColor(t *testing.T) {
	t.Helper()
	colorOn.Store(true)
	t.Cleanup(func() { colorOn.Store(false) })
}

// TestColorIsOffByDefault：默认不上色。
//
// 这条是给管道兜底的：转录、重定向出来的日志、测试的断言里都不该混进转义序列，
// 而它们走的正是默认这条路。
func TestColorIsOffByDefault(t *testing.T) {
	if colorOn.Load() {
		t.Fatal("彩色默认该是关的")
	}
	if got := Card(mustCard(t, "Ah")); got != "A♥" {
		t.Fatalf("不上色时该是干净的，得到 %q", got)
	}
}

// TestRedForHeartsAndDiamonds：只有红桃方块上色，黑桃梅花保持默认前景色。
//
// 不给黑桃梅花写黑色是故意的——深色背景下黑字等于隐身。
func TestRedForHeartsAndDiamonds(t *testing.T) {
	withColor(t)
	for _, c := range []string{"Ah", "Td"} {
		got := Card(mustCard(t, c))
		if !strings.HasPrefix(got, red) || !strings.HasSuffix(got, reset) {
			t.Fatalf("%s 该标红，得到 %q", c, got)
		}
	}
	for _, c := range []string{"As", "Tc"} {
		got := Card(mustCard(t, c))
		if strings.Contains(got, "\033[") {
			t.Fatalf("%s 不该带颜色，得到 %q", c, got)
		}
	}
}

// TestBoldAndDimAreOffWithoutColor：没开彩色时不许往外吐转义序列。
//
// 管道、重定向、转录走的全是这条路，混进 \033[2m 就是一堆乱码。
func TestBoldAndDimAreOffWithoutColor(t *testing.T) {
	for _, got := range []string{Bold("轮到你了"), Dim("────")} {
		if strings.Contains(got, "\033[") {
			t.Fatalf("不上色时该是干净的，得到 %q", got)
		}
	}
	withColor(t)
	if got := Bold("轮到你了"); !strings.HasPrefix(got, bold) || !strings.HasSuffix(got, reset) {
		t.Fatalf("开了彩色该加粗，得到 %q", got)
	}
	// 空串不该被包起来：包了之后一个不占列的片段会平白带上两段转义。
	if got := Dim(""); got != "" {
		t.Fatalf("空串该原样返回，得到 %q", got)
	}
}

// TestWidthIgnoresEscapes：转义序列一列都不占。
//
// 算进去的话，上了色的那一行会被当成更宽，于是补空格补少了，表格就歪了。
func TestWidthIgnoresEscapes(t *testing.T) {
	withColor(t)
	colored := Card(mustCard(t, "Ah"))
	if len(colored) <= 2 {
		t.Fatalf("这一版该是带转义的，得到 %q", colored)
	}
	if got := Width(colored); got != 2 {
		t.Fatalf("A♥ 该占 2 列，得到 %d（%q）", got, colored)
	}
	// 补齐之后的可见宽度也得对。
	if got := Width(Pad(colored, 10)); got != 10 {
		t.Fatalf("补到 10 列，得到 %d", got)
	}
	// 加粗和减弱那两种也一样不占列——重画那一版按这个数算光标要挪几行。
	for _, s := range []string{Bold("轮到你了"), Dim("────")} {
		plain := strings.NewReplacer(bold, "", dim, "", reset, "").Replace(s)
		if Width(s) != Width(plain) {
			t.Fatalf("%q 算出 %d 列，去掉转义之后是 %d 列", s, Width(s), Width(plain))
		}
	}
}

// TestUseColorStaysOffForPipes：对面不是终端就别上色。
func TestUseColorStaysOffForPipes(t *testing.T) {
	t.Cleanup(func() { colorOn.Store(false) })

	var buf strings.Builder
	UseColor(&buf) // 不是 *os.File
	if colorOn.Load() {
		t.Fatal("写进 buffer 也上色了")
	}

	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	UseColor(f) // 是 *os.File，但是普通文件
	if colorOn.Load() {
		t.Fatal("写进文件也上色了")
	}
}

// TestCharDeviceIsNotATerminal：字符设备不等于终端。
//
// 这条是一个真出过的 bug 的墓碑。以前的判断是 os.ModeCharDevice，而
// /dev/null、/dev/zero、串口全都是字符设备——于是 `poker join ... > /dev/null`
// 会被当成终端，往里灌光标移动序列。x/term 走真正的 TCGETS ioctl，只有终端答得上来。
func TestCharDeviceIsNotATerminal(t *testing.T) {
	dev, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Skip("打不开 /dev/null")
	}
	defer dev.Close()
	if info, err := dev.Stat(); err == nil && info.Mode()&os.ModeCharDevice == 0 {
		t.Skip("这台机器上 /dev/null 不是字符设备，这条就没什么可测的了")
	}
	if IsTerminal(dev) {
		t.Fatal("/dev/null 被当成终端了")
	}
}

// TestRealTerminalGetsColorAndSize：对着真 pty 该上色，也该问得出尺寸。
//
// 前面几条都只能证明「不是终端时不做什么」。要证明「是终端时确实做」，
// 就得有一个真的终端——所以这里开一个 pty。
func TestRealTerminalGetsColorAndSize(t *testing.T) {
	t.Cleanup(func() { colorOn.Store(false) })
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("开不了 pty: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()

	if !IsTerminal(tty) {
		t.Fatal("真 pty 都不算终端，那就没有什么算了")
	}
	if err := pty.Setsize(tty, &pty.Winsize{Rows: 24, Cols: 80}); err != nil {
		t.Fatalf("设不了窗口大小: %v", err)
	}
	if got := Height(tty); got != 24 {
		t.Fatalf("终端该有 24 行，得到 %d", got)
	}

	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	UseColor(tty)
	if !colorOn.Load() {
		t.Fatal("对着真终端该上色")
	}

	// NO_COLOR 说了不算数就是不算数（https://no-color.org）——哪怕对面真是终端。
	colorOn.Store(false)
	t.Setenv("NO_COLOR", "1")
	UseColor(tty)
	if colorOn.Load() {
		t.Fatal("设了 NO_COLOR 还上色")
	}
}

// TestHeightIsZeroWhenThereIsNoTerminal：问不出来就说问不出来，别瞎猜一个数。
func TestHeightIsZeroWhenThereIsNoTerminal(t *testing.T) {
	if got := Height(&strings.Builder{}); got != 0 {
		t.Fatalf("不是文件，该返回 0，得到 %d", got)
	}
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got := Height(f); got != 0 {
		t.Fatalf("普通文件没有行数，该返回 0，得到 %d", got)
	}
}

func mustCard(t *testing.T, s string) poker.Card {
	t.Helper()
	c, err := poker.ParseCard(s)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
