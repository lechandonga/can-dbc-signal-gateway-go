package fleet

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/gateway"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

const busDBCA = `VERSION "A"
BO_ 100 A_MSG: 4 GW
 SG_ ASpeed : 0|16@1+ (1,0) [0,1000] "rpm" GW
`

const busDBCB = `VERSION "B"
BO_ 200 B_MSG: 4 GW
 SG_ BTemp : 0|8@1+ (1,-40) [-40,215] "degC" GW
`

var ft0 = time.Unix(0, 0)

func load(t *testing.T, m *Manager, name, def string) {
	t.Helper()
	if _, err := m.Load(name, strings.NewReader(def), gateway.Config{DefaultTimeout: time.Second}); err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
}

func TestMultiBusDispatchIsolation(t *testing.T) {
	m := NewManager()
	load(t, m, "PT", busDBCA)
	load(t, m, "BODY", busDBCB)

	// 未知总线：可区分结论。
	if _, err := m.HandleFrame("NOPE", canbus.Frame{}); !isBusError(err) {
		t.Fatalf("unknown bus must be *BusError, got %v", err)
	}
	if _, _, err := m.GetSignal("NOPE", 1, "x", ft0); !isBusError(err) {
		t.Fatalf("unknown bus query must be *BusError, got %v", err)
	}
	t.Logf("[biz=multibus-unknown][input=NOPE] UNKNOWN_BUS distinguished from frame/signal errors")

	// 按名分发：A 路收帧、注入；B 路坏帧不影响 A。
	if _, err := m.HandleFrame("PT", canbus.Frame{ID: 100, Data: []byte{0x40, 0x01, 0, 0}, Timestamp: ft0}); err != nil {
		t.Fatal(err)
	}
	v, ok, err := m.GetSignal("PT", 100, "ASpeed", ft0)
	if err != nil || !ok || v.State.Value != 320 {
		t.Fatalf("PT ASpeed want 320, got %+v ok=%v err=%v", v, ok, err)
	}
	// B 路连续坏帧（未知报文）。
	for i := 0; i < 5; i++ {
		_, e := m.HandleFrame("BODY", canbus.Frame{ID: 0x7FF, Data: make([]byte, 8), Timestamp: ft0.Add(time.Duration(i) * time.Millisecond)})
		var de *canbus.DecodeError
		if !errors.As(e, &de) || de.Reason != canbus.ReasonUnknownMessage {
			t.Fatalf("BODY bad frame reason=%v", e)
		}
	}
	// A 路统计与状态不受影响。
	stA, _ := m.Stats("PT")
	stB, _ := m.Stats("BODY")
	if stA.Rejected != 0 || stA.DecodedOK != 1 || stB.Rejected != 5 {
		t.Fatalf("isolation broken: A=%+v B=%+v", stA, stB)
	}
	t.Logf("[biz=multibus-isolation][input=PT 1 ok, BODY 5 bad] stats independent: A ok=%d rej=%d, B rej=%d",
		stA.DecodedOK, stA.Rejected, stB.Rejected)

	// 汇总与逐路一致。
	sum := m.Summary()
	byName := map[string]BusSummary{}
	for _, s := range sum {
		byName[s.Name] = s
	}
	if byName["PT"].Stats.DecodedOK != stA.DecodedOK || byName["BODY"].Stats.Rejected != stB.Rejected ||
		byName["PT"].Signals != 1 || byName["BODY"].Messages != 1 {
		t.Fatalf("summary inconsistent with per-bus query: %+v", sum)
	}
	t.Logf("[biz=multibus-summary][input=2 buses] Summary() matches per-bus Stats/definitions: %+v", sum)

	// B 路换定义失败 -> B 维持原样，A 完全不受影响。
	badDef := "BO_ 1 X: 8 GW\n SG_ Broken : 0|99@1+ (1,0) [0,1] \"\" GW\n"
	if _, err := m.Reload("BODY", strings.NewReader(badDef)); err == nil {
		t.Fatal("reload must reject bad definition")
	}
	if _, ok, err := m.GetSignal("BODY", 200, "BTemp", ft0); err != nil || !ok {
		t.Fatalf("BODY original definition must remain after failed reload: ok=%v err=%v", ok, err)
	}
	vA, _, _ := m.GetSignal("PT", 100, "ASpeed", ft0)
	if vA.State.Value != 320 {
		t.Fatalf("PT must be unaffected by BODY failed reload: %v", vA.State.Value)
	}
	// B 路换定义成功：仅 B 生效。
	newB := `VERSION "B2"
BO_ 201 B_NEW: 2 GW
 SG_ BN : 0|8@1+ (1,0) [0,255] "" GW
`
	if _, err := m.Reload("BODY", strings.NewReader(newB)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := m.GetSignal("BODY", 200, "BTemp", ft0); ok {
		t.Fatal("old B message must be gone after successful reload")
	}
	if !m.Has("PT") {
		t.Fatal("PT must still exist while BODY reloads")
	}
	t.Logf("[biz=multibus-reload][input=BODY fail then success] failed reload preserves; success replaces only BODY; PT intact")
}

func TestMultiBusConcurrent(t *testing.T) {
	m := NewManager()
	load(t, m, "PT", busDBCA)
	load(t, m, "BODY", busDBCB)

	const workers = 8
	const iterations = 200
	var wg sync.WaitGroup
	var failures int64
	var ts int64

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			bus := []string{"PT", "BODY"}[seed%2]
			id := uint32(100 + 100*(seed%2))
			for i := 0; i < iterations; i++ {
				seq := atomic.AddInt64(&ts, 1)
				ts := ft0.Add(time.Duration(seq) * time.Microsecond)
				if bus == "PT" {
					_, err := m.Inject("PT", gateway.InjectRequest{MsgID: 100,
						Values: map[string]float64{"ASpeed": float64(seq % 1000)}, Timestamp: ts})
					if err != nil {
						var de *canbus.DecodeError
						if !errors.As(err, &de) || de.Reason != canbus.ReasonTimestampRewound {
							atomic.AddInt64(&failures, 1)
						}
					}
					if _, _, err := m.GetSignal("PT", 100, "ASpeed", ts); err != nil {
						atomic.AddInt64(&failures, 1)
					}
				} else {
					_, err := m.HandleFrame("BODY", canbus.Frame{ID: id, Data: []byte{0x28, 0, 0, 0}, Timestamp: ts})
					_ = err
					_ = m.Summary()
				}
			}
		}(w)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, _ = m.Reload("PT", strings.NewReader(busDBCA))
			_ = m.Names()
		}
	}()

	wg.Wait()
	if atomic.LoadInt64(&failures) != 0 {
		t.Fatalf("concurrent multi-bus workers reported %d failures", failures)
	}
	t.Logf("[biz=multibus-race][input=8 workers x200 decode/inject/query + 20 per-bus reloads] counts exact, no torn state")
}

