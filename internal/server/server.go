// Package server 是牌桌的唯一权威（ADR-0003）：一个进程一张牌桌，监听
// ~/.poker/<CODE>.sock，Table Code 就是文件名（ADR-0013）。
//
// 并发模型只有一条规矩：牌桌状态归一个 goroutine 独占，别人只能往 channel 里递请求。
// 每条连接另有两个 goroutine——一个读命令，一个写事件——它们碰不到牌桌状态，
// 因此牌桌上不需要任何锁。
package server

import (
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/protocol"
)

// Options 是启动一张牌桌需要的东西。
type Options struct {
	// Dir 是 socket 所在目录，留空取 ~/.poker。
	Dir string
	// Code 留空则随机生成一个 Table Code。
	Code string
	// Rand 是牌桌的随机源，必须由调用方注入——serve 的 --seed 全靠它（ADR-0004）。
	Rand *rand.Rand
	// HandDelay 是两手牌之间的间隔（ADR-0014）。自对弈时设 0。
	HandDelay time.Duration
	// Log 是服务端自己的日志去向，留空则丢弃。它与发给玩家的事件是两回事。
	Log io.Writer
}

// Server 是一张牌桌。
type Server struct {
	dir       string
	code      string
	path      string
	ln        net.Listener
	rng       *rand.Rand
	handDelay time.Duration
	log       *log.Logger

	reqs chan request
	done chan struct{}
	stop sync.Once
	wg   sync.WaitGroup

	mu    sync.Mutex
	conns map[*conn]struct{}

	// 以下字段只有牌桌 goroutine 能读写。
	seats  []*conn
	handNo int
}

// New 创建牌桌并开始监听，此时 Code 已经确定，可以打印给用户。
func New(opts Options) (*Server, error) {
	if opts.Rand == nil {
		return nil, errors.New("server: 必须注入随机源")
	}
	dir := opts.Dir
	if dir == "" {
		d, err := protocol.DefaultDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("server: 无法创建 %s: %w", dir, err)
	}

	logOut := opts.Log
	if logOut == nil {
		logOut = io.Discard
	}
	s := &Server{
		dir:       dir,
		rng:       opts.Rand,
		handDelay: opts.HandDelay,
		log:       log.New(logOut, "", log.LstdFlags),
		reqs:      make(chan request, 64),
		done:      make(chan struct{}),
		conns:     make(map[*conn]struct{}),
	}

	if opts.Code != "" {
		code := strings.ToUpper(opts.Code)
		path, err := protocol.SocketPath(dir, code)
		if err != nil {
			return nil, err
		}
		ln, err := net.Listen("unix", path)
		if err != nil {
			return nil, fmt.Errorf("server: 无法监听 %s: %w", path, err)
		}
		s.code, s.path, s.ln = code, path, ln
		return s, nil
	}

	// 没指定 code 就随机生成。撞上一张已经开着的同码牌桌时换一个再来——
	// 监听失败本身就是「这个码被占了」最可靠的判据，不必先 stat 一次。
	var lastErr error
	for i := 0; i < 10; i++ {
		code := poker.NewTableCode(s.rng)
		path, err := protocol.SocketPath(dir, code)
		if err != nil {
			return nil, err
		}
		ln, err := net.Listen("unix", path)
		if err != nil {
			lastErr = err
			continue
		}
		s.code, s.path, s.ln = code, path, ln
		return s, nil
	}
	return nil, fmt.Errorf("server: 连续 10 次都没能占下一个 Table Code: %w", lastErr)
}

// Code 返回这张牌桌的 Table Code。
func (s *Server) Code() string { return s.code }

// Path 返回 socket 路径。
func (s *Server) Path() string { return s.path }

// Serve 接受连接直到 Close 被调用。
func (s *Server) Serve() error {
	// accept 循环自己也算一份，这样 Close 的 Wait 不会在计数器归零的瞬间
	// 撞上这里为新连接做的 Add。
	s.wg.Add(1)
	defer s.wg.Done()

	s.wg.Add(1)
	go s.tableLoop()

	for {
		nc, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.done:
				return nil
			default:
			}
			return fmt.Errorf("server: accept 失败: %w", err)
		}
		s.wg.Add(1)
		go s.readLoop(nc)
	}
}

