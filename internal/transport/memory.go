package transport

import (
	"fmt"
	"net"
	"sync"
)

// Memory 是一个纯内存传输：不碰文件系统，也不占端口。
//
// 它首先是给测试用的——一张牌桌开在内存里，跑得快，也不会在 /tmp 里留东西。
// 但它还有一层意思：ADR-0001 那个「把传输收敛到接口后面」的接口，有第二个实现才算数。
// 只有一个实现的接口，形状是照着那个实现长的，等真要换传输时才会发现它拦不住什么。
type Memory struct {
	mu     sync.Mutex
	tables map[string]*memListener
}

// NewMemory 造一个内存传输。同一个实例上的 Listen 和 Dial 才连得起来。
func NewMemory() *Memory {
	return &Memory{tables: make(map[string]*memListener)}
}

// backlog 是还没被 Accept 的连接能排多少个，对应 TCP 的监听队列。
const backlog = 8

// Listen 在内存里挂一张牌桌。
func (m *Memory) Listen(code string) (net.Listener, error) {
	key, err := normalizeCode(code)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.tables[key]; taken {
		// 跟同机传输撞上同名 socket 时一个语义：这个码已经被占了。
		return nil, fmt.Errorf("transport: 牌桌 %s 已经开着了", key)
	}
	l := &memListener{
		code:   key,
		accept: make(chan net.Conn, backlog),
		closed: make(chan struct{}),
		owner:  m,
	}
	m.tables[key] = l
	return l, nil
}

// Dial 连上内存里的一张牌桌。
func (m *Memory) Dial(code string) (net.Conn, error) {
	key, err := normalizeCode(code)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	l := m.tables[key]
	m.mu.Unlock()
	if l == nil {
		return nil, fmt.Errorf("transport: 没有开着的牌桌 %s", key)
	}
	// net.Pipe 是同步的：一端写，另一端读走才算写完。这正好模拟了一个读得慢的客户端，
	// 服务端那边本来就有专门的写 goroutine 顶着。
	mine, theirs := net.Pipe()
	select {
	case l.accept <- theirs:
		return mine, nil
	case <-l.closed:
		_ = mine.Close()
		_ = theirs.Close()
		return nil, fmt.Errorf("transport: 牌桌 %s 已经关了", key)
	}
}

// Describe 给出一个一看就知道是内存桌的地址。
func (m *Memory) Describe(code string) string {
	key, err := normalizeCode(code)
	if err != nil {
		return "(非法的 Table Code)"
	}
	return "memory:" + key
}

type memListener struct {
	code   string
	accept chan net.Conn
	closed chan struct{}
	once   sync.Once
	owner  *Memory
}

func (l *memListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.accept:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *memListener) Close() error {
	l.once.Do(func() {
		l.owner.mu.Lock()
		delete(l.owner.tables, l.code)
		l.owner.mu.Unlock()
		close(l.closed)
		// 把还排在队里、没人 Accept 的连接关掉，免得对面永远等在那儿。
		for {
			select {
			case c := <-l.accept:
				_ = c.Close()
			default:
				return
			}
		}
	})
	return nil
}

func (l *memListener) Addr() net.Addr { return memAddr(l.code) }

type memAddr string

func (a memAddr) Network() string { return "memory" }
func (a memAddr) String() string  { return string(a) }
