package gateway

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

const featureDBC = `VERSION "x"
BO_ 256 ENGINE_DATA: 8 GW
 SG_ EngineSpeed : 0|16@1+ (0.25,0) [0,1000] "rpm" GW
 SG_ EngineTemp : 16|8@1+ (1,-40) [-40,100] "degC" GW
 SG_ RequestTorque : 31|16@0- (0.1,0) [-100,100] "Nm" GW
BO_ 257 BODY: 8 GW
 SG_ Sel M : 0|4@1+ (1,0) [0,2] "" GW
 SG_ Door m0 : 8|8@1+ (1,0) [0,100] "%" GW
 SG_ Gear m1 : 7|4@0+ (1,0) [0,3] "" GW
VAL_ 257 Gear 0 "P" 3 "D";
`

func newFeatureGateway(t *testing.T) *Gateway {
	t.Helper()
	db, err := dbc.Parse(strings.NewReader(featureDBC))
	if err != nil {
		t.Fatal(err)
	}
	return New(db, Config{DefaultTimeout: time.Hour})
}

func TestLargeDefinitionScale(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("VERSION \"scale\"\n")
	const messages = 500
	for i := 0; i < messages; i++ {
		id := 1000 + i
		builder.WriteString("BO_ " + strconv.Itoa(id) + " M" + strconv.Itoa(i) + ": 8 GW\n")
		builder.WriteString(fmt.Sprintf(" SG_ Speed%d : 0|16@1+ (0.25,0) [0,1000] \"rpm\" GW\n", i))
		builder.WriteString(fmt.Sprintf(" SG_ Temp%d : 16|8@1+ (1,-40) [-40,100] \"degC\" GW\n", i))
	}
	db, sum, err := dbc.ParseDetailed(strings.NewReader(builder.String()))
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.AdoptedMessages) != messages || len(db.Messages) != messages {
		t.Fatalf("scale parse mismatch messages=%d", len(sum.AdoptedMessages))
	}
	registry := NewBusRegistry()
	gw, _ := registry.AddBus("scale", db, Config{DefaultTimeout: time.Hour})
	for i := 0; i < messages; i++ {
		id := uint32(1000 + i)
		if _, err := gw.Inject(Injection{MessageID: id, Timestamp: t0.Add(time.Duration(i)), Values: map[string]float64{
			fmt.Sprintf("Speed%d", i): 500,
		}}); err != nil {
			t.Fatalf("inject id=%d: %v", id, err)
		}
	}
	all := registry.SnapshotAll(t0.Add(time.Duration(messages)))
	if len(all) != 1 || len(all[0].Signals) != messages*2 || all[0].Stats.DecodedOK != messages {
		t.Fatalf("scale summary mismatch: buses=%d signals=%d ok=%d", len(all), len(all[0].Signals), all[0].Stats.DecodedOK)
	}
	t.Logf("[biz=scale-definition][input=%d messages/%d signals] parse, preserve-inject, per-bus and aggregate snapshots complete", messages, messages*2)
}

func asInjectReason(t *testing.T, err error) canbus.InjectReason {
	t.Helper()
	var ie *canbus.InjectError
	if !errors.As(err, &ie) {
		t.Fatalf("expected *InjectError, got %T: %v", err, err)
	}
	return ie.Reason
}

func TestInjectRoundTripPreserveAndInactive(t *testing.T) {
	gw := newFeatureGateway(t)
	first, err := gw.Inject(Injection{MessageID: 256, Timestamp: t0, Values: map[string]float64{
		"EngineSpeed": 800, "EngineTemp": 90, "RequestTorque": -12.5,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Updated) != 3 {
		t.Fatalf("first injection updated %d signals", len(first.Updated))
	}
	second, err := gw.Inject(Injection{MessageID: 256, Timestamp: t0.Add(time.Millisecond), Values: map[string]float64{
		"EngineSpeed": 801,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Updated) != 3 {
		t.Fatalf("frame decode updates all active signals, got %d", len(second.Updated))
	}
	temp, _ := gw.GetSignal(256, "EngineTemp", t0.Add(time.Millisecond))
	torque, _ := gw.GetSignal(256, "RequestTorque", t0.Add(time.Millisecond))
	if temp.State.Value != 90 || torque.State.Value != -12.5 {
		t.Fatalf("unspecified signals did not preserve prior raw bits: temp=%v torque=%v", temp.State.Value, torque.State.Value)
	}
	speed, _ := gw.GetSignal(256, "EngineSpeed", t0.Add(time.Millisecond))
	if math.Abs(speed.State.Value-801) > 0.125 {
		t.Fatalf("quantized round trip outside tolerance: %v", speed.State.Value)
	}
	t.Logf("[biz=inject-preserve][input=ENGINE_DATA first=Speed/Temp/Torque then Speed] raw unspecified bits preserved: Temp=%v Torque=%v", temp.State.Value, torque.State.Value)

	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0, Values: map[string]float64{"Sel": 1, "Gear": 3}}); err != nil {
		t.Fatal(err)
	}
	gear, _ := gw.GetSignal(257, "Gear", t0)
	if gear.State.Value != 3 || gear.State.Label != "D" || !gear.State.HasLabel {
		t.Fatalf("enum label not decoded: %+v", gear.State)
	}
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0.Add(time.Millisecond), Values: map[string]float64{"Sel": 0, "Door": 50}}); err != nil {
		t.Fatal(err)
	}
	oldGear, _ := gw.GetSignal(257, "Gear", t0.Add(time.Millisecond))
	if oldGear.Status != model.StatusInactive {
		t.Fatalf("old mux branch must be INACTIVE not timeout/ok, got %s reason=%q", oldGear.Status, oldGear.Reason)
	}
	t.Logf("[biz=inject-mux-inactive][input=BODY mux=1 Gear=D then mux=0 Door=50] Gear status=INACTIVE distinct from TIMEOUT; label=%q", oldGear.State.Label)
}

