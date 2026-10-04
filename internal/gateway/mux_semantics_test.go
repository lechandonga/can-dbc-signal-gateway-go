package gateway

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// 选择值不变：先 (Sel=1, Gear=3)，再只写 Sel=1，分支信号必须保持 3 而非被清 0。
func TestInjectMuxPreservesOtherBranchRawBits(t *testing.T) {
	gw := newFeatureGateway(t)
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0, Seq: 1,
		Values: map[string]float64{"Sel": 1, "Gear": 3}}); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0.Add(time.Millisecond), Seq: 2,
		Values: map[string]float64{"Sel": 1}}); err != nil {
		t.Fatal(err)
	}
	gear, _ := gw.GetSignal(257, "Gear", t0.Add(time.Millisecond))
	if gear.State.Value != 3 {
		t.Fatalf("same-selector inject must preserve other branch raw bits: Gear=%v want 3", gear.State.Value)
	}
	t.Logf("[biz=inject-mux-preserve][input=BODY (Sel=1,Gear=3) then Sel=1 only] Gear preserved value=3 raw=%d", gear.State.Raw)

	// 不给选择值时沿用上一帧选择值：仅写 Gear=1 也应写入布局 1。
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0.Add(2 * time.Millisecond), Seq: 3,
		Values: map[string]float64{"Gear": 1}}); err != nil {
		t.Fatalf("branch write with inherited selector should succeed: %v", err)
	}
	gear, _ = gw.GetSignal(257, "Gear", t0.Add(2*time.Millisecond))
	if gear.State.Value != 1 {
		t.Fatalf("inherited-selector branch write failed: Gear=%v", gear.State.Value)
	}
	t.Logf("[biz=inject-mux-inherit][input=BODY Gear=1 without selector after mux=1 history] selector inherited")

	// 切到选择值 0 后，旧分支 Gear 必须判 INACTIVE，不能与 TIMEOUT 混淆。
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0.Add(3 * time.Millisecond), Seq: 4,
		Values: map[string]float64{"Sel": 0, "Door": 50}}); err != nil {
		t.Fatal(err)
	}
	old, _ := gw.GetSignal(257, "Gear", t0.Add(3*time.Millisecond))
	if old.Status != model.StatusInactive {
		t.Fatalf("switched-away branch must be INACTIVE, got %s", old.Status)
	}
	door, _ := gw.GetSignal(257, "Door", t0.Add(3*time.Millisecond))
	if door.Status != model.StatusOK || door.State.Value != 50 {
		t.Fatalf("active branch Door should be OK=50, got %s=%v", door.Status, door.State.Value)
	}

	// 切回选择值 1，布局 1 原始位（Gear=1）仍保留。
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0.Add(4 * time.Millisecond), Seq: 5,
		Values: map[string]float64{"Sel": 1}}); err != nil {
		t.Fatal(err)
	}
	back, _ := gw.GetSignal(257, "Gear", t0.Add(4*time.Millisecond))
	if back.Status != model.StatusOK || back.State.Value != 1 {
		t.Fatalf("branch raw bits must survive switch and back: status=%s value=%v", back.Status, back.State.Value)
	}
	t.Logf("[biz=inject-mux-switch-back][input=BODY mux 1->0->1] Gear INACTIVE at mux=0 then OK=1; INACTIVE distinct from TIMEOUT")
}

