// Package server 是牌桌的唯一权威（ADR-0003）：一个进程一张牌桌。
//
// 牌桌怎么被连上不归这里管——它拿到的是一个 net.Listener，中间是 Unix socket
// 还是别的什么，由 transport 决定（ADR-0001）。
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
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/xujnan/poker-cli/internal/history"
	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/protocol"
	"github.com/xujnan/poker-cli/internal/transport"
)

// Options 是启动一张牌桌需要的东西。
type Options struct {
	// Transport 决定客户端怎么连上这张牌桌（ADR-0001）。必填。
	Transport transport.Transport
	// Code 留空则随机生成一个 Table Code。
	Code string
	// Rand 是牌桌的随机源，必须由调用方注入——serve 的 --seed 全靠它（ADR-0004）。
	Rand *rand.Rand
	// HandDelay 是两手牌之间的间隔（ADR-0014）。自对弈时设 0。
	HandDelay time.Duration
	// Blinds 建桌时定死，之后不变。
	Blinds poker.Blinds
	// Buyin 是玩家没指定时的默认带入。
	Buyin int
	// MaxBuyin 是一个人在这张桌上最多能有多少筹码，0 表示不设上限（默认）。
	//
	// 设了的话，join 和补码两边一起管——只管一边等于没管：以前只有补码有上限，
	// 而直接带着一百万坐下是放行的，想变成最深的筹码根本不用走补码那条路。
	MaxBuyin int
	// MaxHands 是打满多少手就收桌，0 表示一直打下去。
	// 无人值守的 agent 评测要的是「跑 N 手然后停」，不是「跑到有人来杀进程」。
	MaxHands int
	// ActionTimeout 是单个玩家一次行动的时限，到点按「能 check 就 check，否则 fold」
	// 处理（ADR-0011 里那条行动超时规则）。设 0 表示不限时。
	ActionTimeout time.Duration
	// Log 是服务端自己的日志去向，留空则丢弃。它与发给玩家的事件是两回事。
	Log io.Writer
}

// seat 是牌桌上的一个座位。玩家占据座位后，即使不参与当前手牌，座位仍属于他。
type seat struct {
	name  string
	stack int
	// c 为 nil 表示这个人掉线了。座位和筹码都留着，用同一个名字再 join 就认回来（ADR-0009）。
	c *conn
	// requestedOut 是他自己按的 sitout。它跟「输光了」「人不在」是三件不同的事，
	// 混成一个布尔的话，重连或补码之后就说不清他到底该不该回到牌桌上。
	requestedOut bool
	// pendingTopUp 是收下了但还没落地的补码，等两手牌之间才加到 Stack 上（ADR-0015）。
	pendingTopUp int
}

func (st *seat) online() bool { return st.c != nil }

// sittingOut 是此刻不参与手牌。三种来路：他自己要求的、筹码输光了、人不在。
func (st *seat) sittingOut() bool {
	return st.requestedOut || st.stack == 0 || !st.online()
}

// Server 是一张牌桌。
type Server struct {
	tr        transport.Transport
	code      string
	ln        net.Listener
	rng       *rand.Rand
	handDelay time.Duration
	timeout   time.Duration
	blinds    poker.Blinds
	buyin     int
	maxBuyin  int
	maxHands  int
	log       *log.Logger

	// finished 在打满 MaxHands 手之后关闭。
	finished   chan struct{}
	finishOnce sync.Once

	reqs chan request
	done chan struct{}
	stop sync.Once
	wg   sync.WaitGroup

	mu    sync.Mutex
	conns map[*conn]struct{}
	// crash 记下牌桌 goroutine 是被 panic 打断的，由 Close 报出去。
	crash error

	// history 在 Serve 之前定下来，之后只由牌桌 goroutine 用。
	history *history.Writer

	// 以下字段只有牌桌 goroutine 能读写。
	seats      []*seat
	hand       *poker.Hand
	handNo     int
	handMeta   handMeta
	lastButton string
	// armedFor 是行动计时器当前盯着的那个人。
	armedFor string
}