func TestInjectRejectionClassesAndNoHalfState(t *testing.T) {
	gw := newFeatureGateway(t)
	if _, err := gw.Inject(Injection{MessageID: 404, Timestamp: t0, Values: map[string]float64{"X": 1}}); err == nil ||
		asInjectReason(t, err) != canbus.ReasonInjectUnknownMessage {
		t.Fatalf("unknown message reason mismatch: %v", err)
	}
	if _, err := gw.Inject(Injection{MessageID: 256, Timestamp: t0, Values: map[string]float64{"Ghost": 1}}); err == nil ||
		asInjectReason(t, err) != canbus.ReasonUnknownSignal {
		t.Fatalf("unknown signal reason mismatch: %v", err)
	}
	if _, err := gw.Inject(Injection{MessageID: 256, Timestamp: t0, Values: map[string]float64{"EngineSpeed": 9999}}); err == nil ||
		asInjectReason(t, err) != canbus.ReasonValueOutOfRange {
		t.Fatalf("range reason mismatch: %v", err)
	}
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0, Values: map[string]float64{"Gear": 1}}); err == nil ||
		asInjectReason(t, err) != canbus.ReasonMuxSelectorInvalid {
		t.Fatalf("missing selector reason mismatch: %v", err)
	}
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0, Values: map[string]float64{"Sel": 9, "Gear": 1}}); err == nil ||
		asInjectReason(t, err) != canbus.ReasonMuxSelectorInvalid {
		t.Fatalf("illegal selector reason mismatch: %v", err)
	}
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0, Values: map[string]float64{"Sel": 0, "Gear": 1}}); err == nil ||
		asInjectReason(t, err) != canbus.ReasonMuxLayoutMismatch {
		t.Fatalf("selector/layout mismatch reason: %v", err)
	}
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0, Values: map[string]float64{"Sel": 2}}); err == nil ||
		asInjectReason(t, err) != canbus.ReasonInjectMuxLayoutMissing {
		t.Fatalf("missing layout reason: %v", err)
	}
	if _, ok := gw.GetSignal(256, "EngineSpeed", t0); !ok {
		t.Fatal("signal must exist")
	} else if ok && gw.Stats().TotalFrames != 0 {
		t.Fatalf("failed injections must not enter decode pipeline: stats=%+v", gw.Stats())
	}
	t.Logf("[biz=inject-rejections][input=unknown/range/three-mux-classes] all rejected distinctly and no accepted frame state was created")
}

func TestMultiBusIsolationAndConcurrentSummary(t *testing.T) {
	registry := NewBusRegistry()
	dbA := newFeatureGateway(t).DB()
	dbB := newFeatureGateway(t).DB()
	a, added := registry.AddBus("A", dbA, Config{DefaultTimeout: time.Hour})
	if !added {
		t.Fatal("bus A add")
	}
	b, added := registry.AddBus("B", dbB, Config{DefaultTimeout: time.Hour})
	if !added {
		t.Fatal("bus B add")
	}
	if _, err := registry.MustGet("C"); err == nil {
		t.Fatal("unknown bus must be distinguishable")
	}
	if _, err := a.Inject(Injection{MessageID: 256, Timestamp: t0, Values: map[string]float64{"EngineSpeed": 800}}); err != nil {
		t.Fatal(err)
	}
	vA, _ := a.GetSignal(256, "EngineSpeed", t0)
	vB, _ := b.GetSignal(256, "EngineSpeed", t0)
	if vA.Status != model.StatusOK || vB.Status != model.StatusNeverReceived {
		t.Fatalf("bus isolation mismatch: A=%s B=%s", vA.Status, vB.Status)
	}
	all := registry.SnapshotAll(t0)
	if len(all) != 2 || all[0].Name != "A" || all[0].Stats.DecodedOK != 1 || all[1].Stats.DecodedOK != 0 {
		t.Fatalf("aggregate summary differs from per-bus view: %+v", all)
	}
	var wg sync.WaitGroup
	busClock := []int64{0, 0}
	var injectMu [2]sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				busIndex := n % 2
				gw, _ := registry.Get([]string{"A", "B"}[busIndex])
				step := atomic.AddInt64(&busClock[busIndex], 1)
				ts := t0.Add(time.Duration(step) * time.Millisecond)
				injectMu[busIndex].Lock()
				_, _ = gw.Inject(Injection{MessageID: 256, Timestamp: ts, Values: map[string]float64{"EngineSpeed": float64(100 + i%800)}})
				injectMu[busIndex].Unlock()
				_, _ = gw.GetSignal(256, "EngineSpeed", ts)
				_ = registry.SnapshotAll(ts)
			}
		}(i)
	}
	wg.Wait()
	if a.Stats().Rejected != 0 || b.Stats().Rejected != 0 {
		t.Fatalf("race workload produced unexpected rejections: A=%+v B=%+v", a.Stats(), b.Stats())
	}
	t.Logf("[biz=multi-bus-isolation-race][input=A/B concurrent inject/query/snapshot] summaries consistent and reject counters A=%d B=%d", a.Stats().Rejected, b.Stats().Rejected)
}
