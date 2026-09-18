package poker

import "fmt"

const (
	holeCardCount = 2
	// communityCardCount 是一手牌最多翻几张公共牌：翻牌 3 张、转牌河牌各 1 张。
	communityCardCount = 5
	// maxIllegalActions 是同一轮里能连续犯几次非法动作。到第三次就替他做决定（ADR-0011）：
	// 无限重试会让一个写坏的 agent 把牌桌卡死，而直接判负又让它无从纠错。
	maxIllegalActions = 3
)

// Seat 是进入一手牌时的一个座位。Stack 的长期存续归牌桌管，这里只借用一手牌的工夫。
type Seat struct {
	Player string
	Stack  int
}

// seatState 是一个座位在这手牌里的全部状态。
type seatState struct {
	player string
	stack  int
	// committed 是本手牌的总投入，street 是本 Street 的投入。分池要前者，比注要后者。
	committed int
	street    int
	// acted 记录本 Street 上他是否已经行动过。有人足额加注时，其他人的 acted 会被清掉——
	// 大盲在 preflop 的那次 option 也靠它：盲注是被收走的，不算他行动过。
	acted  bool
	folded bool
	allin  bool
	hole   []Card
}

// ActionRecord 是手牌历史里的一个动作（CONTEXT 里 Hand History 要求的「每个 Action」）。
//
// 它记的是已经发生的事实，不是命令。两个数额都留着，因为它们各有各的用处：
// Amount 是这一下实际投进去多少筹码，看牌局流水时要的是它；
// To 是他在这条街上的投入因此变成了多少，重放时要的是它——bet 是「推到多少」的语义（ADR-0005），
// 只记增量就没法原样重放。
type ActionRecord struct {
	Player string `json:"player"`
	Street string `json:"street"`
	Action string `json:"action"`
	Amount int    `json:"amount,omitempty"`
	To     int    `json:"to,omitempty"`
	// Forced 表示这一下不是他自己按的（超时、掉线、或连续非法之后代打）。
	Forced bool `json:"forced,omitempty"`
}

// HandResult 是一手牌打完之后的全部事实，包括所有人的底牌。
//
// 它与事件序列的区别是可见性：事件是分别投递给各个玩家的、经过裁剪的视图，
// HandResult 则是上帝视角，只给服务端自己用（将来写 Hand History，ADR-0008）。
// 千万别顺手把它整个塞进某个广播事件里。
type HandResult struct {
	Number    int
	Players   []string
	Hole      map[string][]Card
	Community []Card
	// Ranks 只含摊牌的人。弃牌者不在里面，他的牌从头到尾没参与过比较。
	Ranks  map[string]HandRank
	Folded []string
	// Actions 是这手牌里发生过的每一个动作，按发生顺序。
	Actions []ActionRecord
	Pots    []Pot
	Payout  map[string]int
	Winners []string
	Pot     int
	// Stacks 是这手牌结束后各人的筹码，服务端据此更新牌桌。
	Stacks map[string]int
}

// Hand 是一手牌的状态机。
//
// 它是纯的：牌从传进来的 Deck 里发，不碰时钟、不碰网络、不起 goroutine（ADR-0012）。
// 外界只能通过 Apply 推动它，每次推动返回该发出去的事件。
type Hand struct {
	number    int
	seats     []*seatState
	button    int
	blinds    Blinds
	deck      *Deck
	street    Street
	community []Card
	// turn 是当前该行动的座位下标。
	turn int
	// lastRaise 是本 Street 上最后一次加注的增量，下一个人的最小加注额由它决定。
	lastRaise int
	illegal   []int
	actions   []ActionRecord
	done      bool
	result    HandResult
}