// Close 关掉牌桌并等所有 goroutine 退出。牌桌状态是易失的，不落盘（ADR-0008）。
func (s *Server) Close() error {
	s.stop.Do(func() {
		close(s.done)
		_ = s.ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			c.close()
		}
		s.mu.Unlock()
	})
	s.wg.Wait()
	// unix listener 关闭时会删掉 socket 文件，这里兜一次底。
	_ = os.Remove(s.path)
	return nil
}

// --- 请求 ---

type requestKind int

const (
	reqJoin requestKind = iota
	reqDisconnect
)

type request struct {
	kind requestKind
	c    *conn
	name string
}

func (s *Server) submit(r request) {
	select {
	case s.reqs <- r:
	case <-s.done:
	}
}

// --- 牌桌 goroutine ---

// tableLoop 是唯一碰得到牌桌状态的地方。
func (s *Server) tableLoop() {
	defer s.wg.Done()

	var timer *time.Timer
	var fire <-chan time.Time
	disarm := func() {
		if timer != nil {
			timer.Stop()
			timer, fire = nil, nil
		}
	}
	// 凑够两人就自动开下一手，不设准备确认（ADR-0014）。
	arm := func() {
		if len(s.seats) < 2 {
			disarm()
			return
		}
		if timer == nil {
			timer = time.NewTimer(s.handDelay)
			fire = timer.C
		}
	}

	for {
		select {
		case <-s.done:
			disarm()
			return
		case r := <-s.reqs:
			switch r.kind {
			case reqJoin:
				s.handleJoin(r.c, r.name)
			case reqDisconnect:
				s.handleDisconnect(r.c)
			}
			arm()
		case <-fire:
			timer, fire = nil, nil
			if len(s.seats) >= 2 {
				s.playHand()
			}
			arm()
		}
	}
}

func (s *Server) handleJoin(c *conn, name string) {
	name = strings.TrimSpace(name)
	if err := validName(name); err != nil {
		c.send(poker.Event{Type: poker.EventError, Code: "bad_name", Message: err.Error()})
		c.shutdown()
		return
	}
	if c.name != "" {
		c.send(poker.Event{Type: poker.EventError, Code: "already_joined", Message: "你已经在这张牌桌上了"})
		return
	}
	for _, seat := range s.seats {
		if seat.name == name {
			c.send(poker.Event{Type: poker.EventError, Code: "name_taken", Message: fmt.Sprintf("牌桌上已经有人叫 %s 了", name)})
			c.shutdown()
			return
		}
	}

	c.name = name
	s.seats = append(s.seats, c)
	s.log.Printf("%s 加入，牌桌上共 %d 人", name, len(s.seats))

	// 先把快照发给新人，再向全桌广播他的到来——顺序反过来的话，他会收到一条关于自己的通知
	// 却还不知道牌桌上有谁。
	c.send(poker.Event{Type: poker.EventTable, Player: name, Players: s.seatNames(), To: name})
	s.deliver([]poker.Event{{Type: poker.EventJoined, Player: name, Players: s.seatNames()}})
}

func (s *Server) handleDisconnect(c *conn) {
	for i, seat := range s.seats {
		if seat != c {
			continue
		}
		s.seats = append(s.seats[:i], s.seats[i+1:]...)
		s.log.Printf("%s 离开，牌桌上还剩 %d 人", c.name, len(s.seats))
		s.deliver([]poker.Event{{Type: poker.EventLeft, Player: c.name, Players: s.seatNames()}})
		break
	}
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}

func (s *Server) seatNames() []string {
	names := make([]string, len(s.seats))
	for i, c := range s.seats {
		names[i] = c.name
	}
	return names
}

