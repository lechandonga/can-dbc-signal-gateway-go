package gateway

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

const coverageDBC = `VERSION "coverage"
BO_ 256 ENGINE_DATA: 8 GW
 SG_ EngineSpeed : 0|16@1+ (0.25,0) [0,16383.75] "rpm" GW
 SG_ DiagLow : 16|4@1+ (1,0) [0,15] "" GW
 SG_ DiagNibble : 23|4@0+ (1,0) [0,15] "" GW
 SG_ Counter : 24|4@1+ (1,0) [0,15] "" GW
 SG_ EngineTemp : 32|8@1+ (1,-40) [-40,215] "degC" GW
 SG_ RequestTorque : 55|16@0- (0.5,0) [-16384,16383.5] "Nm" GW
BO_ 257 BODY: 8 GW
 SG_ Sel M : 0|4@1+ (1,0) [0,1] "" GW
 SG_ Alive : 4|4@1+ (1,0) [0,15] "" GW
 SG_ Door m0 : 8|8@1+ (1,0) [0,100] "%" GW
 SG_ Gear m1 : 23|8@0+ (1,0) [0,255] "" GW
`

func newCoverageGateway(t *testing.T) *Gateway {
	t.Helper()
	db, err := dbc.Parse(strings.NewReader(coverageDBC))
	if err != nil {
		t.Fatal(err)
	}
	return New(db, Config{DefaultTimeout: time.Hour})
}

// TestInjectOverwriteLargeThenSmall 复现并锁定"先大后小按位 OR"缺陷：
// 同一信号连续注入必须完整覆盖，回读在半个量化步长内等于写入值。
func TestInjectOverwriteLargeThenSmall(t *testing.T) {
	gw := newCoverageGateway(t)
	write := func(v float64, at time.Duration) {
		t.Helper()
		if _, err := gw.Inject(Injection{MessageID: 256, Timestamp: t0.Add(at),
			Values: map[string]float64{"EngineSpeed": v}}); err != nil {
			t.Fatalf("inject speed=%v: %v", v, err)
		}
	}
	read := func() float64 {
		v, _ := gw.GetSignal(256, "EngineSpeed", t0)
		return v.State.Value
	}
	write(800, 0)
	if got := read(); math.Abs(got-800) > 0.125 {
		t.Fatalf("after 800 read %v", got)
	}
	write(100, time.Millisecond)
	if got := read(); math.Abs(got-100) > 0.125 {
		t.Fatalf("after 100 read %v (stale high bits OR-ed in)", got)
	}
	write(0, 2*time.Millisecond)
	if got := read(); got != 0 {
		t.Fatalf("after 0 read %v", got)
	}
	write(16383.75, 3*time.Millisecond)
	if got := read(); math.Abs(got-16383.75) > 1e-9 {
		t.Fatalf("max value read %v", got)
	}
	write(25.25, 4*time.Millisecond)
	if got := read(); math.Abs(got-25.25) > 1e-9 {
		t.Fatalf("25.25 read %v", got)
	}
	t.Logf("[biz=inject-overwrite][input=EngineSpeed 800->100->0->16383.75->25.25] every read-back within 0.125rpm half-quantum")

	tq := func(v float64, at time.Duration) {
		t.Helper()
		if _, err := gw.Inject(Injection{MessageID: 256, Timestamp: t0.Add(at),
			Values: map[string]float64{"RequestTorque": v}}); err != nil {
			t.Fatalf("inject torque=%v: %v", v, err)
		}
	}
	readTQ := func() float64 {
		v, _ := gw.GetSignal(256, "RequestTorque", t0)
		return v.State.Value
	}
	tq(1000.5, 10*time.Millisecond)
	if got := readTQ(); math.Abs(got-1000.5) > 0.25 {
		t.Fatalf("moto signed after 1000.5 read %v", got)
	}
	tq(-300.5, 11*time.Millisecond)
	if got := readTQ(); math.Abs(got+300.5) > 0.25 {
		t.Fatalf("moto signed after -300.5 read %v", got)
	}
	tq(12.5, 12*time.Millisecond)
	if got := readTQ(); math.Abs(got-12.5) > 0.25 {
		t.Fatalf("moto signed after 12.5 read %v", got)
	}
	t.Logf("[biz=inject-overwrite-signed-moto][input=RequestTorque 1000.5->-300.5->12.5] signed motorola overwrite exact within 0.25")
}

