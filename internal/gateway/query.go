package gateway

import (
	"time"

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
func classify(st *model.State, timeout time.Duration, now time.Time) (model.Status, string) {
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
	st := g.states[msgID][sigName]
	view := SignalView{}
	if st != nil {
		view.State = *st
	}
	if inactiveFor(g.states[msgID], msg, sigName, st) {
		view.Status = model.StatusInactive
		view.Reason = "signal belongs to a multiplex layout that is not currently selected"
	} else {
		view.Status, view.Reason = classify(st, g.timeoutFor(msg.Name, sigName), now)
	}
	return view, true
}

func inactiveFor(bucket map[string]*model.State, msg *model.Message, signalName string, _ *model.State) bool {
	sig, ok := msg.SignalByName(signalName)
	if !ok || sig.MuxSwitch == nil {
		return false
	}
	var muxSig *model.Signal
	for _, candidate := range msg.Signals {
		if candidate.IsMuxMultiplexor() {
			muxSig = candidate
			break
		}
	}
	if muxSig == nil {
		return false
	}
	current := bucket[muxSig.Name]
	if current == nil {
		return false
	}
	return int(current.Raw) != *sig.MuxSwitch
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
		bucket := g.states[id]
		for _, sig := range msg.Signals {
			var st *model.State
			if bucket != nil {
				st = bucket[sig.Name]
			}
			entry := SignalEntry{MsgID: id, MsgName: msg.Name, Sig: sig}
			if st != nil {
				entry.View.State = *st
			}
			if inactiveFor(bucket, msg, sig.Name, st) {
				entry.View.Status = model.StatusInactive
				entry.View.Reason = "signal belongs to a multiplex layout that is not currently selected"
			} else {
				entry.View.Status, entry.View.Reason = classify(st, g.timeoutFor(msg.Name, sig.Name), now)
			}
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
