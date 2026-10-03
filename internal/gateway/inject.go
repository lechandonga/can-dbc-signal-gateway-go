package gateway

import (
	"fmt"
	"math"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// Injection 是一次按信号物理值注入请求。
type Injection struct {
	MessageID uint32
	Values    map[string]float64
	Timestamp time.Time
	Seq       int64
}

// Inject 按报文和信号物理值构造一帧，并立即走现有 HandleFrame 链路。
// 默认帧为全 0；之后复用该报文上一帧被接受时的原始位。未列出的信号位保持不变。
func (g *Gateway) Inject(in Injection) (*Result, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	data, err := g.buildFrameLocked(in.MessageID, in.Values, in.Timestamp)
	if err != nil {
		return nil, err
	}
	return g.handleFrameLocked(canbus.Frame{
		ID: in.MessageID, Data: data, Timestamp: in.Timestamp, Seq: in.Seq,
	})
}

func (g *Gateway) buildFrameLocked(id uint32, values map[string]float64, ts time.Time) ([]byte, *canbus.InjectError) {
	msg := g.db.Messages[id]
	if msg == nil {
		return nil, &canbus.InjectError{Reason: canbus.ReasonInjectUnknownMessage, BusName: g.name, MsgID: id,
			Detail: "message id not present in loaded definitions"}
	}
	targets := map[string]*model.Signal{}
	for name := range values {
		sig, ok := msg.SignalByName(name)
		if !ok {
			return nil, &canbus.InjectError{Reason: canbus.ReasonUnknownSignal, BusName: g.name, MsgID: id, MsgName: msg.Name,
				Signal: name, Detail: "signal not present in message definition"}
		}
		targets[name] = sig
	}

	var muxSig *model.Signal
	for _, sig := range msg.Signals {
		if sig.IsMuxMultiplexor() {
			muxSig = sig
			break
		}
	}

	muxValue := 0
	if muxSig != nil {
		rawSel, ok, injectErr := selectorValue(g.name, msg, muxSig, values)
		if injectErr != nil {
			return nil, injectErr
		}
		muxValue = rawSel
		_ = ok
		layoutCount := 0
		for _, sig := range msg.Signals {
			if sig.MuxSwitch != nil && *sig.MuxSwitch == muxValue {
				layoutCount++
			}
		}
		if layoutCount == 0 {
			return nil, &canbus.InjectError{Reason: canbus.ReasonInjectMuxLayoutMissing, BusName: g.name, MsgID: id,
				MsgName: msg.Name, Signal: muxSig.Name,
				Detail: fmt.Sprintf("selector value %d has no writable signal layout", muxValue)}
		}
		for name, sig := range targets {
			if sig.MuxSwitch != nil && *sig.MuxSwitch != muxValue {
				return nil, &canbus.InjectError{Reason: canbus.ReasonMuxLayoutMismatch, BusName: g.name, MsgID: id,
					MsgName: msg.Name, Signal: name,
					Detail: fmt.Sprintf("signal belongs to layout %d, selector is %d", *sig.MuxSwitch, muxValue)}
			}
		}
	}

	if last, ok := g.lastTS[id]; ok && ts.Before(last) {
		return nil, &canbus.InjectError{Reason: canbus.ReasonInjectTimestampRewound, BusName: g.name, MsgID: id,
			MsgName: msg.Name, Detail: fmt.Sprintf("timestamp %s is earlier than last accepted %s", ts, last)}
	}

	for name, sig := range targets {
		if err := validatePhysical(sig, values[name]); err != "" {
			return nil, &canbus.InjectError{Reason: canbus.ReasonValueOutOfRange, BusName: g.name, MsgID: id,
				MsgName: msg.Name, Signal: name, Detail: err}
		}
	}

	data := make([]byte, msg.DLC)
	if previous := g.lastData[id]; previous != nil {
		copy(data, previous)
	}
	for name, sig := range targets {
		canbus.EncodePhysical(sig, values[name], data)
	}
	if muxSig != nil {
		clearBits(muxSig, data)
		for _, sig := range msg.Signals {
			if sig.MuxSwitch != nil {
				clearBits(sig, data)
			}
		}
		for name, sig := range targets {
			if sig.MuxSwitch != nil || sig.IsMuxMultiplexor() {
				canbus.EncodePhysical(sig, values[name], data)
			}
		}
	}
	return data, nil
}

func clearBits(sig *model.Signal, data []byte) {
	bits, ok := canbus.SignalBitPositions(sig)
	if !ok {
		return
	}
	for _, bit := range bits {
		data[bit/8] &^= 1 << uint(bit%8)
	}
}

func selectorValue(busName string, msg *model.Message, muxSig *model.Signal, values map[string]float64) (int, bool, *canbus.InjectError) {
	value, ok := values[muxSig.Name]
	if !ok {
		return 0, false, &canbus.InjectError{Reason: canbus.ReasonMuxSelectorInvalid, BusName: busName, MsgID: msg.ID,
			MsgName: msg.Name, Signal: muxSig.Name, Detail: "multiplexor selector value is required"}
	}
	if math.Mod(value, 1) != 0 || value < 0 || value < float64(muxSig.Min) || value > float64(muxSig.Max) {
		return 0, false, &canbus.InjectError{Reason: canbus.ReasonMuxSelectorInvalid, BusName: busName, MsgID: msg.ID,
			MsgName: msg.Name, Signal: muxSig.Name,
			Detail: fmt.Sprintf("selector physical value %v is not an integer in [%g,%g]", value, muxSig.Min, muxSig.Max)}
	}
	return int(value), true, nil
}

func validatePhysical(sig *model.Signal, value float64) string {
	tolerance := sig.Factor / 2
	if value+tolerance < sig.Min || value-tolerance > sig.Max {
		return fmt.Sprintf("physical value %v is outside [%g,%g] beyond half-quantum %g", value, sig.Min, sig.Max, tolerance)
	}
	raw := quantizedRaw(sig, value)
	roundTrip := canbus.Physical(sig, raw)
	if math.Abs(roundTrip-value) > tolerance {
		return fmt.Sprintf("physical value %v cannot be represented by %d-bit raw", value, sig.Length)
	}
	return ""
}

func quantizedRaw(sig *model.Signal, value float64) uint64 {
	data := make([]byte, 8)
	canbus.EncodePhysical(sig, value, data)
	return canbus.ExtractRaw(sig, data)
}