// New 创建牌桌并开始监听，此时 Code 已经确定，可以打印给用户。
func New(opts Options) (*Server, error) {
	if opts.Rand == nil {
		return nil, errors.New("server: 必须注入随机源")
	}
	if opts.Blinds.Small <= 0 || opts.Blinds.Big < opts.Blinds.Small {
		return nil, fmt.Errorf("server: 盲注 %s 不合法", opts.Blinds)
	}
	if opts.Buyin < opts.Blinds.Big {
		return nil, fmt.Errorf("server: 默认带入 %d 还不够一个大盲", opts.Buyin)
	}
	if opts.MaxBuyin > 0 && opts.MaxBuyin < opts.Buyin {
		return nil, fmt.Errorf("server: 带入上限 %d 比默认带入 %d 还小", opts.MaxBuyin, opts.Buyin)
	}
	if opts.Transport == nil {
		return nil, errors.New("server: 必须指定传输方式")
	}

	logOut := opts.Log
	if logOut == nil {
		logOut = io.Discard
	}
	s := &Server{
		tr:        opts.Transport,
		rng:       opts.Rand,
		handDelay: opts.HandDelay,
		timeout:   opts.ActionTimeout,
		blinds:    opts.Blinds,
		buyin:     opts.Buyin,
		maxBuyin:  opts.MaxBuyin,
		maxHands:  opts.MaxHands,
		log:       log.New(logOut, "", log.LstdFlags),
		reqs:      make(chan request, 64),
		done:      make(chan struct{}),
		finished:  make(chan struct{}),
		conns:     make(map[*conn]struct{}),
	}

	if opts.Code != "" {
		code := strings.ToUpper(opts.Code)
		ln, err := s.tr.Listen(code)
		if err != nil {
			return nil, err
		}
		s.code, s.ln = code, ln
		return s, nil
	}

	// 没指定 code 就随机生成。撞上一张已经开着的同码牌桌时换一个再来——
	// 监听失败本身就是「这个码被占了」最可靠的判据，不必先去问一遍。
	var lastErr error
	for i := 0; i < 10; i++ {
		code := poker.NewTableCode(s.rng)
		ln, err := s.tr.Listen(code)
		if err != nil {
			lastErr = err
			continue
		}
		s.code, s.ln = code, ln
		return s, nil
	}
	return nil, fmt.Errorf("server: 连续 10 次都没能占下一个 Table Code: %w", lastErr)
}

// Code 返回这张牌桌的 Table Code。
func (s *Server) Code() string { return s.code }

// Addr 返回一个人能读的地址，打印给用户看。
func (s *Server) Addr() string { return s.tr.Describe(s.code) }

// Transport 返回这张牌桌用的传输方式，客户端拿它来连。
func (s *Server) Transport() transport.Transport { return s.tr }

// Blinds 返回这张桌的盲注。
func (s *Server) Blinds() poker.Blinds { return s.blinds }

// Finished 在打满 MaxHands 手之后关闭；没设 MaxHands 时永远不会关。
//
// 它不自己调 Close：牌桌 goroutine 关自己会死锁在 Close 的 Wait 上。
// 由调用方收到信号之后决定什么时候收。
func (s *Server) Finished() <-chan struct{} { return s.finished }

// Stopped 在牌桌停下来时关闭——不管是谁让它停的。
//
// 正常收桌是调用方自己调 Close，他知道；能让调用方措手不及的只有一种：
// 牌桌 goroutine 崩了，自己把桌收了（见 catchCrash）。没有这个信号的话，
// 那种情况下没有人会去调 Close，历史也就没人冲下盘。
func (s *Server) Stopped() <-chan struct{} { return s.done }

// handMeta 是当前这手牌开局时的样子。牌打完之后座位上的筹码已经变了，
// 写历史要的是「开局前」那一份，所以得在发牌之前先留一份底。
type handMeta struct {
	seed   uint64
	button string
	seats  []history.Seat
	at     time.Time
}

// UseHistory 让这张牌桌把每手牌追加写进 path（ADR-0008）。必须在 Serve 之前调用。
func (s *Server) UseHistory(path string) error {
	w, err := history.NewWriter(path)
	if err != nil {
		return err
	}
	s.history = w
	return nil
}

