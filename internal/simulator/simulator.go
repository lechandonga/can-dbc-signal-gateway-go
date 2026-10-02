// Package simulator builds deterministic local CAN frame sequences from DBC
// definitions, so the whole pipeline can run without hardware or services.
package simulator

import (
	"math"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
)

// Frame is one locally generated raw CAN frame (mirrors decode.Frame to keep
// the simulator dependency-free).
type Frame struct {
	ID        uint32
	Data      []byte
	Timestamp int64
}

// FrameBuilder encodes physical signal values into raw bytes for one message.
type FrameBuilder struct {
	db     *dbc.Database
	values map[uint32]map[string]float64
	raws   map[uint32]map[string]int64
}

func NewBuilder(db *dbc.Database) *FrameBuilder {
	return &FrameBuilder{
		db:     db,
		values: map[uint32]map[string]float64{},
		raws:   map[uint32]map[string]int64{},
	}
}

// SetPhysical sets a signal from a physical value: raw = round((v-offset)/factor).
func (b *FrameBuilder) SetPhysical(id uint32, name string, physical float64) *FrameBuilder {
	if b.values[id] == nil {
		b.values[id] = map[string]float64{}
	}
	b.values[id][name] = physical
	return b
}

// SetRaw sets a signal directly from its integer raw value (e.g. switch tags).
func (b *FrameBuilder) SetRaw(id uint32, name string, raw int64) *FrameBuilder {
	if b.raws[id] == nil {
		b.raws[id] = map[string]int64{}
	}
	b.raws[id][name] = raw
	return b
}

// Build produces a frame at timestamp ms for message id. Unset bits are zero.
// For multiplexed messages the switch must be set and only signals of the
// chosen branch plus always-present signals are encoded.
func (b *FrameBuilder) Build(id uint32, timestampMs int64) (Frame, error) {
	m := b.db.Messages[id]
	if m == nil {
		return Frame{}, &BuildError{Message: "no definition for id", ID: id}
	}
	data := make([]byte, m.DLC)

	var sw *dbc.Signal
	branch := -1
	for _, s := range m.Signals {
		if s.IsSwitch {
			sw = s
		}
	}
	if sw != nil {
		v, ok := b.raws[id][sw.Name]
		if !ok {
			if pv, ok2 := b.values[id][sw.Name]; ok2 {
				v = toRaw(pv, sw)
			} else {
				return Frame{}, &BuildError{ID: id, Signal: sw.Name,
					Message: "multiplexor switch value must be set"}
			}
		}
		branch = int(v)
		if err := encodeSignal(data, sw, v); err != nil {
			return Frame{}, err
		}
	}

	for _, s := range m.Signals {
		if s == sw {
			continue
		}
		if s.HasBranch && branch >= 0 && s.Branch != branch {
			continue
		}
		raw, ok := b.raws[id][s.Name]
		if !ok {
			if pv, ok2 := b.values[id][s.Name]; ok2 {
				raw = toRaw(pv, s)
			} else {
				continue
			}
		}
		if err := encodeSignal(data, s, raw); err != nil {
			return Frame{}, err
		}
	}
	return Frame{ID: id, Data: data, Timestamp: timestampMs}, nil
}

func toRaw(physical float64, s *dbc.Signal) int64 {
	return int64(math.Round((physical - s.Offset) / s.Factor))
}

func encodeSignal(data []byte, s *dbc.Signal, raw int64) error {
	bits, err := dbc.SignalBitPositions(s)
	if err != nil {
		return &BuildError{ID: 0, Signal: s.Name, Message: err.Detail}
	}
	max := uint64(0)
	if s.Length == 64 {
		max = math.MaxUint64
	} else {
		max = 1<<uint(s.Length) - 1
	}
	u := uint64(raw)
	if s.Signed && raw < 0 {
		u = uint64(raw) & max
	}
	if u & ^max != 0 {
		return &BuildError{Signal: s.Name,
			Message: "raw value does not fit signal width"}
	}
	for i, lin := range bits { // bits is MSB-first
		if lin/8 >= len(data) {
			return &BuildError{Signal: s.Name, Message: "bit outside DLC"}
		}
		if u&(1<<uint(len(bits)-1-i)) != 0 {
			data[lin/8] |= 1 << uint(lin%8)
		}
	}
	return nil
}

// BuildError identifies an encoder mistake (unknown id, unfitted value...).
type BuildError struct {
	ID      uint32
	Signal  string
	Message string
}

func (e *BuildError) Error() string {
	if e.Signal != "" {
		return "simulator: signal " + e.Signal + ": " + e.Message
	}
	return "simulator: " + e.Message
}

// Sequence is an ordered, timestamped list of frames with helpers to mutate
// bytes (truncation/length changes) for negative tests.
type Sequence []Frame

// Truncate returns a copy of frame i with only the first n bytes.
func Truncate(f Frame, n int) Frame {
	if n > len(f.Data) {
		n = len(f.Data)
	}
	cp := f
	cp.Data = append([]byte(nil), f.Data[:n]...)
	return cp
}

// WithData returns a copy of the frame with arbitrary bytes.
func WithData(f Frame, data []byte) Frame {
	cp := f
	cp.Data = append([]byte(nil), data...)
	return cp
}
