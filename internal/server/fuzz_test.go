package server

import (
	"io"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/xujnan/poker-cli/internal/client"
	"github.com/xujnan/poker-cli/internal/poker"
	"github.com/xujnan/poker-cli/internal/protocol"
	"github.com/xujnan/poker-cli/internal/transport"
)

// FuzzClientCommands 往牌桌上乱发命令，断言它从不崩。
//
// 这条钉的是一句我之前只是「读代码读出来」的断言：poker 包里那几处 panic 是契约
// 断言，网络上进来的东西碰不到它们。读一遍代码然后相信它，和每次 push 都用新输入
// 试一遍，是两回事——尤其因为违反它的后果不是报错，是整个进程连同没落盘的历史一起没。
//
// 乱发的不只是坏 JSON（那太容易挡了），还有**结构完全合法、只是顺序和数额离谱**的
// 命令：没轮到你就 bet、bet 一个负数、bet 一个大到溢出的数、刚 join 就 sitout 再
// sitin 再 topup。状态机的坑在这一类里，不在 JSON 解析里。
func FuzzClientCommands(f *testing.F) {
	// 种子语料挑的是几类「像样但不对」的输入；go test 不加 -fuzz 时只跑这些。
	f.Add([]byte{0})                                     // 什么都不发
	f.Add([]byte{1, 200, 2, 3})                          // 没轮到就下注
	f.Add([]byte{4, 255, 255, 255, 255})                 // 离谱的数额
	f.Add([]byte{7, 7, 7, 8, 8, 8})                      // 反复 sitout/sitin
	f.Add([]byte("{\"type\":\"join\"}\n\n\n"))           // 重复 join 和空行
	f.Add([]byte("这不是 JSON\n{\"type\":}\n"))             // 坏 JSON
	f.Add([]byte{3, 0, 3, 0, 3, 0, 3, 0, 3, 0, 3, 0})    // 连续非法动作，撞重试上限
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}) // 每种命令各来一遍

	f.Fuzz(func(t *testing.T, data []byte) {
		tr := transport.NewMemory()
		s, err := New(Options{
			Transport:     tr,
			Rand:          rand.New(rand.NewPCG(1, 2)),
			HandDelay:     0,
			ActionTimeout: 20 * time.Millisecond,
			Blinds:        poker.Blinds{Small: 1, Big: 2},
			Buyin:         testBuyin,
			Log:           io.Discard,
		})
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = s.Serve() }()

		// 一个正常的对手，好让牌局真的推进起来——只有一个捣乱的客户端的话，
		// 牌根本发不出去，那就什么都没测到。
		good, err := client.Dial(tr, s.Code(), "good", 0)
		if err != nil {
			t.Fatal(err)
		}
		defer good.Close()
		go func() {
			for {
				ev, _, err := good.Next()
				if err != nil {
					return
				}
				if ev.Type == poker.EventYourTurn && ev.Snapshot != nil {
					if good.Send(protocol.CommandOf(client.Decide(ev.Snapshot))) != nil {
						return
					}
				}
			}
		}()

		bad, err := client.Dial(tr, s.Code(), "bad", 0)
		if err != nil {
			t.Fatal(err)
		}
		defer bad.Close()
		// 捣乱的那个也要把事件读掉：读不走的话服务端的发送缓冲会满，
		// 它就被断开了，后面发的命令一条都到不了。
		go func() {
			for {
				if _, _, err := bad.Next(); err != nil {
					return
				}
			}
		}()

		for _, cmd := range commandsFrom(data) {
			if err := bad.Send(cmd); err != nil {
				break
			}
		}
		// 给牌桌一点时间把这些消化掉，顺带让超时和下一手牌也跑起来。
		time.Sleep(30 * time.Millisecond)

		if err := s.Close(); err != nil {
			t.Fatalf("发了这些之后牌桌垮了：%v\n输入：%q", err, data)
		}
	})
}

// commandsFrom 把一段随便什么字节翻译成一串命令。
//
// 不直接把原始字节灌进 socket，是因为那样绝大多数输入都停在 JSON 解析上，
// 摸不到状态机。这里让每个字节挑一种命令类型、后面几个字节凑出数额，
// 于是发出去的是结构合法、语义乱来的命令——那才是状态机会踩到的东西。
func commandsFrom(data []byte) []protocol.Command {
	types := []protocol.CommandType{
		protocol.CmdJoin, protocol.CmdFold, protocol.CmdCheck, protocol.CmdCall,
		protocol.CmdBet, protocol.CmdAllIn, protocol.CmdTopUp,
		protocol.CmdSitOut, protocol.CmdSitIn, protocol.CmdQuit,
		"", "没听说过这种命令",
	}
	var out []protocol.Command
	for i := 0; i < len(data) && len(out) < 64; {
		c := protocol.Command{Type: types[int(data[i])%len(types)]}
		i++
		// 数额从后面最多四个字节里凑，凑不满就用有多少算多少——
		// 负数和极大值都要能出现，它们正是最容易把算术弄翻的两头。
		amount := 0
		for n := 0; n < 4 && i < len(data); n++ {
			amount = amount<<8 | int(data[i])
			i++
		}
		if amount%3 == 0 {
			amount = -amount
		}
		c.Amount = amount
		c.Buyin = amount
		c.Name = "bad"
		out = append(out, c)
	}
	return out
}