// HistoryPath 是历史文件的位置，没开历史时返回空串。
func (s *Server) HistoryPath() string {
	if s.history == nil {
		return ""
	}
	return s.history.Path()
}

// Serve 接受连接直到 Close 被调用。
func (s *Server) Serve() error {
	if !s.goTracked(s.tableLoop) {
		return nil // 还没开始就已经关了
	}
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
		s.startConn(nc)
	}
}

// crashHook 只在测试里赋值，生产里恒为 nil。
//
// 在生产代码里为测试留口子是要付代价的，这里认这个代价：崩溃收尾那条路一旦写错，
// 表现是「进程没了、历史少几手、没人知道为什么」——正是没有真实触发点就测不出来的
// 那一类。而这个项目里所有能从外面触发 panic 的路都是要修掉的 bug，不是测试用的开关。
var crashHook func(handNo int)

// catchCrash 接住牌桌 goroutine 里的 panic，把它变成一次干净的收桌。
//
// 它**不是**在用 panic 代替错误处理（ADR-0019）。玩家能碰到的一切走的都是错误那条路：
// 动作不合法回一条带 code 的 error 事件，轮次仍归他（ADR-0011）；命令解析不了
// 也是 error。poker 包里那几处 panic 是契约断言——「一手牌至少两个人」这种，
// 调用方违约才会触发，也就是我们自己的 bug，而服务端在调用前全挡住了。
//
// 这里要接住的是另一回事：任何 runtime panic（越界、nil 解引用）出现在这个
// goroutine 里，整个进程就没了，异步队列里还没落盘的手牌历史跟着蒸发——
// 包括出事那一手的种子，而那是唯一能复现它的东西。这个 bug 没法靠错误处理防住，
// 你没法 if err != nil 掉一个 index out of range。
//
// 所以它的职责明确**不是**接着打：状态已经坏了，接着打等于悄悄把底池算错给某个人。
// 它只做三件事——把手数和种子记下来、让历史落盘、干净收桌，然后由 Close 报出去。
func (s *Server) catchCrash() {
	p := recover()
	if p == nil {
		return
	}
	stack := debug.Stack()
	err := fmt.Errorf("server: 第 %d 手（种子 %d）把牌桌打崩了: %v", s.handNo, s.handMeta.seed, p)

	s.mu.Lock()
	s.crash = err
	s.mu.Unlock()

	// 种子单独再说一遍：这一行就是复现它的全部输入。
	s.log.Printf("%v\n复现：poker serve --seed %d（那一手的种子是 %d，历史文件里也有）\n%s",
		err, s.handMeta.seed, s.handMeta.seed, stack)
	s.shutdown()
}

// shutdown 发出「关」的信号并断开所有连接，但不等任何 goroutine 退出。
//
// 跟 Close 分开是因为牌桌 goroutine 自己也要能触发关闭（崩了的时候）：
// 它在 wg 里，调 Close 就是等自己，直接死锁。
func (s *Server) shutdown() {
	s.stop.Do(func() {
		close(s.done)
		_ = s.ln.Close()
		s.mu.Lock()
		for c := range s.conns {
			c.close()
		}
		s.mu.Unlock()
	})
}

func (s *Server) crashErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.crash
}

// goTracked 起一个 Close 会等的 goroutine；已经在关了就不起。
//
// 所有 wg.Add 都得走这里（或 startConn），而且必须在 s.mu 里做：
// WaitGroup 不允许「计数器归零之后的 Add」和 Wait 并发，而 Close 关掉 done、
// 拿过同一把锁之后才 Wait——这样任何 Add 要么发生在 Wait 之前，要么根本不发生。
func (s *Server) goTracked(f func()) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return false
	default:
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		f()
	}()
	return true
}

// startConn 为一条新连接起读写两个 goroutine。
func (s *Server) startConn(nc net.Conn) {
	c := &conn{
		nc:     nc,
		out:    make(chan poker.Event, 256),
		closed: make(chan struct{}),
	}
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		_ = nc.Close()
		return
	default:
	}
	s.conns[c] = struct{}{}
	s.wg.Add(2)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		s.writeLoop(c)
	}()
	go func() {
		defer s.wg.Done()
		s.readLoop(c)
	}()
}

