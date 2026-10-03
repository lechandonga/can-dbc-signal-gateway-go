package gateway

import (
	"fmt"
	"math"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// InjectRequest 为一次"按信号注入帧"的请求。
//
// 构帧规则：以该报文最后一次被接受帧的原始字节为基帧（从未收到时为全 0 的
// 确定默认帧），仅覆盖 Values 中明确给出的信号；同报文其它信号位原样保留。
// 复用报文：若 Values 中包含复用器信号，按其值选择布局；否则沿用当前选择值，
// 从未收到时默认选择值 0。构出的帧随后走与接收帧完全相同的解码链路。
type InjectRequest struct {
	MsgID     uint32
	Values    map[string]float64
	Timestamp time.Time // 必须显式给出（保持可复现）
}

// Inject 按信号物理值构造一帧并送入解码链路；成功后查询立即读到写入值
// （量化误差以内）。任何校验失败都在状态写入之前返回，绝不留下半成品或污染。
func (g *Gateway) Inject(req InjectRequest) (*Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	msg := g.db.Messages[req.MsgID]
	if msg == nil {
		g.rejectLocked(nil, canbus.Frame{ID: req.MsgID, Timestamp: req.Timestamp},
			canbus.ReasonUnknownMessage, "message id not present in loaded definitions")
		return nil, &canbus.DecodeError{Reason: canbus.ReasonUnknownMessage, MsgID: req.MsgID,
			Detail: "message id not present in loaded definitions"}
	}

	// 信号存在性先全部校验（避免编码到一半才发现未知信号留下半成品）。
	type assignment struct {
		sig *model.Signal
		raw uint64
	}
	assigns := make([]assignment, 0, len(req.Values))
	for name, phys := range req.Values {
		sig, ok := msg.SignalByName(name)
		if !ok {
			err := &canbus.DecodeError{Reason: canbus.ReasonUnknownSignal, MsgID: msg.ID, MsgName: msg.Name,
				SigName: name, Detail: "signal is not defined in message"}
			g.rejectLocked(msg, canbus.Frame{ID: msg.ID, Timestamp: req.Timestamp}, err.Reason, err.Detail)
			return nil, err
		}
		// 复用器选择值的合法性由后面的复用专项校验给出更具体原因，
		// 这里只量化编码，不提前归为 VALUE_OUT_OF_RANGE。
		var raw uint64
		isMuxorCandidate := false
		for _, s := range msg.Signals {
			if s.IsMuxMultiplexor() && s.Name == name {
				isMuxorCandidate = true
			}
		}
		if isMuxorCandidate {
			if phys != math.Trunc(phys) || phys < 0 || phys > float64(maxUnsigned(sig.Length)) {
				detail := fmt.Sprintf("multiplexor value %g is not a valid selector (unsigned integer in 0..%d)",
					phys, maxUnsigned(sig.Length))
				g.rejectLocked(msg, canbus.Frame{ID: msg.ID, Timestamp: req.Timestamp},
					canbus.ReasonMuxSelectorIllegal, detail)
				return nil, &canbus.DecodeError{Reason: canbus.ReasonMuxSelectorIllegal, MsgID: msg.ID,
					MsgName: msg.Name, SigName: name, Detail: detail}
			}
			raw = uint64(int64(phys))
		} else {
			var inRange bool
			raw, inRange = canbus.EncodeRawChecked(sig, phys)
			if !inRange {
				detail := fmt.Sprintf("physical value %g is out of signal range [%g,%g] or not representable in %d bits",
					phys, sig.Min, sig.Max, sig.Length)
				g.rejectLocked(msg, canbus.Frame{ID: msg.ID, Timestamp: req.Timestamp},
					canbus.ReasonValueOutOfRange, detail)
				return nil, &canbus.DecodeError{Reason: canbus.ReasonValueOutOfRange, MsgID: msg.ID,
					MsgName: msg.Name, SigName: name, Detail: detail}
			}
		}
		assigns = append(assigns, assignment{sig: sig, raw: raw})
	}

	// 确定复用器信号与本次选择值。
	var muxSig *model.Signal
	for _, sig := range msg.Signals {
		if sig.IsMuxMultiplexor() {
			muxSig = sig
			break
		}
	}

	// 基帧：最后接受帧的副本；从未收到 -> 全 0（确定默认值）。
	data := make([]byte, msg.DLC)
	if prev := g.lastData[msg.ID]; prev != nil {
		copy(data, prev)
	}

	if muxSig != nil {
		// 默认沿用当前选择值；从未收到时为 0。
		v := 0
		if cur := g.currentMuxLocked(msg); cur != nil {
			v = *cur
		}
		for _, a := range assigns {
			if a.sig.IsMuxMultiplexor() {
				v = int(a.raw)
			}
		}

		// 选择值位宽合法性（来自 [min,max] 或原始位宽的检查已覆盖大部分，这里兜底）。
		if v < 0 || uint64(v) > (uint64(1)<<uint(muxSig.Length))-1 {
			detail := fmt.Sprintf("multiplexor selector %d does not fit %d bits", v, muxSig.Length)
			g.rejectLocked(msg, canbus.Frame{ID: msg.ID, Timestamp: req.Timestamp},
				canbus.ReasonMuxSelectorIllegal, detail)
			return nil, &canbus.DecodeError{Reason: canbus.ReasonMuxSelectorIllegal, MsgID: msg.ID,
				MsgName: msg.Name, SigName: muxSig.Name, Detail: detail}
		}

		// 目标分支信号必须属于被选布局。
		layoutExists := false
		for _, sig := range msg.Signals {
			if sig.MuxSwitch != nil && *sig.MuxSwitch == v {
				layoutExists = true
				break
			}
		}
		for _, a := range assigns {
			if a.sig.MuxSwitch == nil {
				continue
			}
			if !layoutExists {
				detail := fmt.Sprintf("selector %d selects a layout with no writable signals", v)
				g.rejectLocked(msg, canbus.Frame{ID: msg.ID, Timestamp: req.Timestamp},
					canbus.ReasonMuxNoWritableLayout, detail)
				return nil, &canbus.DecodeError{Reason: canbus.ReasonMuxNoWritableLayout, MsgID: msg.ID,
					MsgName: msg.Name, SigName: a.sig.Name, Detail: detail}
			}
			if *a.sig.MuxSwitch != v {
				detail := fmt.Sprintf("signal %s belongs to layout m%d but current selector is %d",
					a.sig.Name, *a.sig.MuxSwitch, v)
				g.rejectLocked(msg, canbus.Frame{ID: msg.ID, Timestamp: req.Timestamp},
					canbus.ReasonMuxLayoutMismatch, detail)
				return nil, &canbus.DecodeError{Reason: canbus.ReasonMuxLayoutMismatch, MsgID: msg.ID,
					MsgName: msg.Name, SigName: a.sig.Name, Detail: detail}
			}
		}
		// 只切选择值、未给任何分支信号，但该选择值下没有布局：同样拒绝。
		if !layoutExists {
			detail := fmt.Sprintf("selector %d selects a layout with no writable signals", v)
			g.rejectLocked(msg, canbus.Frame{ID: msg.ID, Timestamp: req.Timestamp},
				canbus.ReasonMuxNoWritableLayout, detail)
			return nil, &canbus.DecodeError{Reason: canbus.ReasonMuxNoWritableLayout, MsgID: msg.ID,
				MsgName: msg.Name, SigName: muxSig.Name, Detail: detail}
		}
	}

	// 时间戳单调性与接收链路共用规则（在写入前判定）。
	if last, ok := g.lastTS[msg.ID]; ok && req.Timestamp.Before(last) {
		detail := fmt.Sprintf("inject timestamp %s is earlier than last accepted %s", req.Timestamp, last)
		g.rejectLocked(msg, canbus.Frame{ID: msg.ID, Timestamp: req.Timestamp},
			canbus.ReasonTimestampRewound, detail)
		return nil, &canbus.DecodeError{Reason: canbus.ReasonTimestampRewound, MsgID: msg.ID,
			MsgName: msg.Name, Detail: detail}
	}

	// 编码：清掉每个目标信号的旧位再写新值；其它信号位保持基帧原样。
	for _, a := range assigns {
		canbus.EncodeRaw(a.sig, a.raw, data)
	}

	// 走与真实接收完全相同的解码链路（复用布局校验、状态更新、计数、时间戳推进都在此）。
	return g.handleFrameLocked(canbus.Frame{ID: msg.ID, Data: data, Timestamp: req.Timestamp})
}

func maxUnsigned(length int) uint64 {
	if length >= 64 {
		return ^uint64(0)
	}
	return (uint64(1) << uint(length)) - 1
}

var _ = model.StatusOK
