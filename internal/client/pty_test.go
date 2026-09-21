package client

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/creack/pty"

	"github.com/xujnan/poker-cli/internal/textui"
)

// screen 是一个只认这几样东西的终端：光标上下移、存/取光标、清到行尾、
// 清到屏幕末尾、回车换行。
//
// 它是这几条测试的判卷人，所以是照着 ANSI 的定义写的，不是照着 live.go 写的——
// 要是它跟着被测代码一起理解错，那就什么都没测到。反过来说，live.go 只用得上这几样，
// 引一个完整的终端模拟器来判它们，是拿一个更大的黑箱去证一个小东西。
//
// 加一个序列到 live.go 里就得同时加到这儿。漏掉的话它不会报错，只会把那个
// 转义序列当成几个普通字符画进屏幕——测试照样绿，测的却已经不是那块屏幕了。
type screen struct {
	lines []string
	row   int
	col   int
	// saved 是 DECSC 存下的光标位置。局部重画靠它回到输入行，
	// 判卷人不认这一对的话，重写完那几行之后它就再也不知道光标在哪了。
	saved [2]int
}

var csi = regexp.MustCompile(`^\x1b\[([0-9;]*)([A-Za-z])`)

func (s *screen) feed(text string) {
	for i := 0; i < len(text); {
		// DECSC / DECRC 不是 CSI，单独认。
		if strings.HasPrefix(text[i:], "\0337") {
			s.saved = [2]int{s.row, s.col}
			i += 2
			continue
		}
		if strings.HasPrefix(text[i:], "\0338") {
			s.row, s.col = s.saved[0], s.saved[1]
			s.fit()
			i += 2
			continue
		}
		if m := csi.FindStringSubmatch(text[i:]); m != nil {
			n := 1
			if m[1] != "" {
				n = 0
				for _, c := range m[1] {
					if c >= '0' && c <= '9' {
						n = n*10 + int(c-'0')
					}
				}
			}
			switch m[2] {
			case "A": // CUU：光标上移 n 行
				s.row = max(0, s.row-n)
			case "B": // CUD：光标下移 n 行
				s.row += n
				s.fit()
			case "K": // EL 0：从光标清到行尾
				s.fit()
				s.lines[s.row] = truncCols(s.lines[s.row], s.col)
			case "J": // ED 0：从光标清到屏幕末尾
				s.fit()
				s.lines[s.row] = truncCols(s.lines[s.row], s.col)
				s.lines = s.lines[:s.row+1]
			}
			i += len(m[0])
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		switch r {
		case '\n':
			s.row++
			s.col = 0
			s.fit()
		case '\r':
			s.col = 0
		default:
			s.put(string(r), textui.Width(string(r)))
		}
		i += size
	}
}

func (s *screen) fit() {
	for len(s.lines) <= s.row {
		s.lines = append(s.lines, "")
	}
}

func (s *screen) put(g string, w int) {
	s.fit()
	line := s.lines[s.row]
	if textui.Width(line) < s.col {
		line += strings.Repeat(" ", s.col-textui.Width(line))
	}
	s.lines[s.row] = truncCols(line, s.col) + g
	s.col += w
}

// truncCols 把一行截到前 n 列。
func truncCols(s string, n int) string {
	w := 0
	for i, r := range s {
		if w >= n {
			return s[:i]
		}
		w += textui.Width(string(r))
	}
	return s
}

func (s *screen) text() string { return strings.TrimRight(strings.Join(s.lines, "\n"), "\n ") }

// TestLiveOnARealTerminalLeavesNoResidue 是就地重画这件事唯一真正的验收。
//
// 单元测试只能证明「上移的行数等于上一帧的行数」这个算术是对的。屏幕最后长什么样，
// 得把字节真写进一个终端、再按 ANSI 的规矩解回来才知道——这条测的就是那一步。
// 它替掉的是一个我手跑过一次的脚本，而手跑过一次的东西，改坏了没人会发现。
func TestLiveOnARealTerminalLeavesNoResidue(t *testing.T) {
	const rows, cols = 24, 100

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("开不了 pty: %v", err)
	}
	defer ptmx.Close()
	if err := pty.Setsize(tty, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var raw bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				raw.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	s, _ := fakeTable(t, threeHands())
	if err := Play(s, FormatLive, tty, nil, false); err != nil {
		t.Fatal(err)
	}
	tty.Close()
	<-readDone

	mu.Lock()
	out := raw.String()
	mu.Unlock()

	if out == "" {
		t.Fatal("什么都没写出来")
	}

	// 一帧一帧地放，每放一帧检查屏幕有没有超出终端高度。
	// 按帧切是必须的：判卷人不缓存被切断的转义序列，随便按字节切的话它自己先解析错。
	sc := &screen{}
	frames := regexp.MustCompile(`(?:\x1b\[\d+A)?\r\x1b\[J`).FindAllStringIndex(out, -1)
	if len(frames) < 10 {
		t.Fatalf("帧数太少（%d），事件没跑起来：\n%q", len(frames), out)
	}
	for i, f := range frames {
		end := len(out)
		if i+1 < len(frames) {
			end = frames[i+1][0]
		}
		sc.feed(out[f[0]:end])
	}

	final := sc.text()
	lines := strings.Split(final, "\n")
	t.Logf("%d 字节、%d 帧，最后屏幕上是：\n%s", len(out), len(frames), final)

	// 屏幕不该越打越长：就地重画的全部意义就在这儿。
	//
	// 留在屏幕上的只有「上一手的结尾」那一小块，不是每打完一手就往下堆一段——
	// 堆起来的话实时帧就退化成了滚动流水账，而那正是这一版要避开的东西（ADR-0017）。
	if len(lines) > rows {
		t.Fatalf("屏幕有 %d 行，超过终端的 %d 行：\n%s", len(lines), rows, final)
	}

	// 正在打的是第 3 手，而且只有一份。
	//
	// 「只有一份」是这条测试里最要紧的断言：重画的行数一旦算错，表现就是某几行
	// 被留下又被重画一遍——于是同一手出现两次。数数比肉眼看可靠。
	if n := strings.Count(final, "第 3 手"); n != 1 {
		t.Fatalf("屏幕上有 %d 个「第 3 手」，该只有一份：\n%s", n, final)
	}
	// 第 2 手作为「上一手」留着一小块；第 1 手早该让位了。
	if !strings.Contains(final, "上一手（第 2 手") {
		t.Fatalf("上一手该留在屏幕上：\n%s", final)
	}
	if strings.Contains(final, "第 1 手") {
		t.Fatalf("再往前那手不该还占着地方：\n%s", final)
	}

	// 正在打的那一块里不该混进上一手的牌。
	liveAt := 0
	for i, l := range lines {
		if strings.Contains(l, "第 3 手") {
			liveAt = i
		}
	}
	liveFrame := strings.Join(lines[liveAt:], "\n")
	for _, stale := range []string{"A♠ K♦", "7♥ 7♦"} {
		if strings.Contains(liveFrame, stale) {
			t.Fatalf("上一手的底牌 %s 混进了正在打的这一帧：\n%s", stale, liveFrame)
		}
	}
	if strings.Contains(final, "> ") {
		t.Fatalf("断开之后还留着输入提示：\n%s", final)
	}
	// 每一行都不该宽过终端——宽了会折行，折了行「上移 N 行」就再也对不上了。
	for _, l := range sc.lines {
		if w := textui.Width(l); w > cols {
			t.Fatalf("有一行占 %d 列，超过终端的 %d 列：%q", w, cols, l)
		}
	}
}

// threeHands 是三手牌的事件流，最后一手停在河牌上——也就是一帧最高的时候。
func threeHands() []string {
	var lines []string
	add := func(ss ...string) { lines = append(lines, ss...) }

	add(`{"type":"table","blinds":"1/2","seats":[{"player":"我","stack":200},{"player":"bot1","stack":200},{"player":"bot2","stack":200}]}`)
	for _, h := range []struct {
		n    int
		hole string
	}{{1, `["As","Kd"]`}, {2, `["7h","7d"]`}, {3, `["Qc","Js"]`}} {
		add(`{"type":"hand_start","hand":` + strconv.Itoa(h.n) + `,"button":"bot1","blinds":"1/2","seats":[{"player":"我","stack":200,"position":"BTN"},{"player":"bot1","stack":200,"position":"SB"},{"player":"bot2","stack":200,"position":"BB"}]}`)
		add(`{"type":"blind","player":"bot1","action":"small_blind","amount":1}`)
		add(`{"type":"blind","player":"bot2","action":"big_blind","amount":2}`)
		add(`{"type":"hole_cards","player":"我","cards":` + h.hole + `}`)
		add(`{"type":"action","player":"我","action":"call","amount":2,"committed":2,"stack":198,"pot":5}`)
		add(`{"type":"action","player":"bot1","action":"call","amount":1,"committed":2,"stack":198,"pot":6}`)
		add(`{"type":"action","player":"bot2","action":"check","committed":2,"stack":198}`)
		add(`{"type":"street","street":"flop","cards":["2c","7s","9h"],"board":["2c","7s","9h"],"pot":6}`)
		add(`{"type":"action","player":"bot1","action":"check","stack":198}`)
		add(`{"type":"action","player":"bot2","action":"check","stack":198}`)
		add(`{"type":"action","player":"我","action":"check","stack":198}`)
		add(`{"type":"street","street":"turn","cards":["Td"],"board":["2c","7s","9h","Td"],"pot":6}`)
		add(`{"type":"action","player":"bot1","action":"check","stack":198}`)
		add(`{"type":"action","player":"bot2","action":"bet","amount":4,"committed":4,"stack":194,"pot":10}`)
		add(`{"type":"action","player":"我","action":"call","amount":4,"committed":4,"stack":194,"pot":14}`)
		add(`{"type":"action","player":"bot1","action":"fold","stack":198}`)
		add(`{"type":"street","street":"river","cards":["3s"],"board":["2c","7s","9h","Td","3s"],"pot":14}`)
		add(`{"type":"action","player":"bot2","action":"check","stack":194}`)
		add(`{"type":"action","player":"我","action":"check","stack":194}`)
		add(`{"type":"showdown","showdown":[{"player":"bot2","cards":["8c","8d"],"category":"一对","best":["8c","8d","Td","9h","7s"]},{"player":"我","cards":` + h.hole + `,"category":"高牌","best":["Td","9h","7s","3s","2c"]}]}`)
		add(`{"type":"pot_awarded","pots":[{"amount":14,"winners":["bot2"]}]}`)
		add(`{"type":"hand_end","hand":` + strconv.Itoa(h.n) + `,"pot":14,"seats":[{"player":"我","stack":194},{"player":"bot1","stack":198},{"player":"bot2","stack":208}]}`)
	}
	return lines
}