// TestInjectSingleSignalAndPreserve 覆盖只写单个信号：从未给过的位按全 0 默认，
// 给过的位在后续帧保持原始位；相邻混排字节序信号互不污染。
func TestInjectSingleSignalAndPreserve(t *testing.T) {
	gw := newCoverageGateway(t)
	if _, err := gw.Inject(Injection{MessageID: 256, Timestamp: t0,
		Values: map[string]float64{"EngineTemp": 90}}); err != nil {
		t.Fatal(err)
	}
	temp, _ := gw.GetSignal(256, "EngineTemp", t0)
	speed, _ := gw.GetSignal(256, "EngineSpeed", t0)
	counter, _ := gw.GetSignal(256, "Counter", t0)
	nibble, _ := gw.GetSignal(256, "DiagNibble", t0)
	if temp.State.Value != 90 || speed.State.Value != 0 || counter.State.Value != 0 || nibble.State.Value != 0 {
		t.Fatalf("first single-signal inject defaults wrong: temp=%v speed=%v counter=%v nibble=%v",
			temp.State.Value, speed.State.Value, counter.State.Value, nibble.State.Value)
	}
	t.Logf("[biz=inject-single-default][input=only EngineTemp=90 first frame] temp=90, unwritten Intel/Motorola bits default raw=0")

	if _, err := gw.Inject(Injection{MessageID: 256, Timestamp: t0.Add(time.Millisecond),
		Values: map[string]float64{"DiagNibble": 13, "Counter": 7}}); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Inject(Injection{MessageID: 256, Timestamp: t0.Add(2 * time.Millisecond),
		Values: map[string]float64{"EngineSpeed": 5}}); err != nil {
		t.Fatal(err)
	}
	speed, _ = gw.GetSignal(256, "EngineSpeed", t0)
	nibble, _ = gw.GetSignal(256, "DiagNibble", t0)
	counter, _ = gw.GetSignal(256, "Counter", t0)
	temp, _ = gw.GetSignal(256, "EngineTemp", t0)
	if speed.State.Value != 5 || nibble.State.Value != 13 || counter.State.Value != 7 || temp.State.Value != 90 {
		t.Fatalf("adjacent mixed-endian overwrite leaked: speed=%v nibble=%v counter=%v temp=%v",
			speed.State.Value, nibble.State.Value, counter.State.Value, temp.State.Value)
	}
	t.Logf("[biz=inject-mixed-endian-boundary][input=nibble=13,counter=7 then speed=5] adjacent Intel/Motorola bits independent; temp held at 90")

	if _, err := gw.Inject(Injection{MessageID: 256, Timestamp: t0.Add(3 * time.Millisecond),
		Values: map[string]float64{"DiagNibble": 1}}); err != nil {
		t.Fatal(err)
	}
	speed, _ = gw.GetSignal(256, "EngineSpeed", t0)
	counter, _ = gw.GetSignal(256, "Counter", t0)
	temp, _ = gw.GetSignal(256, "EngineTemp", t0)
	if speed.State.Value != 5 || counter.State.Value != 7 || temp.State.Value != 90 {
		t.Fatalf("unspecified raw bits not preserved: speed=%v counter=%v temp=%v",
			speed.State.Value, counter.State.Value, temp.State.Value)
	}
	t.Logf("[biz=inject-preserve-raw][input=only DiagNibble=1] speed/counter/temp keep last accepted raw bits")
}

// TestInjectMuxKeepsBranchBitsAndStatic 锁定复用保持语义：
// 选择值不变且只再给选择值时，分支信号必须保持上一帧原始位而非清零；
// 只写普通信号（不提选择值）也能正常构造且只动写出的信号。
func TestInjectMuxKeepsBranchBitsAndStatic(t *testing.T) {
	gw := newCoverageGateway(t)
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0,
		Values: map[string]float64{"Sel": 1, "Gear": 3, "Alive": 5}}); err != nil {
		t.Fatal(err)
	}
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0.Add(time.Millisecond),
		Values: map[string]float64{"Sel": 1}}); err != nil {
		t.Fatal(err)
	}
	gear, _ := gw.GetSignal(257, "Gear", t0.Add(time.Millisecond))
	sel, _ := gw.GetSignal(257, "Sel", t0.Add(time.Millisecond))
	alive, _ := gw.GetSignal(257, "Alive", t0.Add(time.Millisecond))
	if gear.State.Value != 3 || gear.Status != model.StatusOK {
		t.Fatalf("branch bits must be retained when selector stays: gear=%v status=%s", gear.State.Value, gear.Status)
	}
	if sel.State.Value != 1 || alive.State.Value != 5 {
		t.Fatalf("selector/static must be present: sel=%v alive=%v", sel.State.Value, alive.State.Value)
	}
	t.Logf("[biz=inject-mux-keep][input=Sel=1,Gear=3,Alive=5 then only Sel=1] Gear retained=3 status=OK, static Alive=5")

	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0.Add(2 * time.Millisecond),
		Values: map[string]float64{"Alive": 9}}); err != nil {
		t.Fatalf("static-only inject with implicit selector must succeed: %v", err)
	}
	alive, _ = gw.GetSignal(257, "Alive", t0.Add(2*time.Millisecond))
	gear, _ = gw.GetSignal(257, "Gear", t0.Add(2*time.Millisecond))
	if alive.State.Value != 9 || gear.State.Value != 3 {
		t.Fatalf("static-only inject moved branch bits: alive=%v gear=%v", alive.State.Value, gear.State.Value)
	}
	t.Logf("[biz=inject-mux-static-only][input=only Alive=9] implicit selector=1 reused; Gear still 3")

	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0.Add(3 * time.Millisecond),
		Values: map[string]float64{"Sel": 0, "Door": 42}}); err != nil {
		t.Fatal(err)
	}
	oldGear, _ := gw.GetSignal(257, "Gear", t0.Add(3*time.Millisecond))
	door, _ := gw.GetSignal(257, "Door", t0.Add(3*time.Millisecond))
	if oldGear.Status != model.StatusInactive || oldGear.State.Value != 3 {
		t.Fatalf("old branch must be INACTIVE with last value kept: status=%s val=%v", oldGear.Status, oldGear.State.Value)
	}
	if door.State.Value != 42 || door.Status != model.StatusOK {
		t.Fatalf("new branch door: val=%v status=%s", door.State.Value, door.Status)
	}
	t.Logf("[biz=inject-mux-switch-inactive][input=switch Sel 1->0 Door=42] Gear status=INACTIVE (not TIMEOUT) last=3")
}

