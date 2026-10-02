// Package decode turns raw CAN frame bytes into physical signal values using
// validated dbc.Database definitions. It is stateless and side-effect free;
// state maintenance lives in the gateway package.
package decode

import (
	"fmt"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
)

// Frame is one raw CAN frame from the bus.
type Frame struct {
	ID        uint32
	Data      []byte
	Timestamp int64 // monotonic milliseconds; ordering rules applied by gateway
}

// Decode validates the frame against the definition and decodes every signal
// present in the selected multiplexor branch. Invalid frames return a typed
// *FrameError and no partial signal list.
func Decode(db *dbc.Database, f Frame) (*FrameResult, error) {
	m, ok := db.Messages[f.ID]
	if !ok {
		return nil, &FrameError{Reason: ReasonUnknownMessage, MessageID: f.ID,
			Detail: "no definition for frame id"}
	}
	if len(f.Data) < m.DLC {
		return nil, &FrameError{Reason: ReasonFrameTooShort, MessageID: f.ID,
			Detail: fmtDetail("frame has %d bytes but definition requires %d (truncated)",
				len(f.Data), m.DLC)}
	}
	if len(f.Data) > m.DLC {
		return nil, &FrameError{Reason: ReasonLengthMismatch, MessageID: f.ID,
			Detail: fmtDetail("frame has %d bytes but definition DLC is %d (oversized)",
				len(f.Data), m.DLC)}
	}

	res := &FrameResult{MessageID: m.ID, Message: m.Name, Branch: -1}

	var sw *dbc.Signal
	for _, s := range m.Signals {
		if s.IsSwitch {
			sw = s
			break
		}
	}

	active := map[*dbc.Signal]bool{}
	for _, s := range m.Signals {
		active[s] = !s.HasBranch
	}
	if sw != nil {
		raw, err := readSignal(sw, f.Data)
		if err != nil {
			return nil, &FrameError{MessageID: m.ID, Signal: sw.Name, Detail: err.Error()}
		}
		res.SwitchRaw = raw
		res.HasBranch = true
		res.Branch = int(raw)
		if sw.Enum != nil {
			if _, legal := sw.Enum[raw]; !legal {
				return nil, &FrameError{Reason: ReasonMuxIllegal,
					MessageID: m.ID, Signal: sw.Name, Value: raw,
					Detail: "selector value is not among the switch's legal enum values"}
			}
		}
		found := false
		for _, s := range m.Signals {
			if s.HasBranch && s.Branch == int(raw) {
				active[s] = true
				found = true
			}
		}
		if !found {
			return nil, &FrameError{Reason: ReasonMuxNoLayout,
				MessageID: m.ID, Signal: sw.Name, Value: raw,
				Detail: "no multiplexed signal layout exists for selector value"}
		}
	}

	for _, s := range m.Signals {
		if !active[s] {
			continue
		}
		raw, err := readSignal(s, f.Data)
		if err != nil {
			return nil, &FrameError{MessageID: m.ID, Signal: s.Name,
				Detail: err.Error()}
		}
		res.Signals = append(res.Signals, SignalValue{
			Name:     s.Name,
			Raw:      raw,
			Physical: physical(raw, s),
			Unit:     s.Unit,
		})
	}
	return res, nil
}

func physical(raw int64, s *dbc.Signal) float64 {
	v := float64(raw)*s.Factor + s.Offset
	// Canonicalize -0 to 0 for stable test output.
	if v == 0 {
		v = 0
	}
	return v
}

func fmtDetail(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