// Close 关掉牌桌并等所有 goroutine 退出。牌桌状态是易失的，不落盘（ADR-0008）。
func (s *Server) Close() error {
	s.shutdown()
	s.wg.Wait()

	if err := s.crashErr(); err != nil {
		// 崩了的话，历史照样要冲下盘——出事那一手的种子就在里面，
		// 而那是唯一能把这个 bug 原样重放出来的东西。
		if s.history != nil {
			_ = s.history.Close()
		}
		return err
	}

	if s.history != nil {
		// 丢过东西就说出来。历史是尽力而为的（ADR-0016），但「尽力」不等于「悄悄地」。
		if n := s.history.Dropped(); n > 0 {
			s.log.Printf("有 %d 手牌因为写盘跟不上被丢掉了", n)
		}
		if n := s.history.Failed(); n > 0 {
			s.log.Printf("有 %d 手牌写盘失败", n)
		}
		return s.history.Close()
	}
	return nil
}

// --- 请求 ---

type requestKind int

const (
	reqJoin requestKind = iota
	reqAction
	// reqSeat 是不参与牌局推进的座位类命令：补码、暂离、回座。
	reqSeat
	reqDisconnect
)

type request struct {
	kind requestKind
	c    *conn
	cmd  protocol.Command
}

func (s *Server) submit(r request) {
	select {
	case s.reqs <- r:
	case <-s.done:
	}
}

// --- 牌桌 goroutine ---

// tableLoop 是唯一碰得到牌桌状态的地方。
//
// 这里管着两个计时器：一个决定「下一手什么时候开」，一个决定「这个人还能想多久」。
// 后者不可省——连着却不说话的客户端会把整张牌桌冻住，而那在无人值守的自对弈里
// 迟早会发生（断线还有接管兜着，装死没有）。
func (s *Server) tableLoop() {
	defer s.catchCrash()

	var handTimer, actTimer *time.Timer
	var handFire, actFire <-chan time.Time

	disarmHand := func() {
		if handTimer != nil {
			handTimer.Stop()
			handTimer, handFire = nil, nil
		}
	}
	disarmAct := func() {
		if actTimer != nil {
			actTimer.Stop()
			actTimer, actFire = nil, nil
		}
		s.armedFor = ""
	}
	// 凑够两个有筹码的人就自动开下一手，不设准备确认（ADR-0014）。
	armHand := func() {
		if s.hand != nil || len(s.activeSeats()) < 2 || s.handsDone() {
			disarmHand()
			return
		}
		if handTimer == nil {
			handTimer = time.NewTimer(s.handDelay)
			handFire = handTimer.C
		}
	}
	armAct := func() {
		if s.timeout <= 0 || s.hand == nil || s.hand.Done() {
			disarmAct()
			return
		}
		turn := s.hand.Turn()
		if turn == "" {
			disarmAct()
			return
		}
		// 还是同一个人在想，就让钟继续走——非法动作重来一次并不重新给他一份时间，
		// 否则一个只会发非法动作的客户端就能无限拖下去。
		if turn == s.armedFor {
			return
		}
		disarmAct()
		s.armedFor = turn
		actTimer = time.NewTimer(s.timeout)
		actFire = actTimer.C
	}
	arm := func() { armHand(); armAct() }

	for {
		select {
		case <-s.done:
			disarmHand()
			disarmAct()
			return
		case r := <-s.reqs:
			switch r.kind {
			case reqJoin:
				s.handleJoin(r.c, r.cmd)
			case reqAction:
				s.handleAction(r.c, r.cmd)
			case reqSeat:
				s.handleSeatCommand(r.c, r.cmd)
			case reqDisconnect:
				s.handleDisconnect(r.c)
			}
			arm()
		case <-handFire:
			handTimer, handFire = nil, nil
			if s.hand == nil && len(s.activeSeats()) >= 2 && !s.handsDone() {
				s.startHand()
			}
			arm()
		case <-actFire:
			actTimer, actFire = nil, nil
			waited := s.armedFor
			s.armedFor = ""
			if s.hand != nil && !s.hand.Done() && waited != "" && s.hand.Turn() == waited {
				s.log.Printf("%s 行动超时，替他做决定", waited)
				s.deliverAndDrive(s.forceAction(waited, "行动超时"))
			}
			arm()
		}
	}
}