// NewHand 开一手牌：收盲注、发底牌，然后把行动权交给第一个该说话的人。
//
// button 是庄家位在 seats 里的下标。三人以上时 button 左手第一位是小盲、第二位是大盲，
// preflop 从大盲左手第一位开始；两人时 button 自己是小盲，preflop 他先说话、翻牌后他后说话——
// 这条单挑特例不写进来的话，两人局的盲注位置从头到尾都是错的。
func NewHand(number int, seats []Seat, button int, blinds Blinds, d *Deck) (*Hand, []Event) {
	if len(seats) < 2 {
		panic("poker: 一手牌至少要两个玩家")
	}
	for _, s := range seats {
		if s.Stack <= 0 {
			panic("poker: 没有筹码的人不能进入一手牌")
		}
	}
	// 一副牌发不出这么多人的话，在这里说清楚。不挡的话，它会在半路某次
	// Draw 里蹦出一句「牌堆已空」——那时候牌都发一半了，谁也看不出是人太多。
	if need := len(seats)*holeCardCount + communityCardCount; d.Remaining() < need {
		panic(fmt.Sprintf("poker: %d 个人要 %d 张牌，牌堆只有 %d 张", len(seats), need, d.Remaining()))
	}
	n := len(seats)
	h := &Hand{
		number:    number,
		seats:     make([]*seatState, n),
		button:    ((button % n) + n) % n,
		blinds:    blinds,
		deck:      d,
		lastRaise: blinds.Big,
		illegal:   make([]int, n),
	}
	for i, s := range seats {
		h.seats[i] = &seatState{player: s.Player, stack: s.Stack}
	}

	small, big := h.blindSeats()
	events := []Event{{
		Type:    EventHandStart,
		Hand:    number,
		Players: h.playerNames(),
		Button:  h.seats[h.button].player,
		Blinds:  blinds.String(),
		Seats:   h.seatViews(),
	}}
	events = append(events, h.postBlind(small, blinds.Small, "small_blind"))
	events = append(events, h.postBlind(big, blinds.Big, "big_blind"))

	// 从 button 左手第一位开始，一张一张发两圈，跟真实牌桌一样。
	for i := 0; i < holeCardCount; i++ {
		for k := 1; k <= n; k++ {
			s := h.seats[(h.button+k)%n]
			s.hole = append(s.hole, h.deck.Draw())
		}
	}
	for k := 1; k <= n; k++ {
		s := h.seats[(h.button+k)%n]
		// 底牌逐个定向投递。To 一旦漏填，这张牌就广播出去了。
		events = append(events, Event{
			Type:   EventHoleCards,
			To:     s.player,
			Hand:   number,
			Player: s.player,
			Cards:  cloneCards(s.hole),
		})
	}

	// 把 turn 放在大盲上，step 会从它的下一位开始找——单挑时大盲的下一位正好绕回 button，
	// 三人以上时正好是 UTG，两种情形一个式子就够了。
	h.turn = big
	events = append(events, h.step()...)
	return h, events
}

// Apply 施加一个动作，返回该发出去的事件。
//
// 动作非法时返回一条结构化错误，行动权仍归该玩家；同一轮连续三次非法，
// 就按行动超时规则替他做决定（ADR-0011）。
func (h *Hand) Apply(player string, a Action) []Event {
	if h.done {
		return []Event{h.errorTo(player, "hand_over", "这手牌已经结束了")}
	}
	i := h.indexOf(player)
	if i < 0 {
		return []Event{h.errorTo(player, "not_in_hand", "你不在这手牌里")}
	}
	if i != h.turn {
		return []Event{h.errorTo(player, "not_your_turn", fmt.Sprintf("现在轮到 %s 行动", h.seats[h.turn].player))}
	}

	if bad := h.validate(i, a); bad != nil {
		h.illegal[i]++
		out := []Event{*bad}
		if h.illegal[i] < maxIllegalActions {
			return append(out, h.yourTurn())
		}
		// 第三次了。能 check 就 check，否则 fold——跟行动超时同一套处理。
		h.illegal[i] = 0
		why := fmt.Sprintf("连续 %d 次非法动作，按超时规则处理", maxIllegalActions)
		out = append(out, h.exec(i, h.forcedAction(i), true, why)...)
		return append(out, h.step()...)
	}

	h.illegal[i] = 0
	out := h.exec(i, a, false, "")
	return append(out, h.step()...)
}

