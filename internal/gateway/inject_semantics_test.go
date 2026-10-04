package gateway

import (
	"math"
	"testing"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

func withinQuantum(sig *model.Signal, got, want float64) bool {
	half := sig.Factor / 2
	return math.Abs(got-want) <= half+half*1e-9
}

func findSig(t *testing.T, gw *Gateway, id uint32, name string) *model.Signal {
	t.Helper()
	msg := gw.DB().Messages[id]
	if msg == nil {
		t.Fatalf("message %d not loaded", id)
	}
	sig, ok := msg.SignalByName(name)
	if !ok {
		t.Fatalf("signal %q not in message %d", name, id)
	}
	return sig
}

// 同一信号先写大值再写小值，注入后立刻查询必须在量化误差内等于写入值。
func TestInjectReadbackLargeThenSmall(t *testing.T) {
	gw := newFeatureGateway(t)
	cases := []struct {
		signal string
		values []float64
	}{
		{"EngineSpeed", []float64{800, 100, 0, 1000, 250.25}},
		{"EngineTemp", []float64{90, -40, 100, 90}},
		{"RequestTorque", []float64{-12.5, 12.5, -100, 100, 0, -0.1}},
	}
	step := int64(0)
	for _, tc := range cases {
		sig := findSig(t, gw, 256, tc.signal)
		for i, v := range tc.values {
			step++
			ts := t0.Add(time.Duration(step) * time.Millisecond)
			if _, err := gw.Inject(Injection{MessageID: 256, Timestamp: ts, Seq: step,
				Values: map[string]float64{tc.signal: v}}); err != nil {
				t.Fatalf("inject %s=%v: %v", tc.signal, v, err)
			}
			view, _ := gw.GetSignal(256, tc.signal, ts)
			if view.Status != model.StatusOK || !withinQuantum(sig, view.State.Value, v) {
				t.Fatalf("readback mismatch %s: wrote %v got %v status=%s", tc.signal, v, view.State.Value, view.Status)
			}
			t.Logf("[biz=inject-readback][input=ENGINE_DATA %s=%v seq=%d] readback=%v within half-quantum %g raw=%d",
				tc.signal, v, i+1, view.State.Value, sig.Factor/2, view.State.Raw)
		}
	}
}

// 只写单个信号时，同一报文其它信号保持上一帧被接受时的原始位。
func TestInjectSingleSignalDoesNotClearOthers(t *testing.T) {
	gw := newFeatureGateway(t)
	if _, err := gw.Inject(Injection{MessageID: 256, Timestamp: t0, Seq: 1, Values: map[string]float64{
		"EngineSpeed": 800, "EngineTemp": 90, "RequestTorque": -12.5,
	}}); err != nil {
		t.Fatal(err)
	}
	for i, v := range []float64{100, 800, 100, 900} {
		ts := t0.Add(time.Duration(i+1) * time.Millisecond)
		if _, err := gw.Inject(Injection{MessageID: 256, Timestamp: ts, Seq: int64(i + 2),
			Values: map[string]float64{"EngineSpeed": v}}); err != nil {
			t.Fatal(err)
		}
		temp, _ := gw.GetSignal(256, "EngineTemp", ts)
		torque, _ := gw.GetSignal(256, "RequestTorque", ts)
		if temp.State.Raw != 130 || torque.State.Raw != 0xFF83 {
			t.Fatalf("after single-signal inject #%d unspecified bits changed: temp raw=%d torque raw=%#x",
				i, temp.State.Raw, torque.State.Raw)
		}
	}
	t.Logf("[biz=inject-preserve][input=ENGINE_DATA 4x Speed-only after full write] EngineTemp raw=130 RequestTorque raw=0xff83 preserved bit-for-bit")
}

// 复用报文只写普通信号、未给选择值（从未收到帧）应能构造，按默认选择值 0 处理。
func TestInjectMuxOrdinaryOnlyWithoutSelector(t *testing.T) {
	gl := openDemo(t)
	dgw := gl.gw
	if _, err := dgw.Inject(Injection{MessageID: 257, Timestamp: t0, Seq: 1,
		Values: map[string]float64{}}); err != nil {
		t.Fatalf("first inject on mux message without selector must use default 0: %v", err)
	}
	sel, _ := dgw.GetSignal(257, "StatusType", t0)
	if sel.State.Raw != 0 {
		t.Fatalf("default selector must be 0, got %d", sel.State.Raw)
	}
	// feature DBC 里只有复用器与分支：显式写分支信号但无历史选择值，必须报选择值缺失。
	gw := newFeatureGateway(t)
	_, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0, Seq: 2,
		Values: map[string]float64{"Gear": 3}})
	if err == nil || asInjectReason(t, err) != canbus.ReasonMuxSelectorInvalid {
		t.Fatalf("branch write without selector on empty history must be SELECTOR_INVALID: %v", err)
	}
	t.Logf("[biz=inject-mux-default][input=BODY_STATUS first inject without selector] default selector 0 accepted; branch-only write still SELECTOR_INVALID")
}
