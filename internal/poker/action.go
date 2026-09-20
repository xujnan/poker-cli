package poker

import (
	"fmt"
	"strconv"
	"strings"
)

// Street 是一手牌内部的四个下注阶段。
type Street uint8

const (
	Preflop Street = iota
	Flop
	Turn
	River
)

var streetNames = [...]string{Preflop: "preflop", Flop: "flop", Turn: "turn", River: "river"}

func (s Street) String() string { return streetNames[s] }

// ActionKind 是玩家轮到自己时能做的五种事之一。
type ActionKind uint8

const (
	Fold ActionKind = iota
	Check
	Call
	// BetTo 是「把我在当前 Street 上的总投入推到 Amount」，不是「再加 Amount」（ADR-0005）。
	// 名字里带 To 就是为了让写代码的人也无处误解——传统术语里的下注与加注在这里是同一件事。
	BetTo
	AllIn
)

var actionNames = [...]string{Fold: "fold", Check: "check", Call: "call", BetTo: "bet", AllIn: "allin"}

func (k ActionKind) String() string { return actionNames[k] }

// Action 是一个动作。只有 BetTo 用得上 Amount。
type Action struct {
	Kind   ActionKind
	Amount int
}

func (a Action) String() string {
	if a.Kind == BetTo {
		return fmt.Sprintf("bet %d", a.Amount)
	}
	return a.Kind.String()
}

// actionAliases 是人在终端里敲的单字母快捷输入。
//
// 只有这里认别名。JSON 线路上的动作走 protocol.Command.Action()，那是另一个
// switch，每个动作永远只有一种拼法——写 agent 的人不必知道别名存在，也不会
// 收到两种写法要分别处理（ADR-0005 修订）。
//
// k 是 check、c 是 call，跟牌桌上的习惯走，别按首字母想当然：两个词都以 c
// 开头，而抢到 c 的是更常用的那个。
var actionAliases = map[string]string{
	"f": "fold",
	"k": "check",
	"c": "call",
	"b": "bet",
	"a": "allin",
}

// ParseAction 解析 "fold"、"call"、"bet 100" 这样的输入，也认 actionAliases 里的单字母。
func ParseAction(s string) (Action, error) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return Action{}, fmt.Errorf("空动作")
	}
	word := fields[0]
	if full, ok := actionAliases[word]; ok {
		word = full
	}
	switch word {
	case "fold":
		return Action{Kind: Fold}, requireNoArg(fields)
	case "check":
		return Action{Kind: Check}, requireNoArg(fields)
	case "call":
		return Action{Kind: Call}, requireNoArg(fields)
	case "allin":
		return Action{Kind: AllIn}, requireNoArg(fields)
	case "bet":
		if len(fields) != 2 {
			return Action{}, fmt.Errorf("bet 要跟一个数额，比如 bet 100（把本轮总投入推到 100）")
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			return Action{}, fmt.Errorf("%q 不是一个数额", fields[1])
		}
		if n <= 0 {
			return Action{}, fmt.Errorf("数额必须是正数")
		}
		return Action{Kind: BetTo, Amount: n}, nil
	default:
		return Action{}, fmt.Errorf("不认识的动作 %q，可用：fold(f)、check(k)、call(c)、bet <数额>(b)、allin(a)", fields[0])
	}
}

func requireNoArg(fields []string) error {
	if len(fields) > 1 {
		return fmt.Errorf("%s 不带参数", fields[0])
	}
	return nil
}

// Blinds 是建桌时定死的盲注（ADR 里 serve --blinds 1/2 的那两个数）。
type Blinds struct {
	Small int
	Big   int
}

func (b Blinds) String() string { return fmt.Sprintf("%d/%d", b.Small, b.Big) }

// ParseBlinds 解析 "1/2"。
func ParseBlinds(s string) (Blinds, error) {
	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return Blinds{}, fmt.Errorf("盲注要写成 小盲/大盲，比如 1/2")
	}
	small, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || small <= 0 {
		return Blinds{}, fmt.Errorf("小盲 %q 不是正整数", parts[0])
	}
	big, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || big <= 0 {
		return Blinds{}, fmt.Errorf("大盲 %q 不是正整数", parts[1])
	}
	if big < small {
		return Blinds{}, fmt.Errorf("大盲不能小于小盲")
	}
	return Blinds{Small: small, Big: big}, nil
}
