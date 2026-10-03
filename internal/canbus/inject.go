package canbus

import "fmt"

// InjectReason 是按信号注入失败的可区分原因。
type InjectReason string

const (
	ReasonUnknownBus             InjectReason = "UNKNOWN_BUS"
	ReasonInjectUnknownMessage   InjectReason = "UNKNOWN_MESSAGE"
	ReasonUnknownSignal          InjectReason = "UNKNOWN_SIGNAL"
	ReasonMuxSelectorInvalid     InjectReason = "MUX_SELECTOR_INVALID"
	ReasonMuxLayoutMismatch      InjectReason = "MUX_LAYOUT_MISMATCH"
	ReasonInjectMuxLayoutMissing InjectReason = "MUX_LAYOUT_MISSING"
	ReasonValueOutOfRange        InjectReason = "VALUE_OUT_OF_RANGE"
	ReasonInjectTimestampRewound InjectReason = "TIMESTAMP_REWOUND"
)

// InjectError 描述一次按信号注入失败；失败不产生帧、不修改状态。
type InjectError struct {
	Reason  InjectReason
	BusName string
	MsgID   uint32
	MsgName string
	Signal  string
	Detail  string
}

func (e *InjectError) Error() string {
	return fmt.Sprintf("inject bus=%q message=%d(%q) signal=%q: %s: %s",
		e.BusName, e.MsgID, e.MsgName, e.Signal, e.Reason, e.Detail)
}
