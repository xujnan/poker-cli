package protocol

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xujnan/poker-cli/internal/poker"
)

// DefaultDirName 是放 socket 的目录名，位于用户 home 下。
const DefaultDirName = ".poker"

// DefaultDir 返回 ~/.poker。
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("protocol: 找不到用户目录: %w", err)
	}
	return filepath.Join(home, DefaultDirName), nil
}

// SocketPath 把 Table Code 拼成 socket 路径。
//
// 「按牌桌码加入」在这个项目里就降解成了这一次拼接——没有注册表，没有服务发现（ADR-0013）。
// 正因为如此，code 必须先过 ValidTableCode，所以这个函数拿 error 而不是直接拼。
func SocketPath(dir, code string) (string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if !poker.ValidTableCode(code) {
		return "", fmt.Errorf("protocol: %q 不是合法的 Table Code（%d 位大写字母数字，不含 I/O/0/1）", code, poker.TableCodeLength)
	}
	return filepath.Join(dir, code+".sock"), nil
}
