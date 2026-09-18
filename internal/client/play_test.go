package client

import (
	"bytes"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/xujnan/poker-cli/internal/protocol"
	"github.com/xujnan/poker-cli/internal/transport"
)

// fakeTable 是一个只会照本宣科的牌桌：把给定的几行原样发过去，然后挂断。
//
// 用它而不是真服务端，是因为这几条测的是客户端这一侧——尤其是 jsonl 那条路
// 必须一个字节都不改，而只有手写的行才能让「原样」这件事真的可断言。
func fakeTable(t *testing.T, lines []string) (*Session, <-chan []protocol.Command) {
	t.Helper()
	tr := transport.NewMemory()
	const code = "FAKE23"
	ln, err := tr.Listen(code)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	cmds := make(chan []protocol.Command, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			cmds <- nil
			return
		}
		// 读客户端的命令，直到它断开；join 那条也在里面。
		got := make(chan []protocol.Command, 1)
		go func() {
			var seen []protocol.Command
			r := protocol.NewCommandReader(conn)
			for {
				c, err := r.Next()
				if err != nil {
					got <- seen
					return
				}
				seen = append(seen, c)
			}
		}()
		for _, l := range lines {
			if _, err := io.WriteString(conn, l+"\n"); err != nil {
				break
			}
		}
		// 给客户端一点时间把命令发回来，然后挂断。
		time.Sleep(50 * time.Millisecond)
		conn.Close()
		cmds <- <-got
	}()

	s, err := Dial(tr, code, "我", 200)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, cmds
}

var sampleLines = []string{
	`{"type":"table","blinds":"1/2","seats":[{"player":"我","stack":200}]}`,
	`{"type":"hand_start","hand":1,"button":"bot1","blinds":"1/2","seats":[{"player":"我","stack":200,"position":"BTN"}]}`,
	`{"type":"hole_cards","player":"我","cards":["As","Kd"]}`,
	`{"type":"hand_end","hand":1,"pot":6,"seats":[{"player":"我","stack":206}]}`,
}

// TestPlayJSONLPassesBytesThrough：jsonl 那条路是给 agent 的，服务端说什么就原样出什么。
//
// 解码再编码一次就够了：字段顺序会变、omitempty 会吃掉零值、我们不认识的新字段会凭空消失。
// agent 那边看到的就不再是服务端说的话。
func TestPlayJSONLPassesBytesThrough(t *testing.T) {
	s, _ := fakeTable(t, sampleLines)
	var out bytes.Buffer
	if err := Play(s, FormatJSONL, &out, nil, false); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), strings.Join(sampleLines, "\n")+"\n"; got != want {
		t.Fatalf("字节不一样了：\n得到 %q\n期望 %q", got, want)
	}
}

// TestPlayTextStaysPlain：滚动那一版一个转义序列都不许有。
//
// 这一条是给管道兜底的：转录、CI 的日志、重定向出来的文件走的都是这条路，
// 混进 \033[12A 之后那些东西就再也没法看了。
func TestPlayTextStaysPlain(t *testing.T) {
	s, _ := fakeTable(t, sampleLines)
	var out bytes.Buffer
	if err := Play(s, FormatText, &out, nil, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "\033") {
		t.Fatalf("text 模式漏了转义序列出来：%q", out.String())
	}
	if !strings.Contains(out.String(), "连接已断开") {
		t.Fatalf("断开该说一声：%q", out.String())
	}
}

// TestResolveFormatFallsBackOffTerminal：对面不是终端就退回滚动输出。
//
// 就地重画只有终端认；往管道里写光标移动序列，出来的是一堆乱码，而这正是
// 测试、转录、`| tee` 每天在走的那条路。
func TestResolveFormatFallsBackOffTerminal(t *testing.T) {
	if got := ResolveFormat(FormatAuto, &bytes.Buffer{}); got != FormatText {
		t.Fatalf("非终端该退回 %s，得到 %s", FormatText, got)
	}
	// 显式指定的一律照办：他知道自己在往哪儿输出。
	for _, f := range []string{FormatText, FormatJSONL, FormatLive} {
		if got := ResolveFormat(f, &bytes.Buffer{}); got != f {
			t.Fatalf("显式的 %s 不该被改成 %s", f, got)
		}
	}
}