// TestInjectMuxThreeFailureClassesAtomic 三类复用矛盾各自可区分；
// 任何失败都不留半成品、不污染已收到帧状态。
func TestInjectMuxThreeFailureClassesAtomic(t *testing.T) {
	gw := newCoverageGateway(t)
	if _, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0,
		Values: map[string]float64{"Sel": 1, "Gear": 3}}); err != nil {
		t.Fatal(err)
	}
	beforeGear, _ := gw.GetSignal(257, "Gear", t0)
	beforeTS, hadTS := gw.LastTimestamp(257)
	dataBefore := append([]byte(nil), gw.lastData[257]...)

	try := func(values map[string]float64, want canbus.InjectReason, id string) {
		t.Helper()
		_, err := gw.Inject(Injection{MessageID: 257, Timestamp: t0.Add(time.Millisecond), Values: values})
		if err == nil {
			t.Fatalf("%s: want %s, got success", id, want)
		}
		var ie *canbus.InjectError
		if !errors.As(err, &ie) || ie.Reason != want {
			t.Fatalf("%s: want %s, got %v", id, want, err)
		}
		t.Logf("[biz=inject-mux-fail-class][input=%s] rejected reason=%s: %s", id, want, ie.Detail)
	}
	// 选择值本身非法：16 超出 4 位选择器位宽。
	try(map[string]float64{"Sel": 16, "Gear": 1}, canbus.ReasonMuxSelectorInvalid, "Sel=16 out of selector bits")
	// 选择值和目标信号所在布局对不上：Sel=0 但写布局 1 的 Gear。
	try(map[string]float64{"Sel": 0, "Gear": 1}, canbus.ReasonMuxLayoutMismatch, "Sel=0 with layout-1 Gear")

	// 构造"选择值合法但该值下没有可写布局"：加载 Sel 范围到 2 但只有 m0/m1 分支的定义。
	noLayout := strings.Replace(coverageDBC, "SG_ Sel M : 0|4@1+ (1,0) [0,1]", "SG_ Sel M : 0|4@1+ (1,0) [0,2]", 1)
	db, err := dbc.Parse(strings.NewReader(noLayout))
	if err != nil {
		t.Fatal(err)
	}
	gw2 := New(db, Config{DefaultTimeout: time.Hour})
	if _, err := gw2.Inject(Injection{MessageID: 257, Timestamp: t0,
		Values: map[string]float64{"Sel": 0, "Door": 1}}); err != nil {
		t.Fatal(err)
	}
	_, err = gw2.Inject(Injection{MessageID: 257, Timestamp: t0.Add(time.Millisecond),
		Values: map[string]float64{"Sel": 2}})
	if err == nil || asInjectReason(t, err) != canbus.ReasonInjectMuxLayoutMissing {
		t.Fatalf("selector with no layout: want MUX_LAYOUT_MISSING, got %v", err)
	}
	t.Logf("[biz=inject-mux-fail-class][input=Sel=2 no branch layout] rejected reason=MUX_LAYOUT_MISSING")

	// 原子性：失败后已接受帧的时间戳、原始数据和信号值保持不变。
	afterGear, _ := gw.GetSignal(257, "Gear", t0)
	afterTS, _ := gw.LastTimestamp(257)
	if afterGear.State.Value != beforeGear.State.Value || afterGear.State.Raw != beforeGear.State.Raw {
		t.Fatalf("failed inject polluted signal state: before=%+v after=%+v", beforeGear.State, afterGear.State)
	}
	if !hadTS || !afterTS.Equal(beforeTS) {
		t.Fatalf("failed inject moved last timestamp: %v -> %v", beforeTS, afterTS)
	}
	if string(gw.lastData[257]) != string(dataBefore) {
		t.Fatalf("failed inject polluted last accepted frame: %x -> %x", dataBefore, gw.lastData[257])
	}
	stats := gw.Stats()
	if stats.TotalFrames != 1 || stats.DecodedOK != 1 || stats.Rejected != 0 {
		t.Fatalf("failed injects must not enter decode pipeline: %+v", stats)
	}
	t.Logf("[biz=inject-fail-atomic][input=2 rejected builds on BODY] last frame/state unchanged; pipeline counts total=1 ok=1 rejected=0")
}
