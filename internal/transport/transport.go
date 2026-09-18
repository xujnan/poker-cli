// Package transport 决定「怎么连上一张牌桌」，把这件事收敛到一个接口后面（ADR-0001）。
//
// 第一版只落地同机传输（Unix domain socket），但牌局逻辑一行都不知道这件事：
// 服务端拿到的是一个 net.Listener，客户端拿到的是一个 net.Conn，中间经过什么不归它们管。
// 后续加跨机传输（TCP）时换一个实现即可，serve 与 join 的代码不用动。
//
// 「Table Code 怎么变成一个能连的地址」也归这里管，而不是散在服务端和客户端各写一遍——
// 那正是换传输时最先要改的东西：同机是拼一个文件路径（ADR-0013），跨机就得是主机加端口。
//
// 依赖方向是 transport → poker（只用到 Table Code 的校验），反向绝不允许（ADR-0012）。
package transport

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/xujnan/poker-cli/internal/poker"
)

// Transport 是一种把客户端和牌桌连起来的办法。
//
// 连接本身用标准的 net.Conn / net.Listener，不另造一套：变的是「怎么拿到一条连接」，
// 不是「连接是什么」。多发明一层只会让每个实现都先把自己包一遍。
type Transport interface {
	// Listen 为一张牌桌开始监听。牌桌关掉时由调用方 Close，实现自己负责清理
	// （比如同机传输要删掉 socket 文件，不删的话那个 Table Code 就再也开不了第二次）。
	Listen(code string) (net.Listener, error)

	// Dial 连上一张牌桌。
	Dial(code string) (net.Conn, error)

	// Describe 给出一个人能读的地址，打印在开桌信息里。
	Describe(code string) string
}

// normalizeCode 校验并规范化一个 Table Code。
//
// 每个实现都过这一关，而不是各写各的：一个在这个传输上连得上的客户端，
// 换个传输也得连得上。各实现各一套校验，等于让「什么是合法的牌桌码」有两个答案。
// 对同机传输它还兼着安全边界——码是要拼进文件路径的，藏着 ../ 就不是在找牌桌了。
func normalizeCode(code string) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !poker.ValidTableCode(code) {
		return "", fmt.Errorf("transport: %q 不是合法的 Table Code（%d 位大写字母数字，不含 I/O/0/1）",
			code, poker.TableCodeLength)
	}
	return code, nil
}

// DirName 是 poker 在用户 home 下的目录名，socket 和手牌历史都放在这儿。
const DirName = ".poker"

// DefaultDir 返回 ~/.poker。
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("transport: 找不到用户目录: %w", err)
	}
	return filepath.Join(home, DirName), nil
}