// ApplyForced 替某人做决定：能过牌就过牌，否则弃牌，并把这一下记成不是他自己按的。
//
// 服务端在玩家掉线或行动超时的时候用它接手——不接手的话，一个人拔掉网线
// 就能让整张牌桌永远停在他那一轮。规则跟连续非法动作那条是同一套（ADR-0011）。
//
// 「这一下是代打」这件事由这里统一标进事件和历史记录，服务端不该自己去改事件字段：
// 标记散在两个地方，迟早有一处漏掉。
func (h *Hand) ApplyForced(player, why string) []Event {
	if h.done {
		return []Event{h.errorTo(player, "hand_over", "这手牌已经结束了")}
	}
	i := h.indexOf(player)
	if i < 0 {
		return []Event{h.errorTo(player, "not_in_hand", "你不在这手牌里")}
	}
	if i != h.turn {
		return []Event{h.errorTo(player, "not_your_turn", fmt.Sprintf("现在轮到 %s 行动", h.seats[h.turn].player))}
	}
	h.illegal[i] = 0
	out := h.exec(i, h.forcedAction(i), true, why)
	return append(out, h.step()...)
}

// Done 这手牌是否已经结束。
func (h *Hand) Done() bool { return h.done }

// Turn 返回当前该谁行动，牌局已结束时返回空串。
func (h *Hand) Turn() string {
	if h.done || h.turn < 0 {
		return ""
	}
	return h.seats[h.turn].player
}

// Number 是这是第几手牌。
func (h *Hand) Number() int { return h.number }

// Result 返回结束后的事实。牌还没打完时调用它没有意义。
func (h *Hand) Result() HandResult { return h.result }

// Players 返回参与这手牌的所有人。
func (h *Hand) Players() []string { return h.playerNames() }

// --- 推进 ---

// step 把状态机推到「有人该行动」或「这手牌结束」为止。
func (h *Hand) step() []Event {
	var out []Event
	for {
		if h.aliveCount() <= 1 {
			// 只剩一个人没弃牌，牌局立刻结束，不摊牌——他不必亮牌，
			// 别人的底牌也没有任何理由公开。
			return append(out, h.finish(false)...)
		}
		if !h.bettingClosed() {
			next := h.nextActorFrom(h.turn + 1)
			if next < 0 {
				// 理论上走不到：bettingClosed 已经排除了没人能行动的情形。
				return append(out, h.finish(true)...)
			}
			h.turn = next
			return append(out, h.yourTurn())
		}
		if h.street == River {
			return append(out, h.finish(true)...)
		}
		out = append(out, h.dealStreet())
	}
}

// bettingClosed 判断本 Street 的下注是不是已经打完了。
func (h *Hand) bettingClosed() bool {
	high := h.highStreet()

	// 先看还有几个人「能」行动。弃牌的和 all-in 的都不能。
	actionable := 0
	for _, s := range h.seats {
		if s.folded || s.allin {
			continue
		}
		if s.street < high {
			return false // 有人还欠着注，无论如何没打完
		}
		actionable++
	}
	if actionable <= 1 {
		// 最多只剩一个人能行动，而且他没欠注——没有对手能跟他对赌，这条街就到此为止。
		// 所有人 all-in 之后剩下的公共牌会一路发完，正是走的这条路。
		return true
	}
	for _, s := range h.seats {
		if s.folded || s.allin {
			continue
		}
		if !s.acted || s.street != high {
			return false
		}
	}
	return true
}

// dealStreet 翻开下一条街。
func (h *Hand) dealStreet() Event {
	h.street++
	n := 1
	if h.street == Flop {
		n = 3
	}
	fresh := h.deck.DrawN(n)
	h.community = append(h.community, fresh...)

	for _, s := range h.seats {
		s.street = 0
		s.acted = false
	}
	// 新的一条街从零开始下注，最小下注额是一个大盲。
	h.lastRaise = h.blinds.Big
	// 翻牌后从 button 左手第一位开始说话，step 会从 turn 的下一位找起。
	h.turn = h.button

	pot := h.potTotal()
	return Event{
		Type:   EventStreet,
		Hand:   h.number,
		Street: h.street.String(),
		Cards:  cloneCards(fresh),
		Board:  cloneCards(h.community),
		Pot:    &pot,
		Seats:  h.seatViews(),
	}
}

