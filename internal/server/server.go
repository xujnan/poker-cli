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
	// Blinds 建桌时定死，之后不变。
	Blinds poker.Blinds
	// Buyin 是玩家没指定时的默认带入。
	Buyin int
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
	c          *conn
	sittingOut bool
}

func (st *seat) online() bool { return st.c != nil }

// Server 是一张牌桌。
type Server struct {
	dir       string
	code      string
	path      string
	ln        net.Listener
	rng       *rand.Rand
	handDelay time.Duration
	timeout   time.Duration
	blinds    poker.Blinds
	buyin     int
	log       *log.Logger

	reqs chan request
	done chan struct{}
	stop sync.Once
	wg   sync.WaitGroup

	mu    sync.Mutex
	conns map[*conn]struct{}

	// 以下字段只有牌桌 goroutine 能读写。
	seats      []*seat
	hand       *poker.Hand
	handNo     int
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
		timeout:   opts.ActionTimeout,
		blinds:    opts.Blinds,
		buyin:     opts.Buyin,
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

// Blinds 返回这张桌的盲注。
func (s *Server) Blinds() poker.Blinds { return s.blinds }

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
	reqAction
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
		if s.hand != nil || len(s.activeSeats()) < 2 {
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
			case reqDisconnect:
				s.handleDisconnect(r.c)
			}
			arm()
		case <-handFire:
			handTimer, handFire = nil, nil
			if s.hand == nil && len(s.activeSeats()) >= 2 {
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

// forceAction 替某人做决定，并把「这一下不是他自己按的」标在事件上。
//
// 牌桌上其他人（和事后翻记录的人）得能分清「他选择了弃牌」和「他没说话，系统替他弃了」。
func (s *Server) forceAction(player, why string) []poker.Event {
	events := s.hand.Apply(player, s.hand.ForcedAction(player))
	for i := range events {
		if events[i].Type == poker.EventAction && events[i].Player == player {
			events[i].Forced = true
			events[i].Message = why
		}
	}
	return events
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
		st.sittingOut = st.stack == 0
		s.log.Printf("%s 重新连上，筹码 %d", name, st.stack)
		s.sendTable(c, name)
		s.deliver([]poker.Event{{
			Type: poker.EventJoined, Player: name, Players: s.seatNames(), Seats: s.seatViews(),
		}})
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
		Type:    poker.EventTable,
		To:      name,
		Player:  name,
		Players: s.seatNames(),
		Seats:   s.seatViews(),
		Blinds:  s.blinds.String(),
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
	st.sittingOut = true
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

	s.handNo++
	deck := poker.NewDeck(s.rng)
	hand, events := poker.NewHand(s.handNo, seats, button, s.blinds, deck)
	s.hand = hand
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

	// Stack 归零的人自动进入 Sitting Out——这是 Sitting Out 定义里就写着的一条。
	// 他还留在座位上，也还收得到牌桌上的公开信息，只是不再被发牌。
	// 要想再打，得等补码那一刀（Top-up 只能发生在两手牌之间）。
	for _, st := range s.seats {
		if st.stack == 0 && !st.sittingOut {
			st.sittingOut = true
			s.deliver([]poker.Event{{
				Type: poker.EventSitOut, Player: st.name, Message: "筹码输光了", Seats: s.seatViews(),
			}})
		}
	}
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
		if st.online() && !st.sittingOut && st.stack > 0 {
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
			SittingOut: st.sittingOut,
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

// SocketDir 返回这张牌桌 socket 所在的目录，主要给测试与诊断用。
func (s *Server) SocketDir() string { return filepath.Dir(s.path) }