// forceAction 替某人做决定。「这一下不是他自己按的」这件事由核心统一标进事件和历史，
// 服务端不该自己去改事件字段——标记散在两个地方，迟早有一处漏掉。
func (s *Server) forceAction(player, why string) []poker.Event {
	return s.hand.ApplyForced(player, why)
}

func (s *Server) handleJoin(c *conn, cmd protocol.Command) {
	name := strings.TrimSpace(cmd.Name)
	if err := validName(name); err != nil {
		c.send(poker.Event{Type: poker.EventError, Code: "bad_name", Message: err.Error()})
		c.shutdown()
		return
	}
	if c.name != "" {
		c.send(poker.Event{Type: poker.EventError, Code: "already_joined", Message: "你已经在这张牌桌上了"})
		return
	}

	if st := s.seatOf(name); st != nil {
		if st.online() {
			c.send(poker.Event{Type: poker.EventError, Code: "name_taken", Message: fmt.Sprintf("牌桌上已经有人叫 %s 了", name)})
			c.shutdown()
			return
		}
		// 同一个名字回来了，认回原座位与筹码（ADR-0009）。重连不发凭据，名字就是身份。
		c.name = name
		st.c = c
		s.log.Printf("%s 重新连上，筹码 %d", name, st.stack)
		s.sendTable(c, name)
		s.deliver([]poker.Event{{
			Type: poker.EventJoined, Player: name, Players: s.seatNames(), Seats: s.seatViews(),
		}})
		return
	}

	if len(s.seats) >= poker.MaxSeats {
		c.send(poker.Event{
			Type: poker.EventError, Code: "table_full",
			Message: fmt.Sprintf("这张桌已经坐满 %d 个人了", poker.MaxSeats),
			Max:     poker.MaxSeats,
		})
		c.shutdown()
		return
	}

	buyin := cmd.Buyin
	if buyin == 0 {
		buyin = s.buyin
	}
	if buyin < s.blinds.Big {
		c.send(poker.Event{
			Type: poker.EventError, Code: "buyin_too_small",
			Message: fmt.Sprintf("带入至少要有一个大盲 %d", s.blinds.Big),
			Min:     s.blinds.Big,
		})
		c.shutdown()
		return
	}
	if s.maxBuyin > 0 && buyin > s.maxBuyin {
		c.send(poker.Event{
			Type: poker.EventError, Code: "buyin_too_big",
			Message: fmt.Sprintf("这张桌的带入上限是 %d", s.maxBuyin),
			Max:     s.maxBuyin,
		})
		c.shutdown()
		return
	}

	c.name = name
	s.seats = append(s.seats, &seat{name: name, stack: buyin, c: c})
	s.log.Printf("%s 加入，带入 %d，牌桌上共 %d 人", name, buyin, len(s.seats))

	// 先把快照发给新人，再向全桌广播他的到来——顺序反过来的话，他会收到一条关于自己的通知
	// 却还不知道牌桌上有谁。
	s.sendTable(c, name)
	s.deliver([]poker.Event{{
		Type: poker.EventJoined, Player: name, Players: s.seatNames(), Seats: s.seatViews(),
	}})
}

func (s *Server) sendTable(c *conn, name string) {
	c.send(poker.Event{
		Type:      poker.EventTable,
		To:        name,
		Player:    name,
		Protocol:  poker.ProtocolVersion,
		Buyin:     s.buyin,
		TimeoutMS: int(s.timeout.Milliseconds()),
		Players:   s.seatNames(),
		Seats:     s.seatViews(),
		Blinds:    s.blinds.String(),
	})
}

func (s *Server) handleAction(c *conn, cmd protocol.Command) {
	if c.name == "" {
		c.send(poker.Event{Type: poker.EventError, Code: "not_joined", Message: "先 join 再行动"})
		return
	}
	if s.hand == nil {
		c.send(poker.Event{Type: poker.EventError, Code: "no_hand", Message: "现在没有正在进行的牌局"})
		return
	}
	action, err := cmd.Action()
	if err != nil {
		c.send(poker.Event{Type: poker.EventError, Code: "bad_action", Message: err.Error()})
		return
	}
	s.deliverAndDrive(s.hand.Apply(c.name, action))
}