// finish 结算这手牌。showdown 为假时无人需要亮牌。
func (h *Hand) finish(showdown bool) []Event {
	h.done = true
	h.turn = -1
	var out []Event

	ranks := make(map[string]HandRank)
	if showdown {
		entries := make([]ShowdownEntry, 0, len(h.seats))
		// 只有没弃牌的人进摊牌。弃牌者的底牌在这里被挡住，之后也不会再有出场机会——
		// 「弃牌者底牌永不公开」（ADR-0006）落地就落在这一个 continue 上。
		for _, s := range h.seats {
			if s.folded {
				continue
			}
			seven := make([]Card, 0, holeCardCount+len(h.community))
			seven = append(seven, s.hole...)
			seven = append(seven, h.community...)
			rank := Evaluate(seven)
			ranks[s.player] = rank
			entries = append(entries, ShowdownEntry{
				Player:   s.player,
				Cards:    cloneCards(s.hole),
				Category: rank.Category.String(),
				Best:     cloneCards(rank.Best),
			})
		}
		out = append(out, Event{
			Type:     EventShowdown,
			Hand:     h.number,
			Street:   h.street.String(),
			Board:    cloneCards(h.community),
			Showdown: entries,
		})
	}

	potSeats := make([]PotSeat, len(h.seats))
	for i, s := range h.seats {
		potSeats[i] = PotSeat{Player: s.player, Committed: s.committed, Folded: s.folded}
	}
	total := h.potTotal()
	pots, payout := AwardPots(BuildPots(potSeats), ranks, h.orderFromButton())
	for _, s := range h.seats {
		s.stack += payout[s.player]
	}

	var winners []string
	for _, p := range h.orderFromButton() {
		if payout[p] > 0 {
			winners = append(winners, p)
		}
	}

	out = append(out, Event{
		Type:    EventPotAwarded,
		Hand:    h.number,
		Pots:    pots,
		Winners: winners,
		Seats:   h.seatViews(),
	})
	out = append(out, Event{
		Type:    EventHandEnd,
		Hand:    h.number,
		Winners: winners,
		Pot:     &total,
		Seats:   h.seatViews(),
	})

	h.result = h.buildResult(ranks, pots, payout, winners, total)
	return out
}

func (h *Hand) buildResult(ranks map[string]HandRank, pots []Pot, payout map[string]int, winners []string, total int) HandResult {
	res := HandResult{
		Number:    h.number,
		Players:   h.playerNames(),
		Hole:      make(map[string][]Card, len(h.seats)),
		Community: cloneCards(h.community),
		Ranks:     ranks,
		Actions:   append([]ActionRecord(nil), h.actions...),
		Pots:      pots,
		Payout:    payout,
		Winners:   winners,
		Pot:       total,
		Stacks:    make(map[string]int, len(h.seats)),
	}
	for _, s := range h.seats {
		res.Hole[s.player] = cloneCards(s.hole)
		res.Stacks[s.player] = s.stack
		if s.folded {
			res.Folded = append(res.Folded, s.player)
		}
	}
	return res
}

// --- 动作 ---

// validate 检查一个动作是否合法，合法返回 nil，否则返回该回给他的结构化错误。
func (h *Hand) validate(i int, a Action) *Event {
	s := h.seats[i]
	high := h.highStreet()
	owed := high - s.street

	switch a.Kind {
	case Fold:
		// 就算能 check 也允许 fold。真实牌桌允许，而且替人拦下这个动作没有意义。
		return nil

	case Check:
		if owed > 0 {
			e := h.errorTo(s.player, "cannot_check", fmt.Sprintf("还差 %d 才跟得上，不能过牌", owed))
			e.Amount = owed
			return &e
		}
		return nil

	case Call:
		if owed <= 0 {
			e := h.errorTo(s.player, "nothing_to_call", "没有注要跟，用 check")
			return &e
		}
		return nil

	case AllIn:
		if s.stack <= 0 {
			e := h.errorTo(s.player, "no_chips", "你已经没有筹码可推了")
			return &e
		}
		return nil

	case BetTo:
		maxTo := s.street + s.stack
		minTo := high + h.lastRaise
		if a.Amount > maxTo {
			e := h.errorTo(s.player, "insufficient_stack", fmt.Sprintf("你最多只能推到 %d", maxTo))
			e.Max = maxTo
			return &e
		}
		if a.Amount <= high {
			e := h.errorTo(s.player, "bet_too_small", fmt.Sprintf("bet 是「推到多少」，当前最高已经是 %d，至少要推到 %d", high, minTo))
			e.Min, e.Max = minTo, maxTo
			return &e
		}
		// 筹码不够做一次足额加注时，只能整个推光。推光允许小于最小加注额，这是标准规则。
		if a.Amount < minTo && a.Amount != maxTo {
			e := h.errorTo(s.player, "min_raise", fmt.Sprintf("至少要推到 %d（或者 allin 推光 %d）", minTo, maxTo))
			e.Min, e.Max = minTo, maxTo
			return &e
		}
		return nil
	}
	e := h.errorTo(s.player, "unknown_action", "不认识的动作")
	return &e
}

