package history

import (
	"fmt"

	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/textui"
)

// Verify 用记录里的种子和动作把这一手牌原样重放一遍，确认结果对得上。
//
// 一条记录之所以能单独重放，是因为它自足：种子、座位顺序、Button、盲注四样凑齐
// 就够了，不必先重放前面每一手（ADR-0016）。这也是这份历史「能用来复现 bug」
// 这句话的兑现方式——没有校验，谁也不知道记下来的东西还原不还原得回去。
func Verify(r Record) (err error) {
	// 历史文件是外面来的数据，可能被截断、被手改。纯核心对不合法的输入是 panic 的
	// （那是给调用方算错了准备的），在这里得把它翻译成一条错误，而不是让校验工具崩掉。
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("重放时崩了：%v", p)
		}
	}()

	seats := make([]poker.Seat, len(r.Seats))
	button := -1
	for i, s := range r.Seats {
		seats[i] = poker.Seat{Player: s.Player, Stack: s.Stack}
		if s.Player == r.Button {
			button = i
		}
	}
	if len(seats) < 2 {
		return fmt.Errorf("只有 %d 个座位", len(seats))
	}
	if button < 0 {
		return fmt.Errorf("庄家 %q 不在座位表里", r.Button)
	}
	blinds, err := poker.ParseBlinds(r.Blinds)
	if err != nil {
		return fmt.Errorf("盲注 %q 解析不了: %w", r.Blinds, err)
	}

	hand, events := poker.NewHand(r.Hand, seats, button, blinds, DeckFor(r.Seed))

	// 先比底牌。发牌就对不上的话，问题在种子，跟后面的动作无关——
	// 分开报能省掉一轮排查。
	for _, ev := range events {
		if ev.Type != poker.EventHoleCards {
			continue
		}
		if err := sameCards(r.Hole[ev.Player], ev.Cards); err != nil {
			return fmt.Errorf("按种子重放，%s 的底牌对不上：%w", ev.Player, err)
		}
	}

	for i, a := range r.Actions {
		if hand.Done() {
			return fmt.Errorf("第 %d 个动作（%s %s）之前牌局就已经结束了", i+1, a.Player, a.Action)
		}
		if turn := hand.Turn(); turn != a.Player {
			return fmt.Errorf("第 %d 个动作记的是 %s，但按规则该轮到 %s", i+1, a.Player, turn)
		}
		var got []poker.Event
		if a.Forced {
			got = hand.ApplyForced(a.Player, "重放")
		} else {
			act, err := actionOf(a)
			if err != nil {
				return fmt.Errorf("第 %d 个动作: %w", i+1, err)
			}
			got = hand.Apply(a.Player, act)
		}
		for _, ev := range got {
			if ev.Type == poker.EventError {
				return fmt.Errorf("第 %d 个动作（%s %s）重放时被判非法：%s", i+1, a.Player, a.Action, ev.Message)
			}
		}
	}
	if !hand.Done() {
		return fmt.Errorf("动作都重放完了，牌局却还没结束（记录被截断了？）")
	}

	res := hand.Result()
	if err := sameCards(r.Community, res.Community); err != nil {
		return fmt.Errorf("公共牌对不上：%w", err)
	}
	for player, want := range r.Hole {
		if err := sameCards(want, res.Hole[player]); err != nil {
			return fmt.Errorf("%s 的底牌对不上：%w", player, err)
		}
	}
	if r.Pot != res.Pot {
		return fmt.Errorf("底池对不上：记的是 %d，重放出来是 %d", r.Pot, res.Pot)
	}
	for player, want := range r.Stacks {
		if got := res.Stacks[player]; got != want {
			return fmt.Errorf("%s 结束时的筹码对不上：记的是 %d，重放出来是 %d", player, want, got)
		}
	}
	if err := sameNames(r.Winners, res.Winners); err != nil {
		return fmt.Errorf("赢家对不上：%w", err)
	}
	return nil
}

// actionOf 把记录里的动作翻回成一个可以施加的动作。
//
// bet 用的是 To 而不是 Amount：bet 是「推到多少」的语义（ADR-0005），
// 拿增量去重放会越打越小。
func actionOf(a poker.ActionRecord) (poker.Action, error) {
	switch a.Action {
	case "fold":
		return poker.Action{Kind: poker.Fold}, nil
	case "check":
		return poker.Action{Kind: poker.Check}, nil
	case "call":
		return poker.Action{Kind: poker.Call}, nil
	case "allin":
		return poker.Action{Kind: poker.AllIn}, nil
	case "bet":
		if a.To <= 0 {
			return poker.Action{}, fmt.Errorf("bet 记录里没有 to")
		}
		return poker.Action{Kind: poker.BetTo, Amount: a.To}, nil
	}
	return poker.Action{}, fmt.Errorf("不认识的动作 %q", a.Action)
}

// sameCards 比两串牌。报错时按终端的样子印（A♠），不是按文件里的样子印（As）——
// 这条消息是给人读的，而排查时是按手号和种子去找那一手，不会拿牌去 grep 文件。
func sameCards(want, got []poker.Card) error {
	mismatch := func() error {
		return fmt.Errorf("记的是 %s，重放出来是 %s", textui.Cards(want), textui.Cards(got))
	}
	if len(want) != len(got) {
		return mismatch()
	}
	for i := range want {
		if want[i] != got[i] {
			return mismatch()
		}
	}
	return nil
}

func sameNames(want, got []string) error {
	if len(want) != len(got) {
		return fmt.Errorf("记的是 %v，重放出来是 %v", want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			return fmt.Errorf("记的是 %v，重放出来是 %v", want, got)
		}
	}
	return nil
}
