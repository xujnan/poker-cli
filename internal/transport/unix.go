package transport

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Unix 是同机传输：Table Code 直接充当 socket 文件名，放在 Dir 下（ADR-0013）。
//
// 于是「按牌桌码加入」降解成一次文件路径拼接——不需要注册表，不需要服务发现，
// 也不需要广播。换到跨机传输时，要重写的正是这一层映射。
type Unix struct {
	Dir string
}

// NewUnix 造一个同机传输。dir 留空时取 ~/.poker。
func NewUnix(dir string) (Unix, error) {
	if dir == "" {
		d, err := DefaultDir()
		if err != nil {
			return Unix{}, err
		}
		dir = d
	}
	return Unix{Dir: dir}, nil
}

// Path 返回一个 Table Code 对应的 socket 路径。
func (u Unix) Path(code string) (string, error) {
	code, err := normalizeCode(code)
	if err != nil {
		return "", err
	}
	return filepath.Join(u.Dir, code+".sock"), nil
}

// Listen 在 <Dir>/<CODE>.sock 上监听。
func (u Unix) Listen(code string) (net.Listener, error) {
	path, err := u.Path(code)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(u.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("transport: 无法创建 %s: %w", u.Dir, err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("transport: 无法监听 %s: %w", path, err)
	}
	return &unixListener{Listener: ln, path: path}, nil
}

// Dial 连上一张牌桌。
func (u Unix) Dial(code string) (net.Conn, error) {
	path, err := u.Path(code)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("transport: 连不上牌桌 %s（%s）：%w", strings.ToUpper(code), path, err)
	}
	return conn, nil
}

// Describe 给出 socket 路径。
func (u Unix) Describe(code string) string {
	path, err := u.Path(code)
	if err != nil {
		return "(非法的 Table Code)"
	}
	return path
}

// unixListener 在关闭时顺手把 socket 文件删掉。
//
// net 包自己在多数情况下也会删，这里再兜一次底：文件留着的话，
// 那个 Table Code 就再也开不了第二次，而报出来的错是「地址已被占用」——
// 看到这条错误的人一般不会想到去 rm 一个文件。
type unixListener struct {
	net.Listener
	path string
}

func (l *unixListener) Close() error {
	err := l.Listener.Close()
	_ = os.Remove(l.path)
	return err
}
