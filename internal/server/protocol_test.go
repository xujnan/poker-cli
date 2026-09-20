package server

import (
	"encoding/json"
	"testing"

	"github.com/xujnan/poker-cli/internal/poker"
)

// TestTableEventCarriesProtocolVersion：agent 收到的第一条消息里必须写着协议版本。
//
// 版本号只有在第一条消息里才有用：agent 还没开始解析别的东西，就能决定认不认。
// 藏在后面某条事件里等于没有。
func TestTableEventCarriesProtocolVersion(t *testing.T) {
	s := startTable(t, 0)
	alice := dial(t, s, "alice")

	ev, raw, err := alice.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Type != poker.EventTable {
		t.Fatalf("坐下之后第一条该是 table，得到 %s", ev.Type)
	}
	if ev.Protocol != poker.ProtocolVersion {
		t.Fatalf("协议版本该是 %d，得到 %d", poker.ProtocolVersion, ev.Protocol)
	}
	// 这张桌的默认带入也必须在第一条事件里。--rebuy 靠它决定补到多少，
	// 缺了它，输光之后重连的人就永远补不上（见 TestRebuyWorksAfterReconnect）。
	if ev.Buyin != testBuyin {
		t.Fatalf("table 事件该带着默认带入 %d，得到 %d", testBuyin, ev.Buyin)
	}

	// 线路上那一行也得真有这个键——结构体里有、JSON 里被 omitempty 吃掉的话，
	// agent 什么也看不见。ProtocolVersion 是 0 的那天，这条会提醒你 omitempty 的坑。
	var onWire map[string]any
	if err := json.Unmarshal(raw, &onWire); err != nil {
		t.Fatal(err)
	}
	if got, ok := onWire["protocol"]; !ok {
		t.Fatalf("线路上没有 protocol 这个键：%s", raw)
	} else if int(got.(float64)) != poker.ProtocolVersion {
		t.Fatalf("线路上写的是 %v，该是 %d", got, poker.ProtocolVersion)
	}
}

// TestOnlyTableEventCarriesProtocol：别的事件不带版本号。
//
// 每条事件都塞一遍是白费带宽，更要紧的是它会让「版本是握手的一部分」这件事变模糊——
// 一个 agent 可能开始在第 500 条事件上检查版本，那时候它早就已经误解了前 499 条。
func TestOnlyTableEventCarriesProtocol(t *testing.T) {
	s := startTable(t, 0)
	alice := dial(t, s, "alice")
	events := autoPlay(t, alice, 1)
	bob := dial(t, s, "bob")
	autoPlay(t, bob, 1)

	for _, ev := range waitEvents(t, events) {
		if ev.Type == poker.EventTable {
			continue
		}
		if ev.Protocol != 0 {
			t.Fatalf("%s 事件不该带协议版本，却带了 %d", ev.Type, ev.Protocol)
		}
	}
}
