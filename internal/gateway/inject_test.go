package gateway

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/cansim"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

func TestInjectRoundTripEndianSignedScaling(t *testing.T) {
	gl := openDemo(t)
	gw := gl.gw

	cases := []struct {
		id    uint32
		sig   string
		value float64
	}{
		{256, "EngineSpeed", 1234.5},
		{256, "EngineTemp", 105},
		{256, "RequestTorque", -99.9},
		{258, "DiagWordMoto", 0xBEEF},
		{258, "DiagByte", 0x5A},
	}
	for _, tc := range cases {
		res, err := gw.Inject(InjectRequest{MsgID: tc.id, Values: map[string]float64{tc.sig: tc.value}, Timestamp: t0})
		if err != nil {
			t.Fatalf("inject %s=%v: %v", tc.sig, tc.value, err)
		}
		v, ok := gw.GetSignal(tc.id, tc.sig, t0)
		if !ok {
			t.Fatalf("%s missing after inject", tc.sig)
		}
		if math.Abs(v.State.Value-tc.value) > 1e-9 {
			// 量化误差以内判定：按 factor 的半个步长。
			sig, _ := gw.DB().Messages[tc.id].SignalByName(tc.sig)
			if math.Abs(v.State.Value-tc.value) > sig.Factor/2 {
				t.Fatalf("%s readback=%v want %v (beyond quantization)", tc.sig, v.State.Value, tc.value)
			}
		}
		t.Logf("[biz=inject-roundtrip][input=%s=%v] accepted seq frames=%d readback raw=%d value=%v",
			tc.sig, tc.value, len(res.Updated), v.State.Raw, v.State.Value)
	}
}

