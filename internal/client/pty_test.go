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

// screen 是一个只认三样东西的终端：光标上移、清到屏幕末尾、回车换行。
//
// 它是这几条测试的判卷人，所以是照着 ANSI 的定义写的，不是照着 live.go 写的——
// 要是它跟着被测代码一起理解错，那就什么都没测到。反过来说，live.go 只用得上这三样，
// 引一个完整的终端模拟器来判这三条，是拿一个更大的黑箱去证一个小东西。
type screen struct {
	lines []string
	row   int
	col   int
}

var csi = regexp.MustCompile(`^\x1b\[([0-9;]*)([A-Za-z])`)

func (s *screen) feed(text string) {
	for i := 0; i < len(text); {
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
		if len(sc.lines) > rows {
			t.Fatalf("第 %d 帧之后屏幕有 %d 行，超过终端的 %d 行：\n%s",
				i, len(sc.lines), rows, sc.text())
		}
	}

	final := sc.text()
	t.Logf("%d 字节、%d 帧，最后屏幕上是：\n%s", len(out), len(frames), final)

	// 屏幕上只该剩最后一帧。上一手的牌、上一帧的流水，一个字都不该留。
	if n := strings.Count(final, "第 "); n != 1 {
		t.Fatalf("屏幕上有 %d 个「第 N 手」，该只有一个：\n%s", n, final)
	}
	if strings.Contains(final, "> ") {
		t.Fatalf("断开之后还留着输入提示：\n%s", final)
	}
	if !strings.Contains(final, "第 3 手") {
		t.Fatalf("最后该停在第 3 手上：\n%s", final)
	}
	// 前两手的底牌绝不能还在屏幕上。
	for _, stale := range []string{"A♠ K♦", "7♥ 7♦"} {
		if strings.Contains(final, stale) {
			t.Fatalf("上一手的底牌 %s 还留在屏幕上：\n%s", stale, final)
		}
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