func (s *Server) handleDisconnect(c *conn) {
	st := s.seatByConn(c)
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
	if st == nil {
		return
	}

	st.c = nil
	s.log.Printf("%s 断线，筹码 %d 留在座位上", st.name, st.stack)
	// 断线视同 Sitting Out，筹码不没收（ADR-0009）。
	s.deliver([]poker.Event{{
		Type: poker.EventSitOut, Player: st.name, Message: "断线", Seats: s.seatViews(),
	}})

	// 他可能正轮到行动。没人接手的话，一个人拔网线就能让整张桌子永远停住。
	s.deliverAndDrive(nil)
}

// startHand 开一手新牌。
func (s *Server) startHand() {
	active := s.activeSeats()
	if len(active) < 2 {
		return
	}
	seats := make([]poker.Seat, len(active))
	for i, st := range active {
		seats[i] = poker.Seat{Player: st.name, Stack: st.stack}
	}
	button := s.buttonIndex(active)
	s.lastButton = active[button].name

	// 牌桌的随机源不直接拿去洗牌，而是给这一手牌抽一个种子（ADR-0016）。
	// 这样历史里的每条记录都自足：拿着它就能单独重放第 37 手，
	// 不必先把前 36 手连同每个人的每个动作原样重来一遍。
	seed := s.rng.Uint64()
	before := make([]history.Seat, len(active))
	for i, st := range active {
		before[i] = history.Seat{Player: st.name, Stack: st.stack}
	}

	s.handNo++
	if crashHook != nil {
		crashHook(s.handNo)
	}
	hand, events := poker.NewHand(s.handNo, seats, button, s.blinds, poker.NewDeck(poker.RandFor(seed)))
	s.hand = hand
	s.handMeta = handMeta{seed: seed, button: active[button].name, seats: before, at: time.Now()}
	s.deliverAndDrive(events)
}

// deliverAndDrive 把事件投出去，然后替掉线的人把牌局推下去，直到轮到一个在线的人或牌局结束。
func (s *Server) deliverAndDrive(events []poker.Event) {
	s.deliver(events)
	if s.hand == nil {
		return
	}
	for guard := 0; !s.hand.Done(); guard++ {
		if guard > 1000 {
			s.log.Printf("牌局推不动了，第 %d 手中止", s.handNo)
			break
		}
		turn := s.hand.Turn()
		if st := s.seatOf(turn); st != nil && st.online() {
			break
		}
		// 轮到一个已经不在的人：按行动超时那套替他做决定（ADR-0011）。
		s.deliver(s.forceAction(turn, "掉线代打"))
	}
	if s.hand.Done() {
		s.settleHand()
	}
}

// settleHand 把一手牌的结果写回牌桌。
func (s *Server) settleHand() {
	res := s.hand.Result()
	for name, stack := range res.Stacks {
		if st := s.seatOf(name); st != nil {
			st.stack = stack
		}
	}
	s.hand = nil
	s.log.Printf("第 %d 手结束，底池 %d，赢家 %s", res.Number, res.Pot, strings.Join(res.Winners, "、"))

	if s.history != nil {
		m := s.handMeta
		s.history.Append(history.Of(s.code, m.seed, s.blinds, m.button, m.seats, m.at, res))
	}

	// Stack 归零的人自动进入 Sitting Out——这是 Sitting Out 定义里就写着的一条。
	// 他还留在座位上，也还收得到牌桌上的公开信息，只是不再被发牌，补了码就能回来。
	for name, stack := range res.Stacks {
		if stack != 0 {
			continue
		}
		if st := s.seatOf(name); st != nil {
			s.deliver([]poker.Event{{
				Type: poker.EventSitOut, Player: st.name, Message: "筹码输光了", Seats: s.seatViews(),
			}})
		}
	}

	// 这里正是「两手牌之间」，攒着的补码在此刻落地（ADR-0015）。
	s.applyPendingTopUps()

	if s.handsDone() {
		s.log.Printf("打满 %d 手，收桌", s.maxHands)
		s.finishOnce.Do(func() { close(s.finished) })
	}
}

