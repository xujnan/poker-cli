package poker

// EventType 是事件的类型标签，在 JSONL 输出里就是每行的 "type" 字段。
type EventType string

const (
	// EventTable 是牌桌快照，目前只在有人加入时发给他本人，让他不必从零重放事件。
	// ADR-0007 里 your_turn 内嵌的那份完整快照，是它长大以后的样子。
	EventTable EventType = "table"
	// EventJoined / EventLeft 是座位变动，公开信息。
	EventJoined EventType = "joined"
	EventLeft   EventType = "left"

	EventHandStart      EventType = "hand_start"
	EventHoleCards      EventType = "hole_cards"
	EventCommunityCards EventType = "community_cards"
	EventShowdown       EventType = "showdown"
	EventHandEnd        EventType = "hand_end"

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
	// 的事件都必须是定向的。唯一的例外是 Showdown——那时候亮出来的牌本就对所有人可见。
	To string `json:"-"`

	Hand    int      `json:"hand,omitempty"`
	Player  string   `json:"player,omitempty"`
	Players []string `json:"players,omitempty"`
	Cards   []Card   `json:"cards,omitempty"`

	// Pot 用指针是为了让「底池为 0」能如实出现在 hand_end 里，而不是被 omitempty 吃掉。
	// 第一刀没有下注，它恒为 0；下一刀盲注进来后自然有值。
	Pot *int `json:"pot,omitempty"`

	Showdown []ShowdownEntry `json:"showdown,omitempty"`
	Winners  []string        `json:"winners,omitempty"`

	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// ShowdownEntry 是摊牌时某个玩家亮出来的东西。这里的 Cards 是 Hole Cards，
// 它之所以能出现在广播事件里，正是因为 Showdown 是唯一一个底牌合法公开的时刻。
type ShowdownEntry struct {
	Player   string `json:"player"`
	Cards    []Card `json:"cards"`
	Category string `json:"category"`
	Best     []Card `json:"best"`
}

// Visible 判断一条事件是否应当投递给名为 viewer 的玩家。
//
// 服务端的投递循环和可见性测试都调用它——可见性规则在代码里只有这一处定义，
// 才谈得上「被测试守住」。
func Visible(e Event, viewer string) bool {
	return e.To == "" || e.To == viewer
}
