package poker

import "fmt"

// MaxSeats 是一张牌桌最多坐几个人。
//
// 9 是标准全环桌，位置命名那套惯例也正是按它定的。另外它顺带守住一条物理下限：
// 一副牌 52 张，n 个人要 2n+5 张，到 24 人就发不出来了——没有这个上限的话，
// 那是一次牌桌 goroutine 里的 panic，整个服务端跟着走。
const MaxSeats = 9

// Positions 给一手牌里的每个座位标出位置名，下标与座位一一对应。
//
// 位置是德州扑克里最重要的那个变量：同样两张牌，在 BTN 和在 UTG 是两手完全不同的牌。
// 它能从 button 和人数算出来，但那要照着下面这张惯例表来——所以由服务端算好给出去，
// 而不是让每个客户端和 agent 各自实现一遍（ADR-0007 那条「别让对方自己推导」）。
//
// 惯例是这样的：BTN 左手依次是 SB、BB，然后从 BB 左手开始按 preflop 行动顺序排。
// 中间那些座位从后往前贴 CO、HJ、LJ（它们是相对 BTN 定义的），剩下的从前往后
// 数 UTG、UTG+1……第一个永远叫 UTG，因为「under the gun」的意思就是第一个说话。
//
//	2 人  BTN/SB BB                              （单挑，button 自己就是小盲）
//	3 人  BTN SB BB
//	4 人  BTN SB BB UTG
//	5 人  BTN SB BB UTG CO
//	6 人  BTN SB BB UTG HJ CO
//	7 人  BTN SB BB UTG LJ HJ CO
//	8 人  BTN SB BB UTG UTG+1 LJ HJ CO
//	9 人  BTN SB BB UTG UTG+1 UTG+2 LJ HJ CO
//
// 六人桌那一行是这张表里唯一会让人愣一下的：按「从后往前」数，UTG 那个位置本该叫 LJ，
// 但它同时是第一个说话的人，而 UTG 这个名字说的就是这件事，所以它优先。
func Positions(seats, button int) []string {
	if seats < 2 {
		return nil
	}
	button = ((button % seats) + seats) % seats
	out := make([]string, seats)

	if seats == 2 {
		out[button] = "BTN/SB"
		out[(button+1)%seats] = "BB"
		return out
	}

	out[button] = "BTN"
	out[(button+1)%seats] = "SB"
	out[(button+2)%seats] = "BB"

	// 大盲左手到 button 之间的座位，按 preflop 行动顺序。
	middle := make([]int, 0, seats-3)
	for i := 3; i < seats; i++ {
		middle = append(middle, (button+i)%seats)
	}

	// 从后往前贴 CO / HJ / LJ。下标 0 留给 UTG，不参与。
	for i, name := range []string{"CO", "HJ", "LJ"} {
		idx := len(middle) - 1 - i
		if idx <= 0 {
			break
		}
		out[middle[idx]] = name
	}

	// 剩下的从前往后数。
	n := 0
	for _, seat := range middle {
		if out[seat] != "" {
			continue
		}
		if n == 0 {
			out[seat] = "UTG"
		} else {
			out[seat] = fmt.Sprintf("UTG+%d", n)
		}
		n++
	}
	return out
}