// handsDone 判断是不是已经打满了 MaxHands 手。
func (s *Server) handsDone() bool {
	return s.maxHands > 0 && s.handNo >= s.maxHands
}

// --- 座位类命令 ---

func (s *Server) handleSeatCommand(c *conn, cmd protocol.Command) {
	if c.name == "" {
		c.send(poker.Event{Type: poker.EventError, Code: "not_joined", Message: "先 join 再说别的"})
		return
	}
	st := s.seatOf(c.name)
	if st == nil {
		c.send(poker.Event{Type: poker.EventError, Code: "not_joined", Message: "你不在这张牌桌上"})
		return
	}
	switch cmd.Type {
	case protocol.CmdTopUp:
		s.handleTopUp(st, cmd.Amount)
	case protocol.CmdSitOut:
		s.handleSitOut(st)
	case protocol.CmdSitIn:
		s.handleSitIn(st)
	}
}

func (s *Server) handleTopUp(st *seat, amount int) {
	if amount <= 0 {
		st.c.send(poker.Event{Type: poker.EventError, Code: "bad_amount", Message: "补码要带一个正数额"})
		return
	}
	// 上限默认不设（ADR-0015）。设了的话 join 和补码两边用同一条线——
	// 只管一边等于没管：想变成桌上最深的筹码，直接带着一百万坐下就行，不必走补码。
	if s.maxBuyin > 0 {
		room := s.maxBuyin - (st.stack + st.pendingTopUp)
		if room <= 0 {
			st.c.send(poker.Event{
				Type: poker.EventError, Code: "stack_at_max",
				Message: fmt.Sprintf("你已经有 %d 了，这张桌的带入上限是 %d", st.stack+st.pendingTopUp, s.maxBuyin),
				Max:     s.maxBuyin,
			})
			return
		}
		if amount > room {
			st.c.send(poker.Event{
				Type: poker.EventError, Code: "topup_too_big",
				Message: fmt.Sprintf("最多还能补 %d（带入上限 %d）", room, s.maxBuyin),
				Max:     room,
			})
			return
		}
	}

	st.pendingTopUp += amount
	if s.hand == nil {
		// 现在就是两手牌之间，不用等。
		s.applyPendingTopUps()
		return
	}
	st.c.send(poker.Event{
		Type: poker.EventTopUp, To: st.name, Player: st.name, Amount: amount,
		Message: "这手牌打完就到账",
	})
}

// applyPendingTopUps 把攒着的补码加到筹码上。只能在两手牌之间调用。
func (s *Server) applyPendingTopUps() {
	for _, st := range s.seats {
		if st.pendingTopUp <= 0 {
			continue
		}
		// 没设上限就照单全收；设了才在落地时再看一眼。
		//
		// 落地时还要看一眼，是因为 handleTopUp 收下它的时候筹码还是另一个数：
		// 牌局中途发的补码要等这手打完，而这手里他可能赢了一大把。那时候
		// 回头拒绝他已经收下的东西更糟，所以这里截断——但要说出来，
		// 不能像以前那样闷声把数改掉。
		add := st.pendingTopUp
		if s.maxBuyin > 0 {
			add = min(add, s.maxBuyin-st.stack)
		}
		st.pendingTopUp = 0
		if add <= 0 {
			st.c.send(poker.Event{
				Type: poker.EventError, Code: "stack_at_max", To: st.name,
				Message: fmt.Sprintf("这手打完你已经有 %d 了，到了带入上限 %d，补码没落地", st.stack, s.maxBuyin),
				Max:     s.maxBuyin,
			})
			continue
		}
		st.stack += add
		s.log.Printf("%s 补码 %d，现在有 %d", st.name, add, st.stack)
		// 拷一份再取地址：事件是排队等着序列化的，直接指向座位字段的话，
		// 等它真正被写出去时那个数可能已经变了。
		stack := st.stack
		s.deliver([]poker.Event{{
			Type: poker.EventTopUp, Player: st.name, Amount: add, Stack: &stack, Seats: s.seatViews(),
		}})
	}
}

