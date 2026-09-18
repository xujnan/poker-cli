package poker

import "testing"

// TestEvaluateCategories 用已知牌型逐个核对识别结果与 Tiebreak 的排列顺序。
//
// Tiebreak 的顺序是比较逻辑的全部依据，所以它比 Category 更值得逐位写死：
// 认错牌型会当场看出来，Tiebreak 排错位只会在某一手牌里悄悄判错赢家。
func TestEvaluateCategories(t *testing.T) {
	cases := []struct {
		name string
		hand string
		cat  Category
		tie  []Rank
	}{
		{"皇家同花顺", "As Ks Qs Js Ts", StraightFlush, []Rank{Ace}},
		{"轮子同花顺", "5s 4s 3s 2s As", StraightFlush, []Rank{Five}},
		{"四条带踢脚", "9c 9d 9h 9s 2c", FourOfAKind, []Rank{Nine, Two}},
		{"葫芦", "3c 3d 3h 8s 8c", FullHouse, []Rank{Three, Eight}},
		{"同花", "Ac Tc 8c 5c 2c", Flush, []Rank{Ace, Ten, Eight, Five, Two}},
		{"顺子", "9c 8d 7h 6s 5c", Straight, []Rank{Nine}},
		{"轮子顺子", "Ac 2d 3h 4s 5c", Straight, []Rank{Five}},
		{"三条", "7c 7d 7h Kc 2d", ThreeOfAKind, []Rank{Seven, King, Two}},
		{"两对", "Jc Jd 4h 4s 9c", TwoPair, []Rank{Jack, Four, Nine}},
		{"一对", "Ac Ad Kh 7s 2c", OnePair, []Rank{Ace, King, Seven, Two}},
		{"高牌", "Ac Kd 9h 7s 2c", HighCard, []Rank{Ace, King, Nine, Seven, Two}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Evaluate(MustParseCards(c.hand))
			if got.Category != c.cat {
				t.Fatalf("%s：牌型 = %v，想要 %v", c.hand, got.Category, c.cat)
			}
			if len(got.Tiebreak) != len(c.tie) {
				t.Fatalf("%s：Tiebreak = %v，想要 %v", c.hand, got.Tiebreak, c.tie)
			}
			for i := range c.tie {
				if got.Tiebreak[i] != c.tie[i] {
					t.Fatalf("%s：Tiebreak = %v，想要 %v", c.hand, got.Tiebreak, c.tie)
				}
			}
		})
	}
}

// TestWheelStraightIsTheLowestStraight 单独盯住 A-2-3-4-5：
// Ace 在这里当 1 用，是整个点数序里唯一的例外，也是最容易写错的一处。
func TestWheelStraightIsTheLowestStraight(t *testing.T) {
	wheel := Evaluate(MustParseCards("Ac 2d 3h 4s 5c"))
	six := Evaluate(MustParseCards("2c 3d 4h 5s 6c"))
	if wheel.Compare(six) >= 0 {
		t.Fatalf("轮子顺子应该输给 6 高顺子")
	}
	// Ace 还在，但这手牌的大小必须按 5 算，不能因为有 A 就被当成 A 高。
	aceHigh := Evaluate(MustParseCards("Ts Jd Qh Ks Ac"))
	if wheel.Compare(aceHigh) >= 0 {
		t.Fatalf("轮子顺子应该输给 A 高顺子")
	}
}

