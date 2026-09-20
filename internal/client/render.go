package client

import (
	"fmt"
	"strings"

	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/textui"
)

// Render 把一条事件渲染成一行（或几行）中文。
//
// 人看到的文本和 agent 收到的 JSONL 是同一个 Event 对象的两种投影（ADR-0002）：
// 渲染器只能读事件里已有的字段，不能自己去问服务端要补充信息——真那么做了，
// 两种格式给出的事实就会开始分岔。
func Render(ev poker.Event) string {
	switch ev.Type {
	case poker.EventTable:
		return fmt.Sprintf("已加入牌桌（盲注 %s），在座：%s", ev.Blinds, seatLine(ev.Seats))
	case poker.EventJoined:
		return fmt.Sprintf("* %s 加入了牌桌", ev.Player)
	case poker.EventLeft:
		return fmt.Sprintf("* %s 离开了牌桌", ev.Player)
	case poker.EventSitOut:
		return fmt.Sprintf("* %s 暂离（%s），座位和筹码留着", ev.Player, ev.Message)
	case poker.EventSitIn:
		return fmt.Sprintf("* %s 回座了", ev.Player)
	case poker.EventTopUp:
		if ev.Stack == nil {
			return fmt.Sprintf("* %s 补码 %d（%s）", ev.Player, ev.Amount, ev.Message)
		}
		return fmt.Sprintf("* %s 补码 %d，现在有 %d", ev.Player, ev.Amount, *ev.Stack)

	case poker.EventHandStart:
		return fmt.Sprintf("\n── 第 %d 手 ──  庄家 %s，盲注 %s\n   %s",
			ev.Hand, ev.Button, ev.Blinds, seatLine(ev.Seats))
	case poker.EventBlind:
		name := "小盲"
		if ev.Action == "big_blind" {
			name = "大盲"
		}
		return fmt.Sprintf("%s %s %d%s", ev.Player, name, ev.Amount, allinSuffix(ev))
	case poker.EventHoleCards:
		// hole_cards 只会投递给它的主人，所以这里说「你的」永远没错。
		return fmt.Sprintf("你的底牌：%s", cards(ev.Cards))

	case poker.EventTurn:
		// 滚动那一版不渲染它：下一条就是那个人的动作，中间插一句「轮到谁」只是噪音。
		// 重画那一版用得上——它要在座位表上标出牌桌在等谁（见 live.go）。
		// 显式列在这里而不是落到 default，是因为 default 会打「(未知事件 turn)」。
		return ""
	case poker.EventYourTurn:
		return renderTurn(ev.Player, ev.Snapshot)
	case poker.EventAction:
		return renderAction(ev)
	case poker.EventStreet:
		return fmt.Sprintf("\n%s：%s   底池 %d", streetName(ev.Street), cards(ev.Board), potOf(ev))

	case poker.EventShowdown:
		lines := make([]string, 0, len(ev.Showdown)+1)
		lines = append(lines, "摊牌：")
		for _, e := range ev.Showdown {
			lines = append(lines, fmt.Sprintf("  %s %s  →  %s（%s）",
				textui.Pad(e.Player, 10), cards(e.Cards), e.Category, cards(e.Best)))
		}
		return strings.Join(lines, "\n")
	case poker.EventPotAwarded:
		lines := make([]string, 0, len(ev.Pots))
		for i, pot := range ev.Pots {
			label := "底池"
			if i > 0 {
				label = fmt.Sprintf("边池 %d", i)
			}
			lines = append(lines, fmt.Sprintf("%s %d → %s", label, pot.Amount, strings.Join(pot.Winners, "、")))
		}
		return strings.Join(lines, "\n")
	case poker.EventHandEnd:
		return fmt.Sprintf("这手牌结束，底池 %d。%s", potOf(ev), seatLine(ev.Seats))

	case poker.EventError:
		return fmt.Sprintf("✗ [%s] %s", ev.Code, ev.Message)
	default:
		// 不认识的事件也要露个面：静默丢弃会让人以为牌桌卡住了。
		return fmt.Sprintf("(未知事件 %s)", ev.Type)
	}
}

