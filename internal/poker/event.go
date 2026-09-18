package poker

// EventType 是事件的类型标签，在 JSONL 输出里就是每行的 "type" 字段。
type EventType string

const (
	// EventTable 是牌桌快照，在有人加入时发给他本人，让他不必从零重放事件。
	EventTable EventType = "table"
	// 座位变动都是公开信息：谁来了、谁走了、谁暂离、谁回来了、谁补了码。
	EventJoined EventType = "joined"
	EventLeft   EventType = "left"
	EventSitOut EventType = "sit_out"
	EventSitIn  EventType = "sit_in"
	EventTopUp  EventType = "top_up"

	EventHandStart  EventType = "hand_start"
	EventBlind      EventType = "blind"
	EventHoleCards  EventType = "hole_cards"
	EventYourTurn   EventType = "your_turn"
	EventAction     EventType = "action"
	EventStreet     EventType = "street"
	EventShowdown   EventType = "showdown"
	EventPotAwarded EventType = "pot_awarded"
	EventHandEnd    EventType = "hand_end"

	// EventError 带机器可读的 Code 与给人看的 Message（ADR-0011）。
	EventError EventType = "error"
)

// Event 是服务端推给客户端的一条事实。JSONL 格式下它就是一行 JSON，
// text 格式下由客户端的渲染器变成中文——两者渲染的是同一个对象（ADR-0002）。
//
// 字段扁平、全部 omitempty，是为了让 JSONL 每行只出现与这条事件真正相关的键，
// agent 不必在一堆 null 里挑。
type Event struct {
	Type EventType `json:"type"`

	// To 决定投递范围：空字符串表示广播给牌桌上所有人，非空表示只投递给该名字的玩家。
	//
	// 它带 `json:"-"`，因为「这条事件该给谁看」是服务端的投递决策，不是客户端看到的事实，
	// 不该出现在线路上。ADR-0006 的可见性不变量就落在这一个字段上：任何携带某人 Hole Cards
	// 的事件都必须是定向的。唯一的例外是 Showdown——那时候亮出来的牌本就对所有人可见，
	// 而弃牌的人根本不进摊牌，他的底牌因此永远没有出场的机会。
	To string `json:"-"`

	Hand    int      `json:"hand,omitempty"`
	Player  string   `json:"player,omitempty"`
	Players []string `json:"players,omitempty"`
	Cards   []Card   `json:"cards,omitempty"`

	// Street 是这条事件发生在哪条下注轮上。
	Street string `json:"street,omitempty"`
	// Board 是当前全部公共牌。street 事件里的 Cards 只有新翻开的那几张，
	// Board 让只读一行的人也不必自己累积。
	Board []Card `json:"board,omitempty"`

	// Action / Amount / Committed / Stack 描述一个动作：谁做了什么、这次投进去多少、
	// 他在本 Street 上的总投入变成了多少、手上还剩多少。
	Action    string `json:"action,omitempty"`
	Amount    int    `json:"amount,omitempty"`
	Committed int    `json:"committed,omitempty"`
	Stack     *int   `json:"stack,omitempty"`
	// Forced 标记这个动作不是玩家自己选的，而是连续非法之后按规则替他做的（ADR-0011）。
	Forced bool `json:"forced,omitempty"`

	Button string `json:"button,omitempty"`
	Blinds string `json:"blinds,omitempty"`

	// Pot 用指针是为了让「底池为 0」能如实出现，而不是被 omitempty 吃掉。
	Pot  *int  `json:"pot,omitempty"`
	Pots []Pot `json:"pots,omitempty"`

	// Snapshot 只出现在 your_turn 上：轮到你时，决策需要的一切都在里面（ADR-0007）。
	Snapshot *Snapshot `json:"snapshot,omitempty"`

	Showdown []ShowdownEntry `json:"showdown,omitempty"`
	Winners  []string        `json:"winners,omitempty"`
	Seats    []SeatView      `json:"seats,omitempty"`

	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	// Min / Max 是错误事件的机器可读细节，比如 min_raise 的那个 min（ADR-0011）。
	Min int `json:"min,omitempty"`
	Max int `json:"max,omitempty"`
}

// ShowdownEntry 是摊牌时某个玩家亮出来的东西。这里的 Cards 是 Hole Cards，
// 它之所以能出现在广播事件里，正是因为 Showdown 是唯一一个底牌合法公开的时刻。
type ShowdownEntry struct {
	Player   string `json:"player"`
	Cards    []Card `json:"cards"`
	Category string `json:"category"`
	Best     []Card `json:"best"`
}

// SeatView 是一个座位对外可见的样子。注意这里没有 Hole Cards——
// 一个「各家状态」的结构体如果带上底牌字段，迟早有人把它塞进广播事件里。
type SeatView struct {
	Player string `json:"player"`
	Stack  int    `json:"stack"`
	// Position 是这手牌里的位置（BTN / SB / BB / UTG / HJ / CO …）。
	// 只在牌局进行中有值——两手牌之间没有庄家位，也就没有位置可言。
	Position string `json:"position,omitempty"`
	// Committed 是本 Street 的投入，Total 是本手牌的总投入。
	Committed  int  `json:"committed,omitempty"`
	Total      int  `json:"total,omitempty"`
	Folded     bool `json:"folded,omitempty"`
	AllIn      bool `json:"allin,omitempty"`
	SittingOut bool `json:"sitting_out,omitempty"`
}

// LegalAction 是此刻能做的一个动作。
//
// 直接把它算好给出去，砍掉了一整类 agent 侧的 bug（ADR-0007）：它不必自己推导
// 此刻能不能 check、最小加注额是多少、推光要多少。bet 的 Min/Max 是「到」的额度，
// 不是增量（ADR-0005）。
type LegalAction struct {
	Action string `json:"action"`
	// Amount 是 call / allin 这次要投进去的筹码。
	Amount int `json:"amount,omitempty"`
	// Min / Max 只对 bet 有意义：本 Street 总投入能推到的下限与上限。
	Min int `json:"min,omitempty"`
	Max int `json:"max,omitempty"`
}

// Snapshot 是「轮到你了」时内嵌的完整可行动快照（ADR-0007）。
//
// 有了它，AI agent 可以完全无状态：收到 your_turn 就握有决策所需的一切，
// 不必实现状态归约器，也就不会和服务端的状态算出分歧。
type Snapshot struct {
	Hand      int           `json:"hand"`
	Street    string        `json:"street"`
	Hole      []Card        `json:"hole_cards"`
	Community []Card        `json:"community_cards"`
	Pot       int           `json:"pot"`
	ToCall    int           `json:"to_call"`
	Stack     int           `json:"stack"`
	Seats     []SeatView    `json:"seats"`
	Legal     []LegalAction `json:"legal"`
}

// Visible 判断一条事件是否应当投递给名为 viewer 的玩家。
//
// 服务端的投递循环和可见性测试都调用它——可见性规则在代码里只有这一处定义，
// 才谈得上「被测试守住」。
func Visible(e Event, viewer string) bool {
	return e.To == "" || e.To == viewer
}
