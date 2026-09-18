// Package history 把每手牌的完整事实追加落盘（ADR-0008）。
//
// 它与牌桌状态是两回事，别混淆：状态是易失的运行时，进程一走就没了；
// 历史是只追加的事实日志，服务于 agent 的训练评测数据和 bug 复现。
// 让历史去承担恢复状态的职责，会立刻把状态迁移与版本兼容拖进来。
//
// 依赖方向是 history → poker，反向绝不允许（ADR-0012）。
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/xujnan/poker-cli/internal/poker"
)

// Seat 是一手牌开始前的一个座位。顺序就是牌桌顺序——重放时发牌要照着它走，
// 打乱了牌就发错人了。
type Seat struct {
	Player string `json:"player"`
	Stack  int    `json:"stack"`
}

// Record 是一手牌的完整记录，也就是历史文件里的一行。
//
// 它是自足的：Seed、Seats、Button、Blinds 四样凑齐就能把这一手单独重放出来，
// 不必先重放前面每一手（ADR-0016）。Verify 干的就是这件事。
type Record struct {
	Table string `json:"table"`
	Hand  int    `json:"hand"`
	Time  string `json:"time"`
	// Seed 是这一手牌自己的洗牌种子。
	Seed   uint64 `json:"seed"`
	Blinds string `json:"blinds"`
	Button string `json:"button"`
	Seats  []Seat `json:"seats"`

	// Hole 是所有人的底牌——上帝视角。历史文件是给牌桌主人事后分析用的，
	// 不是发给玩家的事件流，可见性那套规矩在这里不适用（也正因如此，别把它当事件发出去）。
	Hole      map[string][]poker.Card `json:"hole_cards"`
	Community []poker.Card            `json:"community_cards,omitempty"`
	Actions   []poker.ActionRecord    `json:"actions,omitempty"`

	Pots    []poker.Pot    `json:"pots,omitempty"`
	Winners []string       `json:"winners,omitempty"`
	Payout  map[string]int `json:"payout,omitempty"`
	Pot     int            `json:"pot"`
	Stacks  map[string]int `json:"stacks_after"`
}

// Of 把一手牌的结果整理成一条记录。
func Of(table string, seed uint64, blinds poker.Blinds, button string, before []Seat, at time.Time, res poker.HandResult) Record {
	return Record{
		Table:     table,
		Hand:      res.Number,
		Time:      at.Format(time.RFC3339Nano),
		Seed:      seed,
		Blinds:    blinds.String(),
		Button:    button,
		Seats:     append([]Seat(nil), before...),
		Hole:      res.Hole,
		Community: res.Community,
		Actions:   res.Actions,
		Pots:      res.Pots,
		Winners:   res.Winners,
		Payout:    res.Payout,
		Pot:       res.Pot,
		Stacks:    res.Stacks,
	}
}

// DeckFor 用记录里的种子造出这一手牌的牌堆。
//
// 重放和当初发牌走的必须是同一个函数，否则「种子可复现」就只是句口号。
func DeckFor(seed uint64) *poker.Deck { return poker.NewDeck(poker.RandFor(seed)) }

// DefaultPath 是一张牌桌默认的历史文件：<dir>/history/<CODE>-<开桌时间>.jsonl。
//
// 文件名带上开桌时间，是因为同一个 Table Code 会被反复用到（关掉再开就可能重号）。
// 混在一个文件里事后分不清是哪一场，而分不清的历史约等于没有历史。
func DefaultPath(dir, code string, at time.Time) string {
	return filepath.Join(dir, "history", fmt.Sprintf("%s-%s.jsonl", code, at.Format("20060102-150405")))
}

// Scan 逐条读一个历史文件，读一条给一条。
//
// 不一次性读进内存：这个文件是只追加的，跑久了会很大，而分析它的人多半只想扫一遍。
func Scan(path string, fn func(line int, r Record) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	for line := 1; ; line++ {
		var r Record
		if err := dec.Decode(&r); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("第 %d 条记录读不动: %w", line, err)
		}
		if err := fn(line, r); err != nil {
			return err
		}
	}
}
