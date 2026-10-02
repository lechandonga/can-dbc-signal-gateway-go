package gateway

import (
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/cansim"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

var t0 = time.Unix(0, 0)

func openDemo(t *testing.T) *gatewayLike {
	t.Helper()
	f, err := os.Open("../../testdata/demo.dbc")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	db, err := dbc.Parse(f)
	if err != nil {
		t.Fatalf("demo.dbc must parse: %v", err)
	}
	return &gatewayLike{gw: New(db, Config{DefaultTimeout: 100 * time.Millisecond,
		SignalTimeouts: map[string]time.Duration{"ENGINE_DATA.EngineSpeed": 250 * time.Millisecond}})}
}

func ptrInt(v int) *int { return &v }

type gatewayLike struct {
	gw *Gateway
}

func asReason(t *testing.T, err error) canbus.DecodeReason {
	t.Helper()
	var de *canbus.DecodeError
	if !errors.As(err, &de) {
		t.Fatalf("expected *DecodeError, got %T: %v", err, err)
	}
	return de.Reason
}

func TestDecodeEndiannessSignedCrossByte(t *testing.T) {
	// 业务：跨字节 Intel 无符号、Intel 有符号、Motorola 跨字节无符号。
	gl := openDemo(t)
	gw := gl.gw
	b := cansim.NewBuilder(gw.DB())

	// EngineSpeed: 0|16@1+ factor .25；raw 3200 -> 800 rpm；字节布局 0x80,0x0C
	f, err := b.Frame(256, nil, map[string]float64{"EngineSpeed": 800, "EngineTemp": 90, "RequestTorque": -12.5})
	if err != nil {
		t.Fatal(err)
	}
	f.Timestamp = t0
	res, derr := gw.HandleFrame(f)
	if derr != nil {
		t.Fatalf("ENGINE_DATA should decode: %v", derr)
	}
	vals := map[string]float64{}
	raws := map[string]uint64{}
	for _, st := range res.Updated {
		vals[st.SignalName] = st.Value
		raws[st.SignalName] = st.Raw
	}
	if raws["EngineSpeed"] != 3200 || vals["EngineSpeed"] != 800 {
		t.Fatalf("EngineSpeed raw=%d val=%v, want 3200/800", raws["EngineSpeed"], vals["EngineSpeed"])
	}
	// EngineTemp 16|8@1- (1,-40)：90 -> raw130
	if raws["EngineTemp"] != 130 || vals["EngineTemp"] != 90 {
		t.Fatalf("EngineTemp raw=%d val=%v, want 130/90", raws["EngineTemp"], vals["EngineTemp"])
	}
	// RequestTorque 24|16@1- factor .1：-12.5 -> raw=-125 = 0xFF83
	if raws["RequestTorque"] != 0xFF83 || vals["RequestTorque"] != -12.5 {
		t.Fatalf("RequestTorque raw=%#x val=%v, want 0xff83/-12.5", raws["RequestTorque"], vals["RequestTorque"])
	}
	t.Logf("[biz=decode-endian-signed][input=ENGINE_DATA t=0] raw Speed=%d Temp=%d Torque=%#x -> physical matches definitions",
		raws["EngineSpeed"], raws["EngineTemp"], raws["RequestTorque"])

	// Motorola 跨字节：DIAG_PLAIN DiagWordMoto 7|16@0；raw=0xABCD
	f2, err := b.Frame(258, nil, map[string]float64{"DiagWordMoto": 0xABCD, "DiagByte": 0x42})
	if err != nil {
		t.Fatal(err)
	}
	f2.Timestamp = t0
	res2, derr := gw.HandleFrame(f2)
	if derr != nil {
		t.Fatalf("DIAG_PLAIN should decode: %v", derr)
	}
	for _, st := range res2.Updated {
		t.Logf("[biz=decode-endian-signed][input=DIAG_PLAIN] %s raw=%#x val=%v bytes=%x", st.SignalName, st.Raw, st.Value, f2.Data)
	}
	view, _ := gw.GetSignal(258, "DiagWordMoto", t0)
	if view.State.Raw != 0xABCD {
		t.Fatalf("DiagWordMoto raw=%#x, want 0xabcd; data=%x", view.State.Raw, f2.Data)
	}
	view2, _ := gw.GetSignal(258, "DiagByte", t0)
	if view2.State.Raw != 0x42 {
		t.Fatalf("DiagByte raw=%#x, want 0x42; data=%x", view2.State.Raw, f2.Data)
	}
}

func TestMultiplexLayouts(t *testing.T) {
	gl := openDemo(t)
	gw := gl.gw
	b := cansim.NewBuilder(gw.DB())

	mk := func(mux int, at time.Duration) canbus.Frame {
		f, err := b.Frame(257, &mux, map[string]float64{"DoorOpenDeg": 77, "GearEngaged": 3, "GearLeverPos": 200})
		if err != nil {
			t.Fatal(err)
		}
		f.Timestamp = t0.Add(at)
		return f
	}

	r0, err := gw.HandleFrame(mk(0, 0))
	if err != nil {
		t.Fatalf("mux=0 must decode: %v", err)
	}
	got := map[string]bool{}
	for _, st := range r0.Updated {
		got[st.SignalName] = true
	}
	if !got["StatusType"] || !got["DoorOpenDeg"] || got["GearEngaged"] {
		t.Fatalf("mux=0 updated=%v, want StatusType+DoorOpenDeg only", got)
	}
	v, _ := gw.GetSignal(257, "DoorOpenDeg", t0)
	if v.State.Value != 77 || v.State.MuxValue == nil || *v.State.MuxValue != 0 {
		t.Fatalf("DoorOpenDeg value=%v mux=%v", v.State.Value, v.State.MuxValue)
	}
	t.Logf("[biz=multiplex][input=BODY_STATUS mux=0 t=0] decoded layout-0 signals=%v", r0.Updated)

	r1, err := gw.HandleFrame(mk(1, 10*time.Millisecond))
	if err != nil {
		t.Fatalf("mux=1 must decode: %v", err)
	}
	for _, st := range r1.Updated {
		got[st.SignalName] = true
	}
	if !got["GearEngaged"] || !got["GearLeverPos"] {
		t.Fatalf("mux=1 missing gear signals: %v", got)
	}
	t.Logf("[biz=multiplex][input=BODY_STATUS mux=1 t=10ms] decoded layout-1 signals=%v", r1.Updated)

	// 非法选择值：无对应布局 -> 可区分原因，且不污染状态。
	bad := mk(9, 20*time.Millisecond)
	_, err = gw.HandleFrame(bad)
	if asReason(t, err) != canbus.ReasonMuxLayoutMissing {
		t.Fatalf("want MUX_LAYOUT_MISSING, got %v", err)
	}
	t.Logf("[biz=multiplex-illegal][input=BODY_STATUS mux=9 t=20ms] rejected reason=MUX_LAYOUT_MISSING")

	// mux=0 的 DoorOpenDeg 时间戳必须仍是 t=0（坏帧不污染）。
	v, _ = gw.GetSignal(257, "DoorOpenDeg", t0.Add(20*time.Millisecond))
	if !v.State.UpdatedAt.Equal(t0) {
		t.Fatalf("state polluted by illegal mux frame: %v", v.State.UpdatedAt)
	}
}

func TestRejectUnknownLengthTruncatedNoPollution(t *testing.T) {
	gl := openDemo(t)
	gw := gl.gw
	b := cansim.NewBuilder(gw.DB())

	good, _ := b.Frame(256, nil, map[string]float64{"EngineSpeed": 800})
	good.Timestamp = t0
	if _, err := gw.HandleFrame(good); err != nil {
		t.Fatal(err)
	}
	before, _ := gw.GetSignal(256, "EngineSpeed", t0)

	// 未知报文：拒绝。
	_, err := gw.HandleFrame(canbus.Frame{ID: 0x7FF, Data: make([]byte, 8), Timestamp: t0.Add(time.Millisecond)})
	if asReason(t, err) != canbus.ReasonUnknownMessage {
		t.Fatalf("want UNKNOWN_MESSAGE, got %v", err)
	}
	t.Logf("[biz=reject-unknown][input=id=0x7FF dlc=8] rejected reason=UNKNOWN_MESSAGE")

	// 长度大于 DLC：LENGTH_MISMATCH。
	long := good
	long.Data = make([]byte, 9)
	long.Timestamp = t0.Add(2 * time.Millisecond)
	_, err = gw.HandleFrame(long)
	if asReason(t, err) != canbus.ReasonLengthMismatch {
		t.Fatalf("want LENGTH_MISMATCH, got %v", err)
	}
	t.Logf("[biz=reject-length][input=id=256 dlc=9] rejected reason=LENGTH_MISMATCH")

	// 长度小于 DLC：TRUNCATED_FRAME（与 LENGTH_MISMATCH 可区分）。
	trunc := cansim.Truncate(good, 3)
	trunc.Timestamp = t0.Add(3 * time.Millisecond)
	_, err = gw.HandleFrame(trunc)
	if asReason(t, err) != canbus.ReasonTruncatedFrame {
		t.Fatalf("want TRUNCATED_FRAME, got %v", err)
	}
	t.Logf("[biz=reject-truncated][input=id=256 dlc=3<8] rejected reason=TRUNCATED_FRAME")

	after, _ := gw.GetSignal(256, "EngineSpeed", t0.Add(3*time.Millisecond))
	if after.State.Raw != before.State.Raw || !after.State.UpdatedAt.Equal(before.State.UpdatedAt) {
		t.Fatalf("rejected frames polluted state: before=%+v after=%+v", before.State, after.State)
	}
	t.Logf("[biz=no-pollution][input=3 rejected frames] EngineSpeed unchanged raw=%d updatedAt=%v",
		after.State.Raw, after.State.UpdatedAt)
}

func TestTimeoutAndRecovery(t *testing.T) {
	gl := openDemo(t)
	gw := gl.gw
	b := cansim.NewBuilder(gw.DB())

	mk := func(speed float64, at time.Duration) canbus.Frame {
		f, _ := b.Frame(256, nil, map[string]float64{"EngineSpeed": speed, "EngineTemp": 90})
		f.Timestamp = t0.Add(at)
		return f
	}

	if _, err := gw.HandleFrame(mk(800, 0)); err != nil {
		t.Fatal(err)
	}
	// 偶发丢帧：在默认窗口（EngineTemp=100ms）边界前，100ms+ε 才超时；90ms 仍然 OK。
	v, _ := gw.GetSignal(256, "EngineTemp", t0.Add(90*time.Millisecond))
	if v.Status != model.StatusOK {
		t.Fatalf("90ms < 100ms timeout must be OK, got %s", v.Status)
	}
	// EngineSpeed 单独配置 250ms：200ms 仍 OK，说明偶发丢帧不误判。
	vs, _ := gw.GetSignal(256, "EngineSpeed", t0.Add(200*time.Millisecond))
	if vs.Status != model.StatusOK {
		t.Fatalf("200ms < 250ms speed timeout must be OK, got %s", vs.Status)
	}
	t.Logf("[biz=timeout-occluded][input=query@90ms/200ms] both OK; per-signal 250ms vs default 100ms respected")

	// 持续无更新：EngineTemp 在 101ms 失效；EngineSpeed 在 251ms 失效。
	v, _ = gw.GetSignal(256, "EngineTemp", t0.Add(101*time.Millisecond))
	if v.Status != model.StatusTimeout {
		t.Fatalf("EngineTemp must be TIMEOUT at 101ms, got %s", v.Status)
	}
	vs, _ = gw.GetSignal(256, "EngineSpeed", t0.Add(251*time.Millisecond))
	if vs.Status != model.StatusTimeout {
		t.Fatalf("EngineSpeed must be TIMEOUT at 251ms, got %s", vs.Status)
	}
	t.Logf("[biz=timeout][input=query@101ms/251ms] EngineTemp/EngineSpeed marked TIMEOUT: %q / %q",
		v.Reason, vs.Reason)

	// 恢复：新帧到达后回到 OK，UpdatedAt 为新帧时间。
	if _, err := gw.HandleFrame(mk(1200, 300*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	vs, _ = gw.GetSignal(256, "EngineSpeed", t0.Add(301*time.Millisecond))
	if vs.Status != model.StatusOK || vs.State.Value != 1200 {
		t.Fatalf("EngineSpeed must recover OK=1200, got %s val=%v", vs.Status, vs.State.Value)
	}
	v, _ = gw.GetSignal(256, "EngineTemp", t0.Add(301*time.Millisecond))
	if v.Status != model.StatusOK || v.State.Value != 90 {
		t.Fatalf("EngineTemp must recover, got %s val=%v", v.Status, v.State.Value)
	}
	t.Logf("[biz=recovery][input=frame@300ms query@301ms] both signals OK again")

	// 从未收到：DIAG_PLAIN 在本用例中从未发送。
	nr, ok := gw.GetSignal(258, "DiagWordMoto", t0.Add(301*time.Millisecond))
	if !ok {
		t.Fatal("DiagWordMoto must exist in definitions")
	}
	if nr.Status != model.StatusNeverReceived {
		t.Fatalf("DiagWordMoto must be NEVER_RECEIVED, got %s", nr.Status)
	}
	// 定义中不存在的信号：ok=false，与"从未收到"区分。
	if _, ok := gw.GetSignal(256, "NoSuchSignal", t0); ok {
		t.Fatal("unknown signal must return ok=false")
	}
	t.Logf("[biz=never-received][input=DIAG_PLAIN never sent] status=NEVER_RECEIVED; unknown signal -> ok=false")
}

func TestTimestampRewindRejected(t *testing.T) {
	gl := openDemo(t)
	gw := gl.gw
	b := cansim.NewBuilder(gw.DB())
	f1, _ := b.Frame(256, nil, map[string]float64{"EngineSpeed": 800})
	f1.Timestamp = t0.Add(100 * time.Millisecond)
	if _, err := gw.HandleFrame(f1); err != nil {
		t.Fatal(err)
	}
	// 不同报文互不影响：257 的更早时间戳应被接受（按 ID 独立单调）。
	f2, _ := b.Frame(257, ptrInt(0), map[string]float64{"DoorOpenDeg": 10})
	f2.Timestamp = t0
	if _, err := gw.HandleFrame(f2); err != nil {
		t.Fatalf("other message id earlier ts must be accepted: %v", err)
	}
	// 同 ID 回退：拒绝。
	f3, _ := b.Frame(256, nil, map[string]float64{"EngineSpeed": 900})
	f3.Timestamp = t0.Add(50 * time.Millisecond)
	_, err := gw.HandleFrame(f3)
	if asReason(t, err) != canbus.ReasonTimestampRewound {
		t.Fatalf("want TIMESTAMP_REWOUND, got %v", err)
	}
	// 相等时间戳合法（不回退）。
	f4 := f1
	if _, err := gw.HandleFrame(f4); err != nil {
		t.Fatalf("equal timestamp must be accepted: %v", err)
	}
	t.Logf("[biz=timestamp-order][input=256@100ms,257@0(ok),256@50ms(reject),256@100ms(ok)] per-id monotonic rule deterministic")
}

func TestReloadAtomicSuccessAndFailure(t *testing.T) {
	gl := openDemo(t)
	gw := gl.gw
	b := cansim.NewBuilder(gw.DB())
	f, _ := b.Frame(256, nil, map[string]float64{"EngineSpeed": 800})
	f.Timestamp = t0
	if _, err := gw.HandleFrame(f); err != nil {
		t.Fatal(err)
	}

	// 非法新定义：整体拒绝，旧定义继续可用，旧状态保留。
	bad := "BO_ 1 NEW: 8 GW\n SG_ Broken : 0|99@1+ (1,0) [0,1] \"\" GW\n"
	err := gw.Reload(strings.NewReader(bad))
	if err == nil {
		t.Fatal("invalid reload must fail")
	}
	var el dbc.ErrorList
	if !errors.As(err, &el) {
		t.Fatalf("reload error must be ErrorList, got %T", err)
	}
	t.Logf("[biz=reload-fail][input=length=99 def] reload rejected with %d errors; old def stays: %v", len(el.Errors()), el.Errors()[0].Reason)
	if gw.DB().Names["ENGINE_DATA"] != 256 {
		t.Fatal("old definition must remain active after failed reload")
	}
	v, _ := gw.GetSignal(256, "EngineSpeed", t0)
	if v.Status != model.StatusOK || v.State.Value != 800 {
		t.Fatalf("old state must survive failed reload: %+v", v)
	}
	f2, _ := b.Frame(256, nil, map[string]float64{"EngineSpeed": 900})
	f2.Timestamp = t0.Add(time.Millisecond)
	if _, err := gw.HandleFrame(f2); err != nil {
		t.Fatalf("old definition must still decode after failed reload: %v", err)
	}

	// 合法新定义：原子生效；旧状态属于旧定义，按语义清空（不新旧混用）。
	newDef := `VERSION "v2"
BO_ 512 NEW_MSG: 4 GW
 SG_ NewSpeed : 0|16@1+ (0.5,0) [0,1000] "rpm" GW
`
	if err := gw.Reload(strings.NewReader(newDef)); err != nil {
		t.Fatalf("valid reload must succeed: %v", err)
	}
	if _, ok := gw.GetSignal(256, "EngineSpeed", t0); ok {
		t.Fatal("old message 256 must no longer exist after reload")
	}
	t.Logf("[biz=reload-ok][input=v2 only NEW_MSG] new definition active; old runtime state cleared atomically")
}

func TestConcurrentDecodeQueryReload(t *testing.T) {
	// 竞态检测：go test -race 下并发解码/查询/热加载，任何观察者只能看到整份旧或整份新定义。
	gl := openDemo(t)
	gw := gl.gw
	b := cansim.NewBuilder(gw.DB())

	const workers = 8
	const iterations = 300
	var wg sync.WaitGroup
	var failures int64

	frameSeq := func(i int64) canbus.Frame {
		f, _ := b.Frame(256, nil, map[string]float64{"EngineSpeed": float64(800 + int(i)%400)})
		f.Timestamp = t0.Add(time.Duration(i) * time.Millisecond)
		return f
	}
	var ts int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				seq := atomic.AddInt64(&ts, 1)
				if _, err := gw.HandleFrame(frameSeq(seq)); err != nil {
					// 回退帧（多 worker 同刻可能乱序）是确定的合法拒绝，不算失败。
					var de *canbus.DecodeError
					if !errors.As(err, &de) || de.Reason != canbus.ReasonTimestampRewound {
						atomic.AddInt64(&failures, 1)
					}
				}
				if v, ok := gw.GetSignal(256, "EngineSpeed", t0.Add(time.Duration(seq)*time.Millisecond+time.Second)); ok {
					if v.Status != model.StatusOK && v.Status != model.StatusTimeout && v.Status != model.StatusNeverReceived {
						atomic.AddInt64(&failures, 1)
					}
				}
				_ = gw.Snapshot(t0.Add(time.Second))
			}
		}(w)
	}

	// 重载线程：交替提交会失败的定义和与当前相同的合法定义。
	wg.Add(1)
	go func() {
		defer wg.Done()
		goodBytes, _ := os.ReadFile("../../testdata/demo.dbc")
		for i := 0; i < 20; i++ {
			if i%2 == 0 {
				if err := gw.Reload(strings.NewReader("BO_ 9 X: 1 GW\n SG_ Bad : 0|99@1+ (1,0) [0,1] \"\" GW\n")); err == nil {
					atomic.AddInt64(&failures, 1)
				}
			} else if err := gw.Reload(strings.NewReader(string(goodBytes))); err != nil {
				atomic.AddInt64(&failures, 1)
			}
		}
	}()

	wg.Wait()
	if atomic.LoadInt64(&failures) != 0 {
		t.Fatalf("concurrent workers reported %d unexpected failures", failures)
	}
	t.Logf("[biz=concurrent-race][input=8 decoders x300 + 20 reloads against RWMutex] no torn reads; failure count=0")
}
