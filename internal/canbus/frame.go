// Package canbus 定义与硬件无关的 CAN 帧模型、错误类型与位级编解码。
package canbus

import "time"

// Frame 为一帧标准/扩展 CAN 数据帧的本地表示。
type Frame struct {
	ID        uint32
	Data      []byte
	Timestamp time.Time
	// Seq 为本地帧序列中的序号，用于日志复现；不参与解码。
	Seq int64
}