func TestInjectKeepsUnspecifiedBits(t *testing.T) {
	gl := openDemo(t)
	gw := gl.gw
	b := cansim.NewBuilder(gw.DB())

	// 先收一帧：Speed=800、Temp=90、Torque=-12.5。
	f, _ := b.Frame(256, nil, map[string]float64{"EngineSpeed": 800, "EngineTemp": 90, "RequestTorque": -12.5})
	f.Timestamp = t0
	if _, err := gw.HandleFrame(f); err != nil {
		t.Fatal(err)
	}
	// 仅注入 Temp=100：Speed/Torque 的原始位必须保持上一帧。
	if _, err := gw.Inject(InjectRequest{MsgID: 256,
		Values:    map[string]float64{"EngineTemp": 100},
		Timestamp: t0.Add(time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	speed, _ := gw.GetSignal(256, "EngineSpeed", t0.Add(time.Millisecond))
	torque, _ := gw.GetSignal(256, "RequestTorque", t0.Add(time.Millisecond))
	temp, _ := gw.GetSignal(256, "EngineTemp", t0.Add(time.Millisecond))
	if speed.State.Raw != 3200 || speed.State.Value != 800 {
		t.Fatalf("EngineSpeed must be preserved: raw=%d val=%v", speed.State.Raw, speed.State.Value)
	}
	if torque.State.Raw != 0xFF83 || torque.State.Value != -12.5 {
		t.Fatalf("RequestTorque must be preserved: raw=%#x val=%v", torque.State.Raw, torque.State.Value)
	}
	if temp.State.Value != 100 {
		t.Fatalf("EngineTemp must be updated: %v", temp.State.Value)
	}
	t.Logf("[biz=inject-preserve][input=Temp=100 only] Speed raw=%d Torque raw=%#x kept from last accepted frame; Temp=100",
		speed.State.Raw, torque.State.Raw)
}

func TestInjectNeverReceivedDefaultsZero(t *testing.T) {
	gl := openDemo(t)
	gw := gl.gw
	// 258 从未收到：基帧为全 0 确定默认值；只写 DiagByte，DiagWordMoto 读回 0。
	if _, err := gw.Inject(InjectRequest{MsgID: 258,
		Values:    map[string]float64{"DiagByte": 0x77},
		Timestamp: t0}); err != nil {
		t.Fatal(err)
	}
	moto, _ := gw.GetSignal(258, "DiagWordMoto", t0)
	by, _ := gw.GetSignal(258, "DiagByte", t0)
	if moto.State.Raw != 0 || moto.Status != model.StatusOK {
		t.Fatalf("unwritten signal must default to raw 0 and be active: %+v", moto)
	}
	if by.State.Raw != 0x77 {
		t.Fatalf("DiagByte raw=%#x want 0x77", by.State.Raw)
	}
	t.Logf("[biz=inject-default][input=258 never received, DiagByte=0x77] base frame zeros deterministically; DiagWordMoto raw=0")
}

func TestInjectMuxRejectionsSeparated(t *testing.T) {
	gl := openDemo(t)
	gw := gl.gw

	// 先建立 mux=0 的当前选择值（t=0）。
	if _, err := gw.Inject(InjectRequest{MsgID: 257,
		Values:    map[string]float64{"StatusType": 0, "DoorOpenDeg": 45},
		Timestamp: t0}); err != nil {
		t.Fatal(err)
	}

	// 1) 目标信号与当前布局对不上（当前 mux=0，写 m1 的 GearEngaged，且不改选择器）。
	_, err := gw.Inject(InjectRequest{MsgID: 257,
		Values:    map[string]float64{"GearEngaged": 3},
		Timestamp: t0.Add(time.Millisecond)})
	if injectReason(t, err) != canbus.ReasonMuxLayoutMismatch {
		t.Fatalf("want MUX_LAYOUT_MISMATCH, got %v", err)
	}
	t.Logf("[biz=inject-mux-mismatch][input=mux stays 0, write GearEngaged(m1)] rejected MUX_LAYOUT_MISMATCH")

	// 2) 切到不存在的选择值 9 -> NO_WRITABLE_LAYOUT（与 mismatch 分开）。
	_, err = gw.Inject(InjectRequest{MsgID: 257,
		Values:    map[string]float64{"StatusType": 9},
		Timestamp: t0.Add(2 * time.Millisecond)})
	if injectReason(t, err) != canbus.ReasonMuxNoWritableLayout {
		t.Fatalf("want MUX_NO_WRITABLE_LAYOUT, got %v", err)
	}
	t.Logf("[biz=inject-mux-no-layout][input=StatusType=9] rejected MUX_NO_WRITABLE_LAYOUT")

	// 3) 选择值本身不合法：超界（StatusType 4 位，给 16）。
	_, err = gw.Inject(InjectRequest{MsgID: 257,
		Values:    map[string]float64{"StatusType": 16},
		Timestamp: t0.Add(3 * time.Millisecond)})
	if injectReason(t, err) != canbus.ReasonValueOutOfRange && injectReason(t, err) != canbus.ReasonMuxSelectorIllegal {
		t.Fatalf("want selector illegal / out of range, got %v", err)
	}
	t.Logf("[biz=inject-mux-selector-illegal][input=StatusType=16 >4bits] rejected reason=%v", injectReason(t, err))

	// 被拒注入不得污染：当前选择值仍是 0，DoorOpenDeg 值/时间戳不变。
	door, _ := gw.GetSignal(257, "DoorOpenDeg", t0.Add(3*time.Millisecond))
	if door.State.Value != 45 || !door.State.UpdatedAt.Equal(t0) {
		t.Fatalf("failed injects polluted state: %+v", door.State)
	}
	// 切到 mux=1 成功后，旧布局信号 DoorOpenDeg 查询结论必须是 MUX_INACTIVE（不是超时/从未收到）。
	if _, err := gw.Inject(InjectRequest{MsgID: 257,
		Values:    map[string]float64{"StatusType": 1, "GearEngaged": 3, "GearLeverPos": 1},
		Timestamp: t0.Add(4 * time.Millisecond)}); err != nil {
		t.Fatal(err)
	}
	old, _ := gw.GetSignal(257, "DoorOpenDeg", t0.Add(4*time.Millisecond))
	if old.Status != model.StatusMuxInactive {
		t.Fatalf("old layout signal must be MUX_INACTIVE after switch, got %s", old.Status)
	}
	gear, _ := gw.GetSignal(257, "GearEngaged", t0.Add(4*time.Millisecond))
	if gear.Status != model.StatusOK || gear.State.Value != 3 {
		t.Fatalf("new layout signal must be OK=3, got %s val=%v", gear.Status, gear.State.Value)
	}
	t.Logf("[biz=inject-mux-inactive][input=switch 0->1 @4ms] DoorOpenDeg=MUX_INACTIVE (value still %v), GearEngaged=OK",
		old.State.Value)
}

func TestInjectUnknownAndOutOfRangeRejected(t *testing.T) {
	gl := openDemo(t)
	gw := gl.gw

	_, err := gw.Inject(InjectRequest{MsgID: 0x7FF, Values: map[string]float64{"X": 1}, Timestamp: t0})
	if injectReason(t, err) != canbus.ReasonUnknownMessage {
		t.Fatalf("want UNKNOWN_MESSAGE, got %v", err)
	}
	_, err = gw.Inject(InjectRequest{MsgID: 256, Values: map[string]float64{"NoSuchSignal": 1}, Timestamp: t0})
	if injectReason(t, err) != canbus.ReasonUnknownSignal {
		t.Fatalf("want UNKNOWN_SIGNAL, got %v", err)
	}
	_, err = gw.Inject(InjectRequest{MsgID: 256, Values: map[string]float64{"EngineSpeed": 999999}, Timestamp: t0})
	if injectReason(t, err) != canbus.ReasonValueOutOfRange {
		t.Fatalf("want VALUE_OUT_OF_RANGE, got %v", err)
	}
	// 时间戳回退也拒绝。
	good := InjectRequest{MsgID: 256, Values: map[string]float64{"EngineSpeed": 800}, Timestamp: t0.Add(10 * time.Millisecond)}
	if _, err := gw.Inject(good); err != nil {
		t.Fatal(err)
	}
	_, err = gw.Inject(InjectRequest{MsgID: 256, Values: map[string]float64{"EngineSpeed": 900}, Timestamp: t0})
	if injectReason(t, err) != canbus.ReasonTimestampRewound {
		t.Fatalf("want TIMESTAMP_REWOUND, got %v", err)
	}
	t.Logf("[biz=inject-reject][input=unknown msg/sig, out-of-range, rewound ts] all rejected by distinct reason; no state writes")
}

func injectReason(t *testing.T, err error) canbus.DecodeReason {
	t.Helper()
	var de *canbus.DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DecodeError, got %T: %v", err, err)
	}
	return de.Reason
}

func TestInjectAndQueryValueTableLabel(t *testing.T) {
	gl := openDemo(t)
	gw := gl.gw
	// demo.dbc: VAL_ 257 GearEngaged 0 P 1 R 2 N 3 D
	if _, err := gw.Inject(InjectRequest{MsgID: 257,
		Values:    map[string]float64{"StatusType": 1, "GearEngaged": 3, "GearLeverPos": 99},
		Timestamp: t0}); err != nil {
		t.Fatal(err)
	}
	gear, _ := gw.GetSignal(257, "GearEngaged", t0)
	if !gear.State.LabelKnown || gear.State.Label != "D" {
		t.Fatalf("GearEngaged raw=%d label=%q known=%v, want D/true", gear.State.Raw, gear.State.Label, gear.State.LabelKnown)
	}
	lever, _ := gw.GetSignal(257, "GearLeverPos", t0)
	if lever.State.LabelKnown {
		t.Fatalf("GearLeverPos has no value table; label must fall back as unknown")
	}
	t.Logf("[biz=value-table-query][input=GearEngaged=3 GearLeverPos=99] label D resolved; unlisted/no-table falls back to raw value (LabelKnown=false)")
}
