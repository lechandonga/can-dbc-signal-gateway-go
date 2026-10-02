package dbc

import (
	"fmt"
	"sort"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// linearBits 按 MSB->LSB 顺序返回信号占用的线性位序（bit0=byte0 的最低位）。
// Motorola 使用 DBC sawtooth 规则，并校验布局自洽（不得斜跨字节边界）。
func linearBits(sig *model.Signal) ([]int, bool) {
	switch sig.Order {
	case model.Intel:
		bits := make([]int, sig.Length)
		for i := 0; i < sig.Length; i++ {
			bits[sig.Length-1-i] = sig.StartBit + i // MSB->LSB
		}
		return bits, true
	case model.Motorola:
		if !MotorolaLayoutOK(sig.StartBit, sig.Length) {
			return nil, false
		}
		return MotorolaBits(sig.StartBit, sig.Length), true
	}
	return nil, false
}

func (p *parser) validate() {
	ids := make([]uint32, 0, len(p.msgs))
	for id := range p.msgs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		p.validateMessage(p.msgs[id])
	}
}

func (p *parser) validateMessage(msg *model.Message) {
	p.current = msg
	muxCount := 0
	layouts := map[string][]*model.Signal{}

	for _, sig := range msg.Signals {
		if sig.IsMuxMultiplexor() {
			muxCount++
			if muxCount > 1 {
				p.sigErr(sig.Line, sig.Name, ReasonBadMultiplex, "message %q has more than one multiplexor signal", msg.Name)
			}
		}
		if sig.Length < 1 || sig.Length > 64 {
			p.sigErr(sig.Line, sig.Name, ReasonBadLength, "signal length must be in 1..64 bits, got %d", sig.Length)
		}
		if sig.StartBit < 0 || sig.StartBit >= msg.DLC*8 {
			p.sigErr(sig.Line, sig.Name, ReasonBadStartBit, "start bit %d out of frame (DLC=%d, %d bits)", sig.StartBit, msg.DLC, msg.DLC*8)
		}
		if sig.Factor <= 0 {
			p.sigErr(sig.Line, sig.Name, ReasonBadScaling, "factor must be > 0, got %g", sig.Factor)
		}
		if sig.Min > sig.Max {
			p.sigErr(sig.Line, sig.Name, ReasonBadScaling, "min %g greater than max %g", sig.Min, sig.Max)
		}

		bits, ok := linearBits(sig)
		if !ok {
			p.sigErr(sig.Line, sig.Name, ReasonBadStartBit, "Motorola start bit %d with length %d crosses a byte boundary diagonally (sawtooth layout not self-consistent)", sig.StartBit, sig.Length)
			continue
		}
		for _, b := range bits {
			if b < 0 || b >= msg.DLC*8 {
				p.sigErr(sig.Line, sig.Name, ReasonSignalOutOfFrame, "signal reaches bit %d, frame has only %d bits (DLC=%d)", b, msg.DLC*8, msg.DLC)
			}
		}

		layouts[layoutKey(sig)] = append(layouts[layoutKey(sig)], sig)
	}

	if muxCount == 0 {
		for _, sig := range msg.Signals {
			if sig.MuxSwitch != nil {
				p.sigErr(sig.Line, sig.Name, ReasonBadMultiplex, "switched signal exists but message %q has no multiplexor ('M') signal", msg.Name)
			}
		}
	}

	keys := make([]string, 0, len(layouts))
	for k := range layouts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p.checkOverlap(msg, k, layouts[k])
	}
}

func layoutKey(sig *model.Signal) string {
	if sig.MuxSwitch != nil {
		return fmt.Sprintf("m%d", *sig.MuxSwitch)
	}
	return "_"
}

func (p *parser) checkOverlap(msg *model.Message, layout string, sigs []*model.Signal) {
	owners := map[int]string{}
	for _, sig := range sigs {
		bits, ok := linearBits(sig)
		if !ok {
			continue
		}
		for _, b := range bits {
			if other, dup := owners[b]; dup && other != sig.Name {
				p.sigErr(sig.Line, sig.Name, ReasonBitOverlap, "bit %d overlaps signal %q in layout %q of message %q", b, other, layout, msg.Name)
			}
			owners[b] = sig.Name
		}
	}
}
