package model

import "time"

// ByteOrder 为信号字节序。
type ByteOrder int

const (
	// Intel 为小端（LSB-first，start bit 即最低有效位）。
	Intel ByteOrder = iota + 1
	// Motorola 为大端（MSB-first，start bit 即最高有效位，DBC sawtooth 布局）。
	Motorola
)

func (b ByteOrder) String() string {
	switch b {
	case Intel:
		return "Intel"
	case Motorola:
		return "Motorola"
	default:
		return "Unknown"
	}
}

// Signal 描述单个信号定义。
type Signal struct {
	Line      int // DBC 中 SG_ 所在行（1 基）
	Name      string
	IsSigned  bool
	Order     ByteOrder
	StartBit  int // DBC 起始位（Intel=LSB，Motorola=MSB）
	Length    int // 位长
	Factor    float64
	Offset    float64
	Unit      string
	Min       float64
	Max       float64
	MuxSwitch *int // 复用选择值：nil 表示普通信号；MuxIndex==-1 表示复用器
	MuxIndex  int
	Receivers []string
	// ValueTable 为该信号关联的数值表（来自内联 VAL_ 或 VAL_TABLE_ 具名表）；
	// nil 表示无数值表，查询标签时回退为原始值的十进制表示。
	ValueTable *ValueTable
}

// IsMuxMultiplexor 判断该信号是否为多路复用器。
func (s *Signal) IsMuxMultiplexor() bool { return s.MuxIndex == -1 }

// Message 描述单个报文定义。
type Message struct {
	ID       uint32
	Name     string
	DLC      int
	Sender   string
	Extended bool // 扩展帧（29 位 ID，DBC 中 BO_ 高位置 1 写法）
	Signals  []*Signal
	sigIndex map[string]*Signal
}

// SignalByName 按名称查找信号。
func (m *Message) SignalByName(name string) (*Signal, bool) {
	m.ensureIndex()
	s, ok := m.sigIndex[name]
	return s, ok
}

// AddSignal 追加信号并维护名称索引（解析期使用）。
func (m *Message) AddSignal(s *Signal) {
	m.ensureIndex()
	m.Signals = append(m.Signals, s)
	m.sigIndex[s.Name] = s
}

// HasSignal 判断是否已有同名信号（解析期使用）。
func (m *Message) HasSignal(name string) bool {
	m.ensureIndex()
	_, ok := m.sigIndex[name]
	return ok
}

func (m *Message) ensureIndex() {
	if m.sigIndex == nil {
		m.sigIndex = make(map[string]*Signal, len(m.Signals))
		for _, s := range m.Signals {
			m.sigIndex[s.Name] = s
		}
	}
}

// Database 为一份完整、已校验的 DBC 定义快照。
// Parse 成功返回的 Database 不可变，可被多个 goroutine 并发只读使用。
type Database struct {
	Messages map[uint32]*Message
	Names    map[string]uint32 // 报文名 -> ID
	// ValueTables 为文件级具名数值表（VAL_TABLE_），按表名索引。
	ValueTables map[string]*ValueTable
}

// ValueTable 为数值表：原始值 -> 语义标签（如 0->"P", 3->"D"）。
// 解析后的表不可变，可被多个 goroutine 并发只读使用。
type ValueTable struct {
	Name   string
	Labels map[int64]string
}

// LookupLabel 返回原始值对应的标签；表为 nil 或未收录该值时 ok=false，
// 由上层按稳定规则回退（原始值的十进制文本）。
func (t *ValueTable) LookupLabel(raw int64) (string, bool) {
	if t == nil {
		return "", false
	}
	label, ok := t.Labels[raw]
	return label, ok
}

// State 为单个信号的运行时状态快照。
type State struct {
	MessageID   uint32
	MessageName string
	SignalName  string
	Raw         uint64
	Value       float64
	Unit        string
	UpdatedAt   time.Time
	ReceivedAt  time.Time
	MuxValue    *int // 本次取值时的复用器选择值（普通信号为 nil）
	Label       string
	LabelKnown  bool // false 表示信号无数值表或该原始值未收录，回退见 Label 回退规则
}

// Status 为信号查询结论。
type Status int

const (
	// StatusOK 信号在超时窗口内有有效值。
	StatusOK Status = iota + 1
	// StatusTimeout 信号超过配置时长未更新，判定失效。
	StatusTimeout
	// StatusNeverReceived 定义已加载但从未收到包含该信号的帧。
	StatusNeverReceived
	// StatusMuxInactive 该信号属于当前未选中的复用布局：历史值可能存在，
	// 但按当前复用器选择值不生效。与"从未收到"和"超时"严格区分。
	StatusMuxInactive
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusTimeout:
		return "TIMEOUT"
	case StatusNeverReceived:
		return "NEVER_RECEIVED"
	case StatusMuxInactive:
		return "MUX_INACTIVE"
	default:
		return "UNKNOWN"
	}
}
