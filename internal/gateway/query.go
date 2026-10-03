package gateway

import (
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// timeoutFor 解析信号超时：精确键 "msgName.sigName" > 全局键 "sigName" > 默认值。
// 负值表示永不超时（0 回退默认，避免配置遗漏导致立刻失效）。
func (g *Gateway) timeoutFor(msgName, sigName string) time.Duration {
	if d, ok := g.cfg.SignalTimeouts[msgName+"."+sigName]; ok {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return d
		}
	}
	if d, ok := g.cfg.SignalTimeouts[sigName]; ok {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return d
		}
	}
	return g.cfg.DefaultTimeout
}

// classify 在给定"当前时间"下判定信号状态。
// activeMux 为该信号所属报文的当前复用器选择值；仅对分支信号（MuxSwitch!=nil）
// 有意义：选择值对不上时判 MUX_INACTIVE（旧布局值不再当前有效，但不与超时混同）。
func classify(st *model.State, timeout time.Duration, now time.Time, sig *model.Signal, activeMux *int) (model.Status, string) {
	if sig != nil && sig.MuxSwitch != nil {
		if activeMux == nil || *activeMux != *sig.MuxSwitch {
			return model.StatusMuxInactive, "signal belongs to mux layout m" +
				itoa(*sig.MuxSwitch) + ", current selector selects a different layout"
		}
	}
	if st == nil {
		return model.StatusNeverReceived, "signal has never been received"
	}
	if timeout < 0 {
		return model.StatusOK, ""
	}
	if age := now.Sub(st.UpdatedAt); age > timeout {
		return model.StatusTimeout, "no update for " + age.String() + ", timeout " + timeout.String()
	}
	return model.StatusOK, ""
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [24]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// currentMuxLocked 返回报文当前复用器选择值（来自最后接受帧）；
// 非复用报文或从未收到时返回 nil。
func (g *Gateway) currentMuxLocked(msg *model.Message) *int {
	var muxSig *model.Signal
	for _, sig := range msg.Signals {
		if sig.IsMuxMultiplexor() {
			muxSig = sig
			break
		}
	}
	if muxSig == nil {
		return nil
	}
	data := g.lastData[msg.ID]
	if data == nil {
		return nil
	}
	v := int(canbus.ExtractRaw(muxSig, data))
	return &v
}

// GetSignal 按当前时间 now 查询单个信号。
// 定义中不存在该信号时 ok=false（与"从未收到"明确区分）。
func (g *Gateway) GetSignal(msgID uint32, sigName string, now time.Time) (SignalView, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	msg := g.db.Messages[msgID]
	if msg == nil {
		return SignalView{}, false
	}
	if _, ok := msg.SignalByName(sigName); !ok {
		return SignalView{}, false
	}
	view := SignalView{}
	if st := g.states[msgID][sigName]; st != nil {
		view.State = *st
	}
	var sig *model.Signal
	if s, ok := msg.SignalByName(sigName); ok {
		sig = s
	}
	view.Status, view.Reason = classify(g.states[msgID][sigName], g.timeoutFor(msg.Name, sigName), now,
		sig, g.currentMuxLocked(msg))
	return view, true
}

// SignalEntry 为快照中的一行。
type SignalEntry struct {
	MsgID   uint32
	MsgName string
	Sig     *model.Signal
	View    SignalView
}

// Snapshot 返回当前定义下全部信号在 now 时刻的判定结果（拷贝，无锁外引用）。
// 同一物理信号在多个复用布局出现时按信号名只保留最新一次值。
func (g *Gateway) Snapshot(now time.Time) []SignalEntry {
	g.mu.RLock()
	defer g.mu.RUnlock()
	out := make([]SignalEntry, 0)
	for id, msg := range g.db.Messages {
		for _, sig := range msg.Signals {
			var st *model.State
			if bucket := g.states[id]; bucket != nil {
				st = bucket[sig.Name]
			}
			entry := SignalEntry{MsgID: id, MsgName: msg.Name, Sig: sig}
			if st != nil {
				entry.View.State = *st
			}
			entry.View.Status, entry.View.Reason = classify(st, g.timeoutFor(msg.Name, sig.Name), now,
				sig, g.currentMuxLocked(msg))
			out = append(out, entry)
		}
	}
	return out
}

// LastTimestamp 返回某报文最后一次被接受帧的时间戳与是否存在。
func (g *Gateway) LastTimestamp(msgID uint32) (time.Time, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	t, ok := g.lastTS[msgID]
	return t, ok
}