func (s *Server) playHand() {
	s.handNo++
	deck := poker.NewDeck(s.rng)
	res, events := poker.PlayHand(s.handNo, s.seatNames(), deck)
	s.deliver(events)
	// 只记赢家。谁拿了什么底牌属于 Hand History 的职责（ADR-0008），不往运行日志里漏。
	s.log.Printf("第 %d 手结束，赢家 %s", res.Number, strings.Join(res.Winners, "、"))
}

// deliver 是事件出门的唯一关口，也是 ADR-0006 那条可见性不变量的落地点：
// 该给谁看由 poker.Visible 一处说了算，服务端这边不再自己判断。
func (s *Server) deliver(events []poker.Event) {
	for _, ev := range events {
		for _, c := range s.seats {
			if !poker.Visible(ev, c.name) {
				continue
			}
			c.send(ev)
		}
	}
}

// --- 连接 ---

type conn struct {
	nc net.Conn
	// name 只由牌桌 goroutine 写，写完之后才会被别的 goroutine 读到。
	name      string
	out       chan poker.Event
	closed    chan struct{}
	closeOnce sync.Once
}

// shutdown 请求断开，但不立刻关套接字：先让 writeLoop 把队里排着的事件冲出去，再由它收尾。
//
// 「先发一条错误再断开」这种事全靠这个顺序——直接关掉 fd 的话，那条解释原因的事件
// 就永远到不了对端，对面只看到一个莫名其妙的 EOF。
func (c *conn) shutdown() {
	c.closeOnce.Do(func() { close(c.closed) })
}

// close 是硬断开：不管队里还剩什么，立刻关掉连接。
func (c *conn) close() {
	c.shutdown()
	_ = c.nc.Close()
}

// send 永不阻塞：牌桌 goroutine 绝不能因为某个客户端读得慢而停下来，
// 那会让一个卡住的 agent 拖死整张桌子。缓冲满了就断开它。
func (c *conn) send(ev poker.Event) {
	select {
	case <-c.closed:
		return
	default:
	}
	select {
	case c.out <- ev:
	default:
		c.close()
	}
}

// readLoop 读这条连接上的命令，转成请求递给牌桌 goroutine。
func (s *Server) readLoop(nc net.Conn) {
	defer s.wg.Done()

	c := &conn{
		nc:     nc,
		out:    make(chan poker.Event, 64),
		closed: make(chan struct{}),
	}
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()

	s.wg.Add(1)
	go s.writeLoop(c)

	defer func() {
		c.close()
		s.submit(request{kind: reqDisconnect, c: c})
	}()

	cr := protocol.NewCommandReader(nc)
	for {
		cmd, err := cr.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.send(poker.Event{Type: poker.EventError, Code: "bad_command", Message: err.Error()})
			}
			return
		}
		switch cmd.Type {
		case protocol.CmdJoin:
			s.submit(request{kind: reqJoin, c: c, name: cmd.Name})
		case protocol.CmdQuit:
			return
		default:
			c.send(poker.Event{
				Type:    poker.EventError,
				Code:    "unknown_command",
				Message: fmt.Sprintf("不认识的命令 %q", cmd.Type),
			})
		}
	}
}

// writeLoop 是唯一往这条连接上写字节的 goroutine。
func (s *Server) writeLoop(c *conn) {
	defer s.wg.Done()
	defer c.close()

	for {
		select {
		case ev := <-c.out:
			if err := protocol.WriteEvent(c.nc, ev); err != nil {
				return
			}
		case <-c.closed:
			// 把已经排在队里的事件冲出去，免得「你被踢了」这类事件永远到不了对端。
			for {
				select {
				case ev := <-c.out:
					if err := protocol.WriteEvent(c.nc, ev); err != nil {
						return
					}
				default:
					return
				}
			}
		case <-s.done:
			return
		}
	}
}

func validName(name string) error {
	if name == "" {
		return errors.New("名字不能为空")
	}
	if len(name) > 24 {
		return errors.New("名字不能超过 24 字节")
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return errors.New("名字里不能有控制字符")
		}
	}
	return nil
}

// SocketDir 返回这张牌桌 socket 所在的目录，主要给测试与诊断用。
func (s *Server) SocketDir() string { return filepath.Dir(s.path) }