// 三类选择值矛盾各自可区分；任何失败都不留半成品、不污染已收到帧状态。
func TestInjectMuxThreeFailureClassesAndAtomicity(t *testing.T) {
	gw := newFeatureGateway(t)
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0, Seq: 1,
		Values: map[string]float64{"Sel": 1, "Gear": 3}}); err != nil {
		t.Fatal(err)
	}
	statsBefore := gw.Stats()
	tsBefore, _ := gw.LastTimestamp(257)

	cases := []struct {
		name   string
		values map[string]float64
		reason canbus.InjectReason
	}{
		{"selector non-integer", map[string]float64{"Sel": 1.5, "Gear": 1}, canbus.ReasonMuxSelectorInvalid},
		{"selector negative", map[string]float64{"Sel": -1, "Gear": 1}, canbus.ReasonMuxSelectorInvalid},
		{"selector exceeds bit width", map[string]float64{"Sel": 16, "Gear": 1}, canbus.ReasonMuxSelectorInvalid},
		{"selector beyond defined max", map[string]float64{"Sel": 9, "Gear": 1}, canbus.ReasonMuxSelectorInvalid},
		{"selector/layout mismatch", map[string]float64{"Sel": 0, "Gear": 1}, canbus.ReasonMuxLayoutMismatch},
		{"selector with no layout", map[string]float64{"Sel": 2}, canbus.ReasonInjectMuxLayoutMissing},
		{"out of range value", map[string]float64{"Sel": 1, "Gear": 99}, canbus.ReasonValueOutOfRange},
	}
	for _, c := range cases {
		_, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0.Add(10 * time.Millisecond), Seq: 99,
			Values: c.values})
		if err == nil || asInjectReason(t, err) != c.reason {
			t.Fatalf("%s: want %s got %v", c.name, c.reason, err)
		}
		t.Logf("[biz=inject-mux-reject][input=BODY %v] rejected reason=%s", c.values, c.reason)
	}

	statsAfter := gw.Stats()
	tsAfter, _ := gw.LastTimestamp(257)
	if statsAfter.TotalFrames != statsBefore.TotalFrames || statsAfter.DecodedOK != statsBefore.DecodedOK {
		t.Fatalf("failed injects entered decode pipeline: before=%+v after=%+v", statsBefore, statsAfter)
	}
	if !tsAfter.Equal(tsBefore) {
		t.Fatalf("last timestamp mutated by failed injects: %v -> %v", tsBefore, tsAfter)
	}
	gear, _ := gw.GetSignal(257, "Gear", t0.Add(10*time.Millisecond))
	if gear.State.Value != 3 || gear.Status != model.StatusOK {
		t.Fatalf("accepted frame state polluted: value=%v status=%s", gear.State.Value, gear.Status)
	}
	t.Logf("[biz=inject-atomic][input=7 failed BODY injects] stats/lastData/states unchanged; Gear still OK=3")

	_, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0.Add(-time.Millisecond), Seq: 100,
		Values: map[string]float64{"Sel": 1}})
	if err == nil || asInjectReason(t, err) != canbus.ReasonInjectTimestampRewound {
		t.Fatalf("rewound timestamp must be TIMESTAMP_REWOUND: %v", err)
	}
}

// 失败注入与热加载并发：任何观察者都不能看到半成品状态，失败注入不影响重载。
func TestInjectFailuresConcurrentWithReload(t *testing.T) {
	gw := newFeatureGateway(t)
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0, Seq: 1,
		Values: map[string]float64{"Sel": 1, "Gear": 3}}); err != nil {
		t.Fatal(err)
	}

	valid := featureDBC
	bad := "BO_ 9 X: 1 GW\n SG_ Bad : 0|99@1+ (1,0) [0,1] \"\" GW\n"

	var wg sync.WaitGroup
	var failures int64
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if _, err := gw.Inject(Injection{MessageID: 257,
				Timestamp: t0.Add(time.Duration(1000+i) * time.Millisecond), Seq: int64(i),
				Values: map[string]float64{"Sel": 9, "Gear": 1}}); err == nil {
				atomic.AddInt64(&failures, 1)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 60; i++ {
			if i%2 == 0 {
				if err := gw.Reload(strings.NewReader(bad)); err == nil {
					atomic.AddInt64(&failures, 1)
				}
			} else if err := gw.Reload(strings.NewReader(valid)); err != nil {
				atomic.AddInt64(&failures, 1)
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if v, ok := gw.GetSignal(257, "Gear", t0.Add(time.Hour)); ok {
				switch v.Status {
				case model.StatusOK, model.StatusInactive, model.StatusTimeout, model.StatusNeverReceived:
				default:
					atomic.AddInt64(&failures, 1)
				}
			}
		}
	}()
	wg.Wait()
	if atomic.LoadInt64(&failures) != 0 {
		t.Fatalf("concurrent failed-injects and reloads produced %d anomalies", failures)
	}
	t.Logf("[biz=inject-reload-race][input=200 mux-reject injects + 60 reloads + 200 queries] no torn state, failure count=0")
}
