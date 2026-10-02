package decode

import "fmt"

// RejectReason classifies why a frame could not be decoded. Values are stable
// strings so tests and logs can assert on them.
type RejectReason string

const (
	ReasonUnknownMessage RejectReason = "unknown_message"    // id not in the definitions
	ReasonFrameTooShort  RejectReason = "frame_too_short"    // data shorter than DLC: truncated
	ReasonLengthMismatch RejectReason = "length_mismatch"    // data length differs from defined DLC
	ReasonMuxIllegal     RejectReason = "mux_value_illegal"  // switch value not among legal selectors
	ReasonMuxNoLayout    RejectReason = "mux_layout_missing" // selector legal but no signal layout
)

// FrameError is a deterministic, distinguishable decode failure.
type FrameError struct {
	Reason    RejectReason
	MessageID uint32
	Signal    string // switch signal name for mux errors
	Value     int64  // offending selector value for mux errors
	Detail    string
}

func (e *FrameError) Error() string {
	switch e.Reason {
	case ReasonMuxIllegal, ReasonMuxNoLayout:
		return fmt.Sprintf("message %d signal %q: %s (selector=%d)",
			e.MessageID, e.Signal, e.Detail, e.Value)
	default:
		return fmt.Sprintf("message %d: %s (%s)", e.MessageID, e.Detail, e.Reason)
	}
}

// SignalValue is one decoded signal of one frame.
type SignalValue struct {
	Name     string
	Raw      int64   // integer raw value as transmitted
	Physical float64 // raw*factor + offset
	Unit     string
}

// FrameResult is the successful decode of one frame.
type FrameResult struct {
	MessageID uint32
	Message   string
	Branch    int // multiplexor branch that was decoded (-1 if not multiplexed)
	HasBranch bool
	SwitchRaw int64
	Signals   []SignalValue
}
