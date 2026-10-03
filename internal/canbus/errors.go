package canbus

import "fmt"

// DecodeReason 为帧被拒绝 / 信号失效的可区分原因。
type DecodeReason string

const (
	ReasonUnknownMessage   DecodeReason = "UNKNOWN_MESSAGE"    // 定义中不存在该报文 ID
	ReasonLengthMismatch   DecodeReason = "LENGTH_MISMATCH"    // 数据长度与 DBC DLC 不符
	ReasonTruncatedFrame   DecodeReason = "TRUNCATED_FRAME"    // 数据长度小于信号到达的最高字节
	ReasonMuxValueIllegal  DecodeReason = "MUX_VALUE_ILLEGAL"  // 复用器原始值超出其位宽可合理范围（理论不会发生，保留）
	ReasonMuxLayoutMissing DecodeReason = "MUX_LAYOUT_MISSING" // 复用器选择值无对应信号布局
	ReasonTimestampRewound DecodeReason = "TIMESTAMP_REWOUND"  // 同 ID 帧时间戳回退（乱序），拒绝且不更新状态
	// 注入（按信号构帧）专用原因，与解码原因同空间、各自可区分。
	ReasonUnknownSignal       DecodeReason = "UNKNOWN_SIGNAL"         // 报文存在但信号名不在定义中
	ReasonValueOutOfRange     DecodeReason = "VALUE_OUT_OF_RANGE"     // 物理值超出 [min,max] 或原始值超出位宽
	ReasonMuxSelectorIllegal  DecodeReason = "MUX_SELECTOR_ILLEGAL"   // 选择值本身不合法（非整数/越界/负）
	ReasonMuxLayoutMismatch   DecodeReason = "MUX_LAYOUT_MISMATCH"    // 选择值与目标信号所在布局对不上
	ReasonMuxNoWritableLayout DecodeReason = "MUX_NO_WRITABLE_LAYOUT" // 该选择值下没有任何可写布局
)

// DecodeError 描述一次被拒绝的帧解码。
type DecodeError struct {
	Reason  DecodeReason
	MsgID   uint32
	MsgName string
	SigName string // 注入错误时涉及的信号名（可空）
	Detail  string
	Seq     int64
}

func (e *DecodeError) Error() string {
	name := e.MsgName
	if name == "" {
		name = "-"
	}
	sig := ""
	if e.SigName != "" {
		sig = " signal=" + e.SigName
	}
	return fmt.Sprintf("decode message id=%d name=%s%s seq=%d: %s: %s", e.MsgID, name, sig, e.Seq, e.Reason, e.Detail)
}
