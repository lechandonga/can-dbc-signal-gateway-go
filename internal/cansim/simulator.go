// Package cansim 在本地生成确定性的 CAN 帧序列，用于替代真实总线硬件。
//
// 序列以 Script（带时间戳的动作列表）描述，可完整复现：
// 正常帧、长度错误帧、截断帧、未知报文帧、时间戳回退帧都可显式构造。
package cansim

import (
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// Step 为脚本中的一步：在 At 时刻向队列投入帧（可多帧同刻）。
type Step struct {
	At    time.Duration // 相对 T0 的偏移
	Frame canbus.Frame
}

// Sequence 是一条可重放的帧脚本。
type Sequence struct {
	T0    time.Time
	Steps []Step
}

// NewSequence 创建脚本，T0 为零点时间戳（通常传 time.Unix(0,0) 便于复现）。
func NewSequence(t0 time.Time) *Sequence { return &Sequence{T0: t0} }

// Add 追加一帧。data 为 nil 时按 DLC 分配。
func (s *Sequence) Add(at time.Duration, id uint32, data []byte) *Sequence {
	s.Steps = append(s.Steps, Step{
		At:    at,
		Frame: canbus.Frame{ID: id, Data: data, Timestamp: s.T0.Add(at), Seq: int64(len(s.Steps)) + 1},
	})
	return s
}

// Replay 按加入顺序输出带绝对时间戳的帧切片（构造脚本本身保证顺序）。
func (s *Sequence) Replay() []canbus.Frame {
	out := make([]canbus.Frame, len(s.Steps))
	for i, st := range s.Steps {
		f := st.Frame
		f.Timestamp = s.T0.Add(st.At)
		f.Seq = int64(i) + 1
		out[i] = f
	}
	return out
}

// Builder 按 DBC 定义辅助构造合法/非法帧，避免手算字节位。
type Builder struct {
	db *model.Database
}

// NewBuilder 基于已解析的定义创建构造器。
func NewBuilder(db *model.Database) *Builder { return &Builder{db: db} }

// Frame 按信号物理量构造一帧：assignments 为 "信号名 -> 物理量"；
// 多路复用报文需在 assignments 中给出复用器信号的值以选择布局。
// 未赋值信号位为 0。
func (b *Builder) Frame(id uint32, muxValue *int, assignments map[string]float64) (canbus.Frame, error) {
	msg := b.db.Messages[id]
	data := make([]byte, msg.DLC)
	for _, sig := range msg.Signals {
		if sig.IsMuxMultiplexor() {
			if muxValue == nil {
				if v, ok := assignments[sig.Name]; ok {
					encode(sig, v, data)
				}
				continue
			}
			encode(sig, float64(*muxValue), data)
			continue
		}
		if sig.MuxSwitch != nil {
			if muxValue == nil || *sig.MuxSwitch != *muxValue {
				continue
			}
		}
		if v, ok := assignments[sig.Name]; ok {
			encode(sig, v, data)
		}
	}
	return canbus.Frame{ID: id, Data: data}, nil
}

func encode(sig *model.Signal, physical float64, data []byte) {
	canbus.EncodePhysical(sig, physical, data)
}

// Truncate 返回长度为 n 的帧数据副本，用于构造截断/长度不符帧。
func Truncate(f canbus.Frame, n int) canbus.Frame {
	if n > len(f.Data) {
		ext := make([]byte, n)
		copy(ext, f.Data)
		f.Data = ext
		return f
	}
	out := make([]byte, n)
	copy(out, f.Data)
	f.Data = out
	return f
}

// WithID 返回修改了 ID 的帧副本，用于构造未知报文帧。
func WithID(f canbus.Frame, id uint32) canbus.Frame { f.ID = id; return f }
