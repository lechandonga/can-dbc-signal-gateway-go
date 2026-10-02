package dbc

// ByteOrder identifies the bit/byte ordering of a signal.
type ByteOrder int

const (
	OrderIntel    ByteOrder = 1 // little-endian, DBC syntax "@1"
	OrderMotorola ByteOrder = 0 // big-endian, DBC syntax "@0"
)

// Signal describes one signal inside a message.
type Signal struct {
	Name      string
	StartBit  int // DBC start bit (sawtooth numbering)
	Length    int // bit length
	Order     ByteOrder
	Signed    bool
	Factor    float64
	Offset    float64
	Unit      string
	Min       float64
	Max       float64
	Receivers []string

	// Multiplexing.
	IsSwitch  bool // this signal is the multiplexor switch ("M")
	Branch    int  // when HasBranch, the selector value this signal belongs to
	HasBranch bool

	// Enum maps raw switch values to labels (from VAL_ lines). When present on
	// a multiplexor switch, its keys define the set of legal selector values.
	Enum map[int64]string
}

// IsMultiplexed reports whether the signal only exists for one branch.
func (s *Signal) IsMultiplexed() bool { return s.HasBranch }

// Message describes one CAN frame layout.
type Message struct {
	ID        uint32
	Name      string
	DLC       int
	Sender    string
	Signals   []*Signal
	signalMap map[string]*Signal
}

// Signal returns the named signal or nil.
func (m *Message) Signal(name string) *Signal { return m.signalMap[name] }

// Database is an immutable, validated set of DBC definitions.
type Database struct {
	Messages map[uint32]*Message
	Source   string // file name or descriptive source label
}
