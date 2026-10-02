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
)

// DecodeError 描述一次被拒绝的帧解码。
type DecodeError struct {
	Reason  DecodeReason
	MsgID   uint32
	MsgName string
	Detail  string
	Seq     int64
}

func (e *DecodeError) Error() string {
	name := e.MsgName
	if name == "" {
		name = "-"
	}
	return fmt.Sprintf("decode message id=%d name=%s seq=%d: %s: %s", e.MsgID, name, e.Seq, e.Reason, e.Detail)
}
