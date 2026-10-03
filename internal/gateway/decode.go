package gateway

import (
	"fmt"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// HandleFrame 解码一帧并维护状态。
// 返回错误（*canbus.DecodeError）时该帧被整体拒绝，已有状态绝不被污染。
func (g *Gateway) HandleFrame(f canbus.Frame) (*Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.handleFrameLocked(f)
}

// handleFrameLocked 为解码主体；调用方必须持有 g.mu 写锁（Inject 复用同一条链路）。
func (g *Gateway) handleFrameLocked(f canbus.Frame) (*Result, error) {

	g.total++
	msg := g.db.Messages[f.ID]
	if msg == nil {
		g.reject(f, canbus.ReasonUnknownMessage, "message id not present in loaded definitions")
		return nil, &canbus.DecodeError{Reason: canbus.ReasonUnknownMessage, MsgID: f.ID, Seq: f.Seq,
			Detail: "message id not present in loaded definitions"}
	}

	// 长度规则：DLC 不符直接拒绝；其中数据短到信号无法读取的为截断帧，原因可区分。
	if len(f.Data) != msg.DLC {
		var reason canbus.DecodeReason
		if len(f.Data) < msg.DLC {
			reason = canbus.ReasonTruncatedFrame
		} else {
			reason = canbus.ReasonLengthMismatch
		}
		detail := fmt.Sprintf("data length %d does not equal defined DLC %d", len(f.Data), msg.DLC)
		g.rejectLocked(msg, f, reason, detail)
		return nil, &canbus.DecodeError{
			Reason: reason, MsgID: msg.ID, MsgName: msg.Name, Seq: f.Seq, Detail: detail,
		}
	}

	// 时间戳规则：同一报文 ID 的帧必须单调不减；回退帧拒绝且不更新任何状态。
	if last, ok := g.lastTS[msg.ID]; ok && f.Timestamp.Before(last) {
		g.rejectLocked(msg, f, canbus.ReasonTimestampRewound,
			fmt.Sprintf("frame timestamp %s is earlier than last accepted %s", f.Timestamp, last))
		return nil, &canbus.DecodeError{
			Reason: canbus.ReasonTimestampRewound, MsgID: msg.ID, MsgName: msg.Name, Seq: f.Seq,
			Detail: fmt.Sprintf("frame timestamp %s is earlier than last accepted %s", f.Timestamp, last),
		}
	}

	// 先解复用器（若有），再决定本次生效的信号布局。
	var muxValue *int
	var muxSig *model.Signal
	for _, sig := range msg.Signals {
		if sig.IsMuxMultiplexor() {
			muxSig = sig
			v := int(canbus.ExtractRaw(sig, f.Data))
			muxValue = &v
			break
		}
	}
	active := make([]*model.Signal, 0, len(msg.Signals))
	if muxSig != nil {
		active = append(active, muxSig)
		hit := false
		for _, sig := range msg.Signals {
			if sig.MuxSwitch != nil && *sig.MuxSwitch == *muxValue {
				active = append(active, sig)
				hit = true
			}
		}
		if !hit {
			g.rejectLocked(msg, f, canbus.ReasonMuxLayoutMissing,
				fmt.Sprintf("multiplexor %s=%d selects a layout with no signals defined", muxSig.Name, *muxValue))
			return nil, &canbus.DecodeError{
				Reason: canbus.ReasonMuxLayoutMissing, MsgID: msg.ID, MsgName: msg.Name, Seq: f.Seq,
				Detail: fmt.Sprintf("multiplexor %s=%d selects a layout with no signals defined", muxSig.Name, *muxValue),
			}
		}
	} else {
		for _, sig := range msg.Signals {
			if sig.MuxSwitch == nil {
				active = append(active, sig)
			}
		}
	}

	bucket := g.states[msg.ID]
	if bucket == nil {
		bucket = map[string]*model.State{}
		g.states[msg.ID] = bucket
	}
	res := &Result{Frame: f, MsgID: msg.ID, MsgName: msg.Name, MuxValue: muxValue}
	for _, sig := range active {
		raw := canbus.ExtractRaw(sig, f.Data)
		label, known := sig.ValueTable.LookupLabel(int64(raw))
		st := &model.State{
			MessageID:   msg.ID,
			MessageName: msg.Name,
			SignalName:  sig.Name,
			Raw:         raw,
			Value:       canbus.Physical(sig, raw),
			Unit:        sig.Unit,
			UpdatedAt:   f.Timestamp,
			ReceivedAt:  f.Timestamp,
			Label:       label,
			LabelKnown:  known,
		}
		if sig.MuxSwitch != nil {
			v := *muxValue
			st.MuxValue = &v
		}
		bucket[sig.Name] = st
		res.Updated = append(res.Updated, *st)
	}
	g.lastTS[msg.ID] = f.Timestamp
	dataCopy := make([]byte, len(f.Data))
	copy(dataCopy, f.Data)
	g.lastData[msg.ID] = dataCopy
	g.ok++
	return res, nil
}

func (g *Gateway) reject(f canbus.Frame, reason canbus.DecodeReason, detail string) {
	g.rejected++
	g.reasonCnt[reason]++
}

func (g *Gateway) rejectLocked(msg *model.Message, f canbus.Frame, reason canbus.DecodeReason, detail string) {
	g.rejected++
	g.reasonCnt[reason]++
}

// Stats 返回计数快照。
func (g *Gateway) Stats() Stats {
	g.mu.RLock()
	defer g.mu.RUnlock()
	m := make(map[canbus.DecodeReason]int64, len(g.reasonCnt))
	for k, v := range g.reasonCnt {
		m[k] = v
	}
	return Stats{TotalFrames: g.total, DecodedOK: g.ok, Rejected: g.rejected, RejectReasons: m}
}
