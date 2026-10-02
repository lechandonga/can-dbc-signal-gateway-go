package dbc

import "fmt"

// Reason classifies why a DBC definition was rejected.
type Reason string

const (
	ReasonSyntax        Reason = "syntax_error"         // unparsable line / token
	ReasonIncomplete    Reason = "incomplete_structure" // e.g. signal without a message, dangling reference
	ReasonDuplicate     Reason = "duplicate"            // repeated message/signal name
	ReasonInvalidSignal Reason = "invalid_signal"       // bad length/start bit/factor/range
	ReasonBitOutOfRange Reason = "bit_out_of_range"     // signal bits exceed the message DLC
	ReasonBitOverlap    Reason = "bit_overlap"          // two signals occupy the same bit in the same branch
	ReasonMuxConflict   Reason = "mux_conflict"         // contradictory multiplexor setup
	ReasonMuxDangling   Reason = "mux_dangling"         // branch signals without/with mismatched switch
	ReasonUnsupported   Reason = "unsupported_feature"  // recognised syntax outside the supported subset
	ReasonInternal      Reason = "internal_error"
)

// Error is a parse/validation failure with a machine-readable reason and a
// location (line, message, signal) that is safe to show in test logs.
type Error struct {
	Reason  Reason
	Line    int    // 1-based line number in the source, 0 if not applicable
	Message string // CAN message name if known
	Signal  string // signal name if known
	Detail  string
}

func (e *Error) Error() string {
	loc := e.Detail
	if loc == "" {
		loc = string(e.Reason)
	}
	prefix := ""
	if e.Line > 0 {
		prefix = fmt.Sprintf("line %d: ", e.Line)
	}
	switch {
	case e.Message != "" && e.Signal != "":
		return fmt.Sprintf("%smessage %q signal %q: %s (%s)", prefix, e.Message, e.Signal, loc, e.Reason)
	case e.Message != "":
		return fmt.Sprintf("%smessage %q: %s (%s)", prefix, e.Message, loc, e.Reason)
	default:
		return fmt.Sprintf("%s%s (%s)", prefix, loc, e.Reason)
	}
}

func errf(line int, msg, sig string, reason Reason, format string, args ...any) *Error {
	return &Error{Reason: reason, Line: line, Message: msg, Signal: sig, Detail: fmt.Sprintf(format, args...)}
}