// exec 执行一个已经验证过的动作，同时把它记进手牌历史。
//
// forced 为真表示这一下不是玩家自己按的（超时、掉线、连续非法之后代打），
// why 是给人看的原因。
func (h *Hand) exec(i int, a Action, forced bool, why string) []Event {
	s := h.seats[i]
	high := h.highStreet()
	s.acted = true
	// 先记下街名：commit / raiseTo 都不会改它，但 step 之后就变了。
	street := h.street.String()

	var name string
	var amount int
	switch a.Kind {
	case Fold:
		s.folded = true
		name = "fold"

	case Check:
		name = "check"

	case Call:
		// 筹码不够跟满就是推光，这不是错误，是德州扑克本来的样子。
		amount = min(high-s.street, s.stack)
		h.commit(s, amount)
		name = "call"
		if s.allin {
			name = "allin"
		}

	case AllIn:
		amount = s.stack
		to := s.street + amount
		h.commit(s, amount)
		if to > high {
			h.raiseTo(i, to, high)
		}
		name = "allin"

	case BetTo:
		amount = a.Amount - s.street
		h.commit(s, amount)
		h.raiseTo(i, a.Amount, high)
		name = "bet"
		if s.allin {
			name = "allin"
		}

	default:
		panic("poker: exec 收到未知动作")
	}

	h.actions = append(h.actions, ActionRecord{
		Player: s.player, Street: street, Action: name,
		Amount: amount, To: s.street, Forced: forced,
	})
	ev := h.actionEvent(s, name, amount)
	ev.Forced, ev.Message = forced, why
	return []Event{ev}
}

func (h *Hand) commit(s *seatState, amount int) {
	s.stack -= amount
	s.street += amount
	s.committed += amount
	if s.stack == 0 {
		s.allin = true
	}
}

// raiseTo 处理一次把本 Street 最高注推到 to 的加注。
func (h *Hand) raiseTo(actor, to, prevHigh int) {
	inc := to - prevHigh
	if inc < h.lastRaise {
		// 筹码不够、只能推光的那种小额 all-in，不重开下注轮——已经行动过的人
		// 不会因为它重新获得加注权。这条规则不写对，会凭空多出一轮下注。
		return
	}
	h.lastRaise = inc
	for j, s := range h.seats {
		if j != actor && !s.folded && !s.allin {
			s.acted = false
		}
	}
}

// forcedAction 是替人做决定时选的那个动作：能过牌就过牌，否则弃牌。
func (h *Hand) forcedAction(i int) Action {
	if h.highStreet()-h.seats[i].street <= 0 {
		return Action{Kind: Check}
	}
	return Action{Kind: Fold}
}

// --- 事件构造 ---

func (h *Hand) yourTurn() Event {
	s := h.seats[h.turn]
	snap := h.snapshot(h.turn)
	return Event{
		Type:     EventYourTurn,
		To:       s.player,
		Hand:     h.number,
		Player:   s.player,
		Street:   h.street.String(),
		Snapshot: &snap,
	}
}

func (h *Hand) snapshot(i int) Snapshot {
	s := h.seats[i]
	return Snapshot{
		Hand:      h.number,
		Street:    h.street.String(),
		Hole:      cloneCards(s.hole),
		Community: cloneCards(h.community),
		Pot:       h.potTotal(),
		ToCall:    min(h.highStreet()-s.street, s.stack),
		Stack:     s.stack,
		Seats:     h.seatViews(),
		Legal:     h.legalActions(i),
	}
}

// legalActions 算出此刻能做的所有动作，连带数额。
func (h *Hand) legalActions(i int) []LegalAction {
	s := h.seats[i]
	high := h.highStreet()
	owed := high - s.street

	out := []LegalAction{{Action: "fold"}}
	if owed <= 0 {
		out = append(out, LegalAction{Action: "check"})
	} else {
		out = append(out, LegalAction{Action: "call", Amount: min(owed, s.stack)})
	}
	maxTo := s.street + s.stack
	if minTo := high + h.lastRaise; minTo <= maxTo {
		out = append(out, LegalAction{Action: "bet", Min: minTo, Max: maxTo})
	}
	if s.stack > 0 {
		out = append(out, LegalAction{Action: "allin", Amount: s.stack})
	}
	return out
}