// TestCompareOrdering 核对牌型之间的强弱次序，以及同牌型内部的踢脚比较。
func TestCompareOrdering(t *testing.T) {
	cases := []struct {
		name         string
		strong, weak string
	}{
		{"同花顺 > 四条", "9s 8s 7s 6s 5s", "9c 9d 9h 9s 2c"},
		{"四条 > 葫芦", "2c 2d 2h 2s 3c", "Ac Ad Ah Ks Kc"},
		{"葫芦 > 同花", "2c 2d 2h 3s 3c", "Ac Tc 8c 5c 2c"},
		{"同花 > 顺子", "2c 5c 8c Tc Qc", "9c 8d 7h 6s 5c"},
		{"顺子 > 三条", "9c 8d 7h 6s 5c", "Ac Ad Ah Ks 2c"},
		{"三条 > 两对", "2c 2d 2h 3s 4c", "Ac Ad Kh Ks 2c"},
		{"两对 > 一对", "2c 2d 3h 3s 4c", "Ac Ad Kh Qs Jc"},
		{"一对 > 高牌", "2c 2d 3h 4s 5c", "Ac Kd Qh Js 9c"},
		{"同花比最大张", "As Ts 8s 5s 2s", "Ks Qs 8s 5s 2s"},
		{"对子相同比第一踢脚", "Ac Ad Kh 7s 2c", "Ac Ad Qh 7s 2c"},
		{"对子相同比末位踢脚", "Ac Ad Kh 7s 3c", "Ac Ad Kh 7s 2c"},
		{"两对比小对", "Jc Jd 5h 5s 2c", "Jc Jd 4h 4s Ac"},
		{"四条比踢脚", "9c 9d 9h 9s Kc", "9c 9d 9h 9s Qc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			strong, weak := Evaluate(MustParseCards(c.strong)), Evaluate(MustParseCards(c.weak))
			if strong.Compare(weak) != 1 {
				t.Fatalf("%s 应该强于 %s", c.strong, c.weak)
			}
			if weak.Compare(strong) != -1 {
				t.Fatalf("Compare 必须反对称：%s vs %s", c.weak, c.strong)
			}
		})
	}
}

// TestCompareExactTie 完全平局必须返回 0——花色不参与比较，
// 而 0 是「平分底池」这条分支唯一的入口。
func TestCompareExactTie(t *testing.T) {
	cases := [][2]string{
		{"Ac Kd Qh Js Tc", "Ad Kh Qs Jc Th"}, // 同一个顺子，花色不同
		{"Ac Ad Kh Qs 2c", "Ah As Kd Qc 2d"}, // 同一对 A 同样踢脚
		{"2c 3d 4h 5s 7c", "2d 3h 4s 5c 7d"}, // 同一手高牌
	}
	for _, c := range cases {
		a, b := Evaluate(MustParseCards(c[0])), Evaluate(MustParseCards(c[1]))
		if a.Compare(b) != 0 || b.Compare(a) != 0 {
			t.Fatalf("%s 与 %s 应当完全平局", c[0], c[1])
		}
	}
}

// TestEvaluateSevenPicksBestFive 七张牌里挑五张——摊牌时真正跑的就是这条路。
func TestEvaluateSevenPicksBestFive(t *testing.T) {
	cases := []struct {
		name  string
		seven string
		cat   Category
		tie   []Rank
	}{
		{"七张里藏着同花顺", "As Ks Qs Js Ts 2c 3d", StraightFlush, []Rank{Ace}},
		{"要跳过对子去凑同花", "Ac Tc 8c 5c 2c 8d 8h", Flush, []Rank{Ace, Ten, Eight, Five, Two}},
		{"三对只能取两对，第三对退化成踢脚", "Ac Ad Kh Ks 2c 2d 7h", TwoPair, []Rank{Ace, King, Seven}},
		{"顺子横跨底牌与公共牌", "9c 8d 2h 7s 6c 5d Ah", Straight, []Rank{Nine}},
		{"打公共牌：两张废牌配一副皇家同花顺", "2c 3d As Ks Qs Js Ts", StraightFlush, []Rank{Ace}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Evaluate(MustParseCards(c.seven))
			if got.Category != c.cat {
				t.Fatalf("牌型 = %v，想要 %v", got.Category, c.cat)
			}
			for i := range c.tie {
				if i >= len(got.Tiebreak) || got.Tiebreak[i] != c.tie[i] {
					t.Fatalf("Tiebreak = %v，想要 %v", got.Tiebreak, c.tie)
				}
			}
			if len(got.Best) != 5 {
				t.Fatalf("Best 必须是五张牌，得到 %d 张", len(got.Best))
			}
		})
	}
}