// TestPlaySendsTypedCommands：敲进去的一行要变成一条命令发出去。
//
// 输入和事件现在汇在同一个 select 循环里（重画那一版要求呈现是单线程的），
// 这条守着「顺手把命令路径也改坏了」这种事。
func TestPlaySendsTypedCommands(t *testing.T) {
	s, cmds := fakeTable(t, sampleLines)
	var out bytes.Buffer
	in := strings.NewReader("call\nbet 40\ntopup 100\nsitout\n")
	if err := Play(s, FormatText, &out, in, false); err != nil {
		t.Fatal(err)
	}
	got := <-cmds
	want := []protocol.Command{
		{Type: protocol.CmdJoin, Name: "我", Buyin: 200},
		{Type: protocol.CmdCall},
		{Type: protocol.CmdBet, Amount: 40},
		{Type: protocol.CmdTopUp, Amount: 100},
		{Type: protocol.CmdSitOut},
	}
	if len(got) != len(want) {
		t.Fatalf("发出去 %d 条命令，期望 %d 条：%+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 条命令是 %+v，期望 %+v", i, got[i], want[i])
		}
	}
}

// TestPlayReportsBadInputWithoutSending：输错了要有话说，但不能发到服务端去。
func TestPlayReportsBadInputWithoutSending(t *testing.T) {
	s, cmds := fakeTable(t, sampleLines)
	var out bytes.Buffer
	if err := Play(s, FormatText, &out, strings.NewReader("raise 40\ntopup 0\n"), false); err != nil {
		t.Fatal(err)
	}
	if got := <-cmds; len(got) != 1 {
		t.Fatalf("除了 join 不该发出任何命令，得到 %+v", got)
	}
	if !strings.Contains(out.String(), "topup 要跟一个正数额") {
		t.Fatalf("没提示 topup 用法：%q", out.String())
	}
}

// TestPlayLiveRendersOneFrameAtATime：重画那一版每来一条事件重画一帧，
// 而且第一帧之后每一帧都先把光标挪回上一帧的开头。
func TestPlayLiveRendersOneFrameAtATime(t *testing.T) {
	s, _ := fakeTable(t, sampleLines)
	var out bytes.Buffer
	if err := Play(s, FormatLive, &out, nil, false); err != nil {
		t.Fatal(err)
	}
	// start 一帧 + 四条事件各一帧 + 断开一帧 = 六帧，头一帧不上移。
	if got, want := strings.Count(out.String(), "\r\033[J"), 6; got != want {
		t.Fatalf("画了 %d 帧，期望 %d 帧：\n%q", got, want, out.String())
	}
	if got := strings.Count(out.String(), "\033[0A"); got != 0 {
		t.Fatalf("不该出现「上移 0 行」这种空动作：%q", out.String())
	}
	// 最后停在断开那句话上，不留输入提示。
	if !strings.Contains(out.String(), "连接已断开") {
		t.Fatalf("断开该说一声：%q", out.String())
	}
}

// TestPlaySurvivesStdinClosing：管道结束了牌桌还在打，不能跟着退出。
//
// `echo call | poker join ...` 就是这个形状：命令发完了，人还想接着看。
func TestPlaySurvivesStdinClosing(t *testing.T) {
	tr := transport.NewMemory()
	const code = "STDN23"
	ln, err := tr.Listen(code)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	served := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		served <- conn
		// net.Pipe 是同步的：没人读，客户端连 join 都写不出去。
		_, _ = io.Copy(io.Discard, conn)
	}()

	s, err := Dial(tr, code, "我", 200)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	conn := <-served

	done := make(chan error, 1)
	var out bytes.Buffer
	go func() { done <- Play(s, FormatText, &out, strings.NewReader("call\n"), false) }()

	// 标准输入已经到头了。牌桌接着发事件，客户端得照样收。
	time.Sleep(50 * time.Millisecond)
	if _, err := io.WriteString(conn, `{"type":"hand_end","hand":9,"pot":12}`+"\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	conn.Close()

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Play 没退出")
	}
	if !strings.Contains(out.String(), "这手牌结束") {
		t.Fatalf("标准输入关了之后的事件没收到：%q", out.String())
	}
}

// 让编译器替我确认三种视图都真的实现了那套接口。
var (
	_ view = (*textView)(nil)
	_ view = (*jsonlView)(nil)
	_ view = (*liveView)(nil)
)