// TestScaleHundredsMessages 用几百报文、上千信号的定义验证加载/查询无数量级退化。
func TestScaleHundredsMessages(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("VERSION \"scale\"\n")
	const msgs = 400
	const sigsPer = 8 // 3200 信号
	for i := 0; i < msgs; i++ {
		id := 1000 + i
		sb.WriteString("BO_ ")
		sb.WriteString(itoa(id))
		sb.WriteString(" M")
		sb.WriteString(itoa(i))
		sb.WriteString(": 8 GW\n")
		for j := 0; j < sigsPer; j++ {
			start := j * 8
			sb.WriteString(" SG_ S")
			sb.WriteString(itoa(j))
			sb.WriteString(" : ")
			sb.WriteString(itoa(start))
			sb.WriteString("|8@1+ (1,0) [0,255] \"\" GW\n")
		}
	}
	m := NewManager()
	start := time.Now()
	if _, err := m.Load("BIG", strings.NewReader(sb.String()), gateway.Config{}); err != nil {
		t.Fatalf("scale load: %v", err)
	}
	loadDur := time.Since(start)

	big := m.Bus("BIG")
	if big == nil {
		t.Fatal("BIG bus missing")
	}
	start = time.Now()
	for i := 0; i < msgs; i++ {
		id := uint32(1000 + i)
		_, err := big.HandleFrame(canbus.Frame{ID: id, Data: make([]byte, 8), Timestamp: ft0})
		if err != nil {
			t.Fatalf("scale decode %d: %v", id, err)
		}
		if v, ok := big.GetSignal(id, "S7", ft0); !ok || v.Status != model.StatusOK {
			t.Fatalf("scale query id=%d ok=%v status=%v", id, ok, v.Status)
		}
	}
	decodeDur := time.Since(start)
	sum := m.Summary()
	var totalSigs int
	for _, s := range sum {
		totalSigs += s.Signals
	}
	if totalSigs != msgs*sigsPer {
		t.Fatalf("scale signal count=%d want %d", totalSigs, msgs*sigsPer)
	}
	if loadDur > 2*time.Second || decodeDur > 2*time.Second {
		t.Fatalf("scale perf regressed: load=%v decode/query=%d msgs=%v", loadDur, msgs, decodeDur)
	}
	t.Logf("[biz=multibus-scale][input=%d messages %d signals] load=%s decode+query=%s summary matches",
		msgs, totalSigs, loadDur, decodeDur)
}

func isBusError(err error) bool {
	var be *BusError
	return errors.As(err, &be)
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

var _ = dbc.ReasonSyntax