// renderTurn 是人类玩家最需要看清楚的一屏：牌、池、要跟多少、能做什么。
func renderTurn(me string, snap *poker.Snapshot) string {
	if snap == nil {
		return "轮到你了"
	}
	var b strings.Builder
	// 位置跟街名并排放在最显眼的地方：同样两张牌，在 BTN 和在 UTG 是两手完全不同的牌。
	where := streetName(snap.Street)
	for _, s := range snap.Seats {
		if s.Player == me && s.Position != "" {
			where += "，" + s.Position
			break
		}
	}
	fmt.Fprintf(&b, "\n轮到你了（%s）\n", where)
	fmt.Fprintf(&b, "  底牌 %s", cards(snap.Hole))
	if len(snap.Community) > 0 {
		fmt.Fprintf(&b, "   公共牌 %s", cards(snap.Community))
	}
	fmt.Fprintf(&b, "\n  底池 %d   你的筹码 %d", snap.Pot, snap.Stack)
	if snap.ToCall > 0 {
		fmt.Fprintf(&b, "   要跟 %d", snap.ToCall)
	}
	b.WriteString("\n  可以：")
	opts := make([]string, 0, len(snap.Legal))
	for _, l := range snap.Legal {
		switch l.Action {
		case "call":
			opts = append(opts, fmt.Sprintf("call（跟 %d）", l.Amount))
		case "bet":
			opts = append(opts, fmt.Sprintf("bet <%d-%d>", l.Min, l.Max))
		case "allin":
			opts = append(opts, fmt.Sprintf("allin（推 %d）", l.Amount))
		default:
			opts = append(opts, l.Action)
		}
	}
	b.WriteString(strings.Join(opts, " / "))
	return b.String()
}

// renderAction 是滚动那一版的一行：谁 + 做了什么，中间一个空格。
func renderAction(ev poker.Event) string {
	line := ev.Player + " " + actionWhat(ev)
	if pot := actionPot(ev); pot != "" {
		line += "，" + pot
	}
	return line
}

// actionPot 是这个动作之后底池变成了多少，没有可说的就返回空串。
//
// 跟动作本身分开，是因为重画那一版把它排成单独一列（见 live.go），
// 而滚动那一版拼回「下注到 2，底池 8」这样的一句话。
func actionPot(ev poker.Event) string {
	if ev.Pot == nil || ev.Action == "fold" || ev.Action == "check" {
		return ""
	}
	return fmt.Sprintf("底池 %d", *ev.Pot)
}

// actionWhat 只说「做了什么」，不带人名。
//
// 跟人名分开是给重画那一版用的：它要把人名补成一列宽，好让动作那一列对齐
// （见 live.go 的 logLine）。滚动那一版拼回去，输出一个字节都不变。
func actionWhat(ev poker.Event) string {
	var what string
	switch ev.Action {
	case "fold":
		what = "弃牌"
	case "check":
		what = "过牌"
	case "call":
		what = fmt.Sprintf("跟注 %d", ev.Amount)
	case "bet":
		what = fmt.Sprintf("下注到 %d", ev.Committed)
	case "allin":
		what = fmt.Sprintf("全下 %d（本轮共 %d）", ev.Amount, ev.Committed)
	default:
		what = ev.Action
	}
	if ev.Forced {
		what += "（超时代打）"
	}
	return what
}

func streetName(s string) string {
	switch s {
	case "preflop":
		return "翻牌前"
	case "flop":
		return "翻牌"
	case "turn":
		return "转牌"
	case "river":
		return "河牌"
	}
	return s
}

func allinSuffix(ev poker.Event) string {
	if ev.Stack != nil && *ev.Stack == 0 {
		return "（已全下）"
	}
	return ""
}

func potOf(ev poker.Event) int {
	if ev.Pot == nil {
		return 0
	}
	return *ev.Pot
}

// seatLine 把各家筹码排成一行。这里只渲染 SeatView 里有的东西——
// 那个结构里根本没有放底牌的地方，渲染器也就没有机会把别人的牌印出来。
func seatLine(seats []poker.SeatView) string {
	if len(seats) == 0 {
		return ""
	}
	parts := make([]string, 0, len(seats))
	for _, s := range seats {
		tag := ""
		switch {
		case s.Folded:
			tag = " 弃"
		case s.AllIn:
			tag = " 全下"
		case s.SittingOut:
			tag = " 暂离"
		}
		// 位置只在牌局进行中有值：两手牌之间没有庄家位，也就没有位置可言。
		pos := ""
		if s.Position != "" {
			pos = " " + s.Position
		}
		parts = append(parts, fmt.Sprintf("%s%s %d%s", s.Player, pos, s.Stack, tag))
	}
	return strings.Join(parts, " | ")
}

func cards(cs []poker.Card) string { return textui.Cards(cs) }
