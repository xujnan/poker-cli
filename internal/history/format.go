package history

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/xujnan/poker-cli/internal/poker"
)

// Format 把一条记录渲染成人能读的复盘。
//
// 这跟 Verify 是两种读法：Verify 关心「还原不还原得回去」，这里关心「当时发生了什么」。
// 记录是上帝视角，所以复盘里所有人的底牌都摊开——事后翻记录的人本来就该看得到全部，
// 发给玩家的事件流才是要守可见性的那个（ADR-0006）。
func Format(r Record) string {
	var b strings.Builder

	when := r.Time
	if t, err := time.Parse(time.RFC3339Nano, r.Time); err == nil {
		when = t.Local().Format("2006-01-02 15:04:05")
	}
	fmt.Fprintf(&b, "── 第 %d 手 ──  %s  %s  盲注 %s  庄家 %s\n", r.Hand, r.Table, when, r.Blinds, r.Button)
	fmt.Fprintf(&b, "   种子 %d\n", r.Seed)

	stacks := make([]string, 0, len(r.Seats))
	holes := make([]string, 0, len(r.Seats))
	for _, s := range r.Seats {
		stacks = append(stacks, fmt.Sprintf("%s %d", s.Player, s.Stack))
		holes = append(holes, fmt.Sprintf("%s %s", s.Player, cards(r.Hole[s.Player])))
	}
	fmt.Fprintf(&b, "   筹码  %s\n", strings.Join(stacks, " | "))
	fmt.Fprintf(&b, "   底牌  %s\n", strings.Join(holes, " | "))

	// 按街分组。哪几张公共牌属于哪条街可以从张数推出来，不必另记。
	byStreet := map[string][]poker.ActionRecord{}
	for _, a := range r.Actions {
		byStreet[a.Street] = append(byStreet[a.Street], a)
	}
	boards := []struct {
		street string
		cards  []poker.Card
		need   int
	}{
		{"preflop", nil, 0},
		{"flop", cardsAt(r.Community, 0, 3), 3},
		{"turn", cardsAt(r.Community, 3, 4), 4},
		{"river", cardsAt(r.Community, 4, 5), 5},
	}
	for _, s := range boards {
		if len(r.Community) < s.need {
			break
		}
		header := streetName(s.street)
		if len(s.cards) > 0 {
			header += "  " + cards(s.cards)
		}
		fmt.Fprintf(&b, "\n   %s\n", header)
		for _, a := range byStreet[s.street] {
			fmt.Fprintf(&b, "     %s\n", formatAction(a))
		}
	}

	// 摊牌。谁弃了牌从动作里看得出来，不必另记一份。
	folded := map[string]bool{}
	for _, a := range r.Actions {
		if a.Action == "fold" {
			folded[a.Player] = true
		}
	}
	var shown []string
	if len(r.Community) == 5 {
		for _, s := range r.Seats {
			if folded[s.Player] {
				continue
			}
			seven := append(append([]poker.Card(nil), r.Hole[s.Player]...), r.Community...)
			if len(seven) < 5 {
				continue
			}
			rank := poker.Evaluate(seven)
			shown = append(shown, fmt.Sprintf("     %s %s → %s（%s）",
				s.Player, cards(r.Hole[s.Player]), rank.Category, cards(rank.Best)))
		}
	}
	if len(shown) > 1 {
		fmt.Fprintf(&b, "\n   摊牌\n%s\n", strings.Join(shown, "\n"))
	}

	b.WriteString("\n")
	for i, pot := range r.Pots {
		label := "底池"
		if i > 0 {
			label = fmt.Sprintf("边池 %d", i)
		}
		fmt.Fprintf(&b, "   %s %d → %s\n", label, pot.Amount, strings.Join(pot.Winners, "、"))
	}

	after := make([]string, 0, len(r.Seats))
	for _, s := range r.Seats {
		delta := r.Stacks[s.Player] - s.Stack
		after = append(after, fmt.Sprintf("%s %d (%+d)", s.Player, r.Stacks[s.Player], delta))
	}
	fmt.Fprintf(&b, "   结束  %s\n", strings.Join(after, " | "))
	return b.String()
}

func formatAction(a poker.ActionRecord) string {
	var what string
	switch a.Action {
	case "fold":
		what = "弃牌"
	case "check":
		what = "过牌"
	case "call":
		what = fmt.Sprintf("跟注 %d", a.Amount)
	case "bet":
		what = fmt.Sprintf("下注到 %d", a.To)
	case "allin":
		what = fmt.Sprintf("全下 %d（本轮共 %d）", a.Amount, a.To)
	default:
		what = a.Action
	}
	line := fmt.Sprintf("%-10s %s", a.Player, what)
	if a.Forced {
		line += "（代打）"
	}
	return line
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

func cardsAt(cs []poker.Card, from, to int) []poker.Card {
	if len(cs) < to {
		return nil
	}
	return cs[from:to]
}

func cards(cs []poker.Card) string {
	if len(cs) == 0 {
		return "-"
	}
	parts := make([]string, len(cs))
	for i, c := range cs {
		parts[i] = c.String()
	}
	return strings.Join(parts, " ")
}

// PlayerStats 是一个玩家在一段历史里的战绩。
type PlayerStats struct {
	Player string
	// Hands 是他上过桌的手数，Won 是其中赢到钱的手数。
	Hands int
	Won   int
	// Net 是净筹码。它按「每手结束时的筹码减开局时的筹码」累加，
	// 所以补码不会被算成盈利——补码发生在两手牌之间，不在任何一手的账里（ADR-0015）。
	Net int
}

// Summary 边扫边统计，不必把整个历史读进内存。
type Summary struct {
	Hands int
	byWho map[string]*PlayerStats
	order []string
}

// NewSummary 造一个空的统计。
func NewSummary() *Summary {
	return &Summary{byWho: make(map[string]*PlayerStats)}
}

// Add 把一手牌算进去。
func (s *Summary) Add(r Record) {
	s.Hands++
	for _, seat := range r.Seats {
		st, ok := s.byWho[seat.Player]
		if !ok {
			st = &PlayerStats{Player: seat.Player}
			s.byWho[seat.Player] = st
			s.order = append(s.order, seat.Player)
		}
		st.Hands++
		st.Net += r.Stacks[seat.Player] - seat.Stack
		if r.Payout[seat.Player] > 0 {
			st.Won++
		}
	}
}

// Players 按净筹码从多到少列出战绩。
func (s *Summary) Players() []PlayerStats {
	out := make([]PlayerStats, 0, len(s.byWho))
	for _, name := range s.order {
		out = append(out, *s.byWho[name])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Net > out[j].Net })
	return out
}

// String 把战绩排成一张表。
func (s *Summary) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "共 %d 手牌\n\n", s.Hands)
	fmt.Fprintf(&b, "%-14s %6s %6s %8s\n", "玩家", "手数", "赢", "净筹码")
	for _, p := range s.Players() {
		fmt.Fprintf(&b, "%-14s %6d %6d %+8d\n", p.Player, p.Hands, p.Won, p.Net)
	}
	return b.String()
}
