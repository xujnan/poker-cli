package client

import (
	"fmt"
	"strings"

	"github.com/xujnan/poker-cli/internal/poker"
)

// Render 把一条事件渲染成一行（或几行）中文。
//
// 人看到的文本和 agent 收到的 JSONL 是同一个 Event 对象的两种投影（ADR-0002）：
// 渲染器只能读事件里已有的字段，不能自己去问服务端要补充信息——真那么做了，
// 两种格式给出的事实就会开始分岔。
func Render(ev poker.Event) string {
	switch ev.Type {
	case poker.EventTable:
		return fmt.Sprintf("已加入牌桌，在座：%s", join(ev.Players))
	case poker.EventJoined:
		return fmt.Sprintf("* %s 加入了牌桌（在座 %d 人）", ev.Player, len(ev.Players))
	case poker.EventLeft:
		return fmt.Sprintf("* %s 离开了牌桌（在座 %d 人）", ev.Player, len(ev.Players))
	case poker.EventHandStart:
		return fmt.Sprintf("\n── 第 %d 手 ──  %s", ev.Hand, strings.Join(ev.Players, " vs "))
	case poker.EventHoleCards:
		// hole_cards 只会投递给它的主人，所以这里说「你的」永远没错。
		return fmt.Sprintf("你的底牌：%s", cards(ev.Cards))
	case poker.EventCommunityCards:
		return fmt.Sprintf("公共牌：  %s", cards(ev.Cards))
	case poker.EventShowdown:
		lines := make([]string, 0, len(ev.Showdown)+1)
		lines = append(lines, "摊牌：")
		for _, e := range ev.Showdown {
			lines = append(lines, fmt.Sprintf("  %-10s %s  →  %s（%s）", e.Player, cards(e.Cards), e.Category, cards(e.Best)))
		}
		return strings.Join(lines, "\n")
	case poker.EventHandEnd:
		pot := 0
		if ev.Pot != nil {
			pot = *ev.Pot
		}
		switch len(ev.Winners) {
		case 0:
			return "这手牌没有赢家"
		case 1:
			return fmt.Sprintf("%s 赢下这手牌（底池 %d）", ev.Winners[0], pot)
		default:
			return fmt.Sprintf("%s 平分底池（底池 %d）", join(ev.Winners), pot)
		}
	case poker.EventError:
		return fmt.Sprintf("错误 [%s] %s", ev.Code, ev.Message)
	default:
		// 不认识的事件也要露个面：静默丢弃会让人以为牌桌卡住了。
		return fmt.Sprintf("(未知事件 %s)", ev.Type)
	}
}

func cards(cs []poker.Card) string {
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.String()
	}
	return strings.Join(parts, " ")
}

func join(names []string) string { return strings.Join(names, "、") }
