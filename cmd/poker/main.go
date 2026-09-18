// Command poker 是德州扑克命令行程序的唯一入口：serve 开一张牌桌，join 以人的身份坐下，
// bot 以机器人的身份坐下。人和 agent 共用这一套 CLI，只在 --format 上分岔（ADR-0002）。
package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xujnan/poker-cli/internal/client"
	"github.com/xujnan/poker-cli/internal/history"
	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/server"
	"github.com/xujnan/poker-cli/internal/transport"
)

const usage = `用法：
  poker serve [--blinds 1/2] [--seed N] [--hand-delay 3s] [--timeout 30s]  开一张牌桌，打印 Table Code
  poker join <CODE> --as <名字> [--buyin N] [--format ...]  以人的身份坐下
  poker bot  <CODE> --as <名字> [--buyin N] [--rebuy]       以机器人的身份坐下（独立进程，走与 agent 相同的接口）
  poker verify <历史文件>                                    重放手牌历史，确认每一手都还原得回去

各子命令的 --help 里有完整参数。
`

// defaultBuyinBigBlinds 是默认带入，按大盲的倍数算。100 个大盲是常见的坐下深度。
const defaultBuyinBigBlinds = 100

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = runServe(os.Args[2:])
	case "join":
		err = runJoin(os.Args[2:])
	case "bot":
		err = runBot(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "不认识的子命令 %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	blindsFlag := fs.String("blinds", "1/2", "盲注，写成 小盲/大盲")
	buyin := fs.Int("buyin", 0, "默认带入，留空则取 100 个大盲")
	seed := fs.Uint64("seed", 0, "洗牌随机种子，0 表示每次都不一样。给定同一个种子，牌序完全可复现")
	handDelay := fs.Duration("hand-delay", 3*time.Second, "两手牌之间的间隔，自对弈时设 0")
	timeout := fs.Duration("timeout", 30*time.Second, "单次行动的时限，到点按「能过牌就过牌，否则弃牌」处理；设 0 表示不限时")
	code := fs.String("code", "", "指定 Table Code，留空则随机生成")
	dir := fs.String("dir", "", "socket 所在目录，留空取 ~/.poker")
	historyPath := fs.String("history", "", "手牌历史文件，留空则用 <dir>/history/<CODE>-<时间>.jsonl")
	noHistory := fs.Bool("no-history", false, "不写手牌历史")
	if err := fs.Parse(args); err != nil {
		return err
	}

	blinds, err := poker.ParseBlinds(*blindsFlag)
	if err != nil {
		return err
	}
	if *buyin == 0 {
		*buyin = blinds.Big * defaultBuyinBigBlinds
	}
	home, err := pokerDir(*dir)
	if err != nil {
		return err
	}
	tr, err := transport.NewUnix(home)
	if err != nil {
		return err
	}

	s, err := server.New(server.Options{
		Transport:     tr,
		Code:          *code,
		Rand:          newRand(*seed),
		HandDelay:     *handDelay,
		ActionTimeout: *timeout,
		Blinds:        blinds,
		Buyin:         *buyin,
		Log:           os.Stderr,
	})
	if err != nil {
		return err
	}

	if !*noHistory {
		path := *historyPath
		if path == "" {
			path = history.DefaultPath(home, s.Code(), time.Now())
		}
		if err := s.UseHistory(path); err != nil {
			return err
		}
	}

	fmt.Printf("牌桌已开：%s（盲注 %s，默认带入 %d）\n", s.Code(), blinds, *buyin)
	fmt.Printf("加入：poker join %s --as <你的名字>\n", s.Code())
	fmt.Printf("监听：%s\n", s.Addr())
	if p := s.HistoryPath(); p != "" {
		fmt.Printf("手牌历史：%s\n", p)
	}
	if *seed != 0 {
		fmt.Printf("随机种子：%d（牌序可复现）\n", *seed)
	}

	// 牌桌状态是纯内存的，进程一走就没了（ADR-0008）。这里只要保证 socket 文件被删掉，
	// 否则那个 Table Code 就再也开不了第二次。
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		fmt.Fprintln(os.Stderr, "收到退出信号，关闭牌桌。")
		_ = s.Close()
	}()

	return s.Serve()
}

