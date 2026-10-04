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
	Line        int // DBC 中 SG_ 所在行（1 基）
	Name        string
	IsSigned    bool
	Order       ByteOrder
	StartBit    int // DBC 起始位（Intel=LSB，Motorola=MSB）
	Length      int // 位长
	Factor      float64
	Offset      float64
	Unit        string
	Min         float64
	Max         float64
	MuxSwitch   *int // 复用选择值：nil 表示普通信号；MuxIndex==-1 表示复用器
	MuxIndex    int
	Receivers   []string
	ValueLabels map[uint64]string
}

// IsMuxMultiplexor 判断该信号是否为多路复用器。
func (s *Signal) IsMuxMultiplexor() bool { return s.MuxIndex == -1 }

// Message 描述单个报文定义。
type Message struct {
	ID       uint32
	Name     string
	DLC      int
	Sender   string
	Extended bool // DBC 中以高位标记（>=2^31）声明的扩展帧；存储的 ID 已去掉标记位
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
	Messages    map[uint32]*Message
	Names       map[string]uint32 // 报文名 -> ID
	ValueTables map[string]map[uint64]string
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
	HasLabel    bool
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
	// StatusInactive 信号属于复用报文的非当前选择分支。
	StatusInactive
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusTimeout:
		return "TIMEOUT"
	case StatusNeverReceived:
		return "NEVER_RECEIVED"
	case StatusInactive:
		return "INACTIVE"
	default:
		return "UNKNOWN"
	}
}