func (h *Hand) actionEvent(s *seatState, name string, amount int) Event {
	pot := h.potTotal()
	stack := s.stack
	return Event{
		Type:      EventAction,
		Hand:      h.number,
		Street:    h.street.String(),
		Player:    s.player,
		Action:    name,
		Amount:    amount,
		Committed: s.street,
		Stack:     &stack,
		Pot:       &pot,
	}
}

func (h *Hand) postBlind(i, amount int, name string) Event {
	s := h.seats[i]
	// 筹码不够交满盲注就交多少算多少，人直接 all-in。
	paid := min(amount, s.stack)
	h.commit(s, paid)
	// 注意这里不设 acted：盲注是被强制收走的，不算他行动过。
	// 大盲在 preflop 结尾的那次 option 全靠这一点。
	pot := h.potTotal()
	stack := s.stack
	return Event{
		Type:      EventBlind,
		Hand:      h.number,
		Street:    h.street.String(),
		Player:    s.player,
		Action:    name,
		Amount:    paid,
		Committed: s.street,
		Stack:     &stack,
		Pot:       &pot,
	}
}

func (h *Hand) errorTo(player, code, message string) Event {
	return Event{Type: EventError, To: player, Hand: h.number, Code: code, Message: message}
}

// --- 查询 ---

// blindSeats 返回小盲与大盲的座位下标。
func (h *Hand) blindSeats() (small, big int) {
	n := len(h.seats)
	if n == 2 {
		// 单挑：button 自己就是小盲。
		return h.button, (h.button + 1) % n
	}
	return (h.button + 1) % n, (h.button + 2) % n
}

func (h *Hand) indexOf(player string) int {
	for i, s := range h.seats {
		if s.player == player {
			return i
		}
	}
	return -1
}

// nextActorFrom 从下标 from 开始，找第一个还能行动的人。都不能行动时返回 -1。
func (h *Hand) nextActorFrom(from int) int {
	n := len(h.seats)
	for k := 0; k < n; k++ {
		i := ((from+k)%n + n) % n
		if !h.seats[i].folded && !h.seats[i].allin {
			return i
		}
	}
	return -1
}

func (h *Hand) highStreet() int {
	high := 0
	for _, s := range h.seats {
		if s.street > high {
			high = s.street
		}
	}
	return high
}

func (h *Hand) potTotal() int {
	total := 0
	for _, s := range h.seats {
		total += s.committed
	}
	return total
}

func (h *Hand) aliveCount() int {
	n := 0
	for _, s := range h.seats {
		if !s.folded {
			n++
		}
	}
	return n
}

func (h *Hand) playerNames() []string {
	out := make([]string, len(h.seats))
	for i, s := range h.seats {
		out[i] = s.player
	}
	return out
}

// orderFromButton 是从 Button 左手第一位起的座位顺序，平分底池除不尽时按它发多出来的筹码。
func (h *Hand) orderFromButton() []string {
	n := len(h.seats)
	out := make([]string, 0, n)
	for k := 1; k <= n; k++ {
		out = append(out, h.seats[(h.button+k)%n].player)
	}
	return out
}

func (h *Hand) seatViews() []SeatView {
	positions := Positions(len(h.seats), h.button)
	out := make([]SeatView, len(h.seats))
	for i, s := range h.seats {
		out[i] = SeatView{
			Player:    s.player,
			Position:  positions[i],
			Stack:     s.stack,
			Committed: s.street,
			Total:     s.committed,
			Folded:    s.folded,
			AllIn:     s.allin,
		}
	}
	return out
}

// cloneCards 复制一份牌，免得事件与 HandResult 共享同一个底层数组——
// 一个上帝视角的结构和一堆按人裁剪的事件共享内存，是可见性 bug 最舒服的温床。
func cloneCards(cards []Card) []Card {
	if len(cards) == 0 {
		return nil
	}
	out := make([]Card, len(cards))
	copy(out, cards)
	return out
}