func runJoin(args []string) error {
	fs := flag.NewFlagSet("join", flag.ExitOnError)
	name := fs.String("as", "", "你在牌桌上的名字（必填）")
	buyin := fs.Int("buyin", 0, "带入多少筹码，留空则用牌桌的默认值")
	rebuy := fs.Bool("rebuy", false, "输光之后自动补码回最初的带入")
	format := fs.String("format", client.FormatText, "输出格式：text 给人看，jsonl 给 agent 看")
	dir := fs.String("dir", "", "socket 所在目录，留空取 ~/.poker")
	code, err := parseCodeAndFlags(fs, args, "join")
	if err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("必须用 --as 指定名字")
	}
	if *format != client.FormatText && *format != client.FormatJSONL {
		return fmt.Errorf("--format 只能是 %s 或 %s", client.FormatText, client.FormatJSONL)
	}

	s, err := dialTable(*dir, code, *name, *buyin)
	if err != nil {
		return err
	}
	defer s.Close()
	return client.Play(s, *format, os.Stdout, os.Stdin, *rebuy)
}

func runBot(args []string) error {
	fs := flag.NewFlagSet("bot", flag.ExitOnError)
	name := fs.String("as", "", "机器人在牌桌上的名字（必填）")
	buyin := fs.Int("buyin", 0, "带入多少筹码，留空则用牌桌的默认值")
	rebuy := fs.Bool("rebuy", false, "输光之后自动补码回最初的带入，牌桌就能一直打下去")
	dir := fs.String("dir", "", "socket 所在目录，留空取 ~/.poker")
	code, err := parseCodeAndFlags(fs, args, "bot")
	if err != nil {
		return err
	}
	if *name == "" {
		return fmt.Errorf("必须用 --as 指定名字")
	}

	s, err := dialTable(*dir, code, *name, *buyin)
	if err != nil {
		return err
	}
	defer s.Close()
	// 机器人把看到的事件打到 stderr，stdout 留给将来可能的结构化输出。
	return client.RunBot(s, os.Stderr, *rebuy)
}

// runVerify 把一个历史文件从头重放一遍，确认每手牌都还原得回去。
//
// 历史存在的理由之一是「出了 bug 能复现」（ADR-0008）。没有这一步，
// 谁也不知道记下来的东西到底还原不还原得回去——而等到真要复现的时候才发现记漏了，
// 那份历史就已经白攒了。
func runVerify(args []string) error {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		return fmt.Errorf("用法：poker verify <历史文件>")
	}
	path := args[0]

	var checked, bad int
	err := history.Scan(path, func(line int, r history.Record) error {
		checked++
		if err := history.Verify(r); err != nil {
			bad++
			fmt.Printf("✗ 第 %d 行（第 %d 手，种子 %d）：%v\n", line, r.Hand, r.Seed, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if checked == 0 {
		fmt.Println("文件里一手牌都没有。")
		return nil
	}
	fmt.Printf("重放了 %d 手牌，%d 手对不上。\n", checked, bad)
	if bad > 0 {
		return fmt.Errorf("有 %d 手牌还原不回去", bad)
	}
	return nil
}

// pokerDir 是 poker 放 socket 和手牌历史的地方。
func pokerDir(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	return transport.DefaultDir()
}

// dialTable 按当前的传输方式连上一张牌桌。
//
// 人和机器人走同一个函数——它们连的必须是同一条路，否则「机器人在回归测试 agent 接口」
// 这句话就不成立了（ADR-0010）。
func dialTable(dir, code, name string, buyin int) (*client.Session, error) {
	home, err := pokerDir(dir)
	if err != nil {
		return nil, err
	}
	tr, err := transport.NewUnix(home)
	if err != nil {
		return nil, err
	}
	return client.Dial(tr, code, name, buyin)
}

// parseCodeAndFlags 从 `poker join ABC123 --as alice` 这种形式里取出 Table Code。
// Table Code 是位置参数而不是 flag，因为口头报出来的就是它。
func parseCodeAndFlags(fs *flag.FlagSet, args []string, name string) (string, error) {
	if len(args) == 0 || args[0] == "" || args[0][0] == '-' {
		return "", fmt.Errorf("用法：poker %s <CODE> --as <名字>", name)
	}
	code := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return "", err
	}
	return code, nil
}

// newRand 造随机源。种子为 0 时用时钟与进程号凑一个——注意时钟只在这一层出现，
// 纯核心里一次都不许有（ADR-0012）。
func newRand(seed uint64) *rand.Rand {
	if seed == 0 {
		return rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(os.Getpid())))
	}
	return rand.New(rand.NewPCG(seed, 0x9E3779B97F4A7C15))
}