func (s *Server) handleSitOut(st *seat) {
	if st.requestedOut {
		st.c.send(poker.Event{Type: poker.EventError, Code: "already_sitting_out", Message: "你已经在暂离了"})
		return
	}
	st.requestedOut = true
	// 正在进行的这手牌照打完，暂离从下一手开始生效——牌都发了才说不玩，
	// 那是把已经投进池子的筹码丢给别人。
	msg := "自己要求"
	if s.hand != nil {
		msg = "自己要求，这手牌打完生效"
	}
	s.deliver([]poker.Event{{
		Type: poker.EventSitOut, Player: st.name, Message: msg, Seats: s.seatViews(),
	}})
}

func (s *Server) handleSitIn(st *seat) {
	if st.stack+st.pendingTopUp == 0 {
		st.c.send(poker.Event{
			Type: poker.EventError, Code: "no_chips",
			Message: "一分钱都没有，先 topup 再回座",
		})
		return
	}
	st.requestedOut = false
	s.deliver([]poker.Event{{
		Type: poker.EventSitIn, Player: st.name, Seats: s.seatViews(),
	}})
}

// deliver 是事件出门的唯一关口，也是 ADR-0006 那条可见性不变量的落地点：
// 该给谁看由 poker.Visible 一处说了算，服务端这边不再自己判断。
//
// 注意这里遍历的是全部在线座位，而不是「这手牌的参与者」：Sitting Out 的人
// 照样收得到每一条广播，一条定向事件都收不到——这正是 ADR-0006 要求的那种旁观视角。
func (s *Server) deliver(events []poker.Event) {
	for _, ev := range events {
		for _, st := range s.seats {
			if !st.online() || !poker.Visible(ev, st.name) {
				continue
			}
			st.c.send(ev)
		}
	}
}

// --- 座位查询 ---

// activeSeats 是这一手牌能上桌的人：在线、有筹码、没暂离。
func (s *Server) activeSeats() []*seat {
	var out []*seat
	for _, st := range s.seats {
		if !st.sittingOut() {
			out = append(out, st)
		}
	}
	return out
}

// buttonIndex 算出这手牌 button 落在 active 的第几位。
//
// Button 每手往左移一位（CONTEXT）。有人中途离座时不能简单地加一了事，
// 得从上一手的 button 沿着牌桌顺序往后找第一个这手还在打的人。
func (s *Server) buttonIndex(active []*seat) int {
	if s.lastButton == "" {
		return 0
	}
	start := -1
	for i, st := range s.seats {
		if st.name == s.lastButton {
			start = i
			break
		}
	}
	if start < 0 {
		return 0 // 上一手的 button 已经不在桌上了
	}
	for k := 1; k <= len(s.seats); k++ {
		cand := s.seats[(start+k)%len(s.seats)]
		for j, a := range active {
			if a == cand {
				return j
			}
		}
	}
	return 0
}

func (s *Server) seatOf(name string) *seat {
	for _, st := range s.seats {
		if st.name == name {
			return st
		}
	}
	return nil
}

func (s *Server) seatByConn(c *conn) *seat {
	for _, st := range s.seats {
		if st.c == c {
			return st
		}
	}
	return nil
}

func (s *Server) seatNames() []string {
	names := make([]string, len(s.seats))
	for i, st := range s.seats {
		names[i] = st.name
	}
	return names
}

func (s *Server) seatViews() []poker.SeatView {
	out := make([]poker.SeatView, len(s.seats))
	for i, st := range s.seats {
		out[i] = poker.SeatView{
			Player:     st.name,
			Stack:      st.stack,
			SittingOut: st.sittingOut(),
		}
	}
	return out
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
func (s *Server) readLoop(c *conn) {
	nc := c.nc
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
		switch {
		case cmd.Type == protocol.CmdJoin:
			s.submit(request{kind: reqJoin, c: c, cmd: cmd})
		case cmd.Type == protocol.CmdQuit:
			return
		case cmd.IsAction():
			s.submit(request{kind: reqAction, c: c, cmd: cmd})
		case cmd.IsSeatCommand():
			s.submit(request{kind: reqSeat, c: c, cmd: cmd})
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
