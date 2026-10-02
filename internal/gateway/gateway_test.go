package gateway

import (
	"strings"
	"testing"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/decode"
)

const dbcV1 = `
BO_ 256 Engine: 8 ECM
 SG_ EngineSpeed : 0|8@1+ (1,0) [0|255] "rpm" GW
 SG_ SlowCounter : 8|8@1+ (1,0) [0|255] "" GW
BO_ 258 Mux: 8 ECM
 SG_ Sel M : 0|4@1+ (1,0) [0|1] "" GW
 SG_ OilPressure m0 : 16|8@1+ (1,0) [0|255] "kPa" GW
 SG_ FuelLevel m1 : 16|8@1+ (1,0) [0|255] "%" GW
VAL_ 258 Sel 0 "Oil" 1 "Fuel" ;
`

const dbcV2 = dbcV1 + `
BO_ 257 Body: 8 BCM
 SG_ VehicleSpeed : 7|16@0+ (1,0) [0|65535] "km/h" GW
`

const dbcV2Bad = `
BO_ 256 Engine: 1 ECM
 SG_ EngineSpeed : 0|16@1+ (1,0) [0|0] "" GW
`

func newTestGateway(t *testing.T, src string) *Gateway {
	t.Helper()
	db, err := dbc.Parse(strings.NewReader(src), "test-v1")
	if err != nil {
		t.Fatalf("setup parse: %v", err)
	}
	return New(db, Config{
		DefaultTimeout: 100 * time.Millisecond,
		SignalTimeout: map[string]time.Duration{
			"Engine.SlowCounter": time.Second,
		},
	})
}

func TestTimeoutAndRecovery(t *testing.T) {
	g := newTestGateway(t, dbcV1)

	if s := g.Signal(256, "EngineSpeed", 0); s.Status != StatusNeverReceived {
		t.Fatalf("GW-INIT-01 expected never_received, got %s", s.Status)
	}
	t.Logf("business=state-lifecycle case=GW-INIT-01 signal=EngineSpeed status=never_received")

	if _, pe := g.ProcessFrame(decode.Frame{ID: 256, Data: []byte{10, 1, 0, 0, 0, 0, 0, 0}, Timestamp: 0}); pe != nil {
		t.Fatalf("GW-LIVE-01 ingest: %v", pe)
	}
	s := g.Signal(256, "EngineSpeed", 50)
	if s.Status != StatusOK || s.Raw != 10 || s.Physical != 10 || s.Unit != "rpm" {
		t.Fatalf("GW-LIVE-01 state %+v", s)
	}

	if s := g.Signal(256, "EngineSpeed", 100); s.Status != StatusOK {
		t.Fatalf("GW-LIVE-02 gap equal to timeout must stay ok, got %s", s.Status)
	}
	t.Logf("business=state-lifecycle case=GW-LIVE-02 t=100 status=ok basis='strict > timeout rule'")

	if s := g.Signal(256, "EngineSpeed", 101); s.Status != StatusTimeout {
		t.Fatalf("GW-TIME-01 expected timeout at t=101, got %s", s.Status)
	}
	t.Logf("business=state-lifecycle case=GW-TIME-01 t=101 status=timeout basis='101-0>100ms'")

	if s := g.Signal(256, "SlowCounter", 101); s.Status != StatusOK {
		t.Fatalf("GW-TIME-02 per-signal override failed: %s", s.Status)
	}

	if _, pe := g.ProcessFrame(decode.Frame{ID: 256, Data: []byte{42, 1, 0, 0, 0, 0, 0, 0}, Timestamp: 150}); pe != nil {
		t.Fatalf("GW-REC-01: %v", pe)
	}
	if s := g.Signal(256, "EngineSpeed", 150); s.Status != StatusOK || s.Raw != 42 {
		t.Fatalf("GW-REC-01 expected recovered raw=42, got %+v", s)
	}
	t.Logf("business=state-lifecycle case=GW-REC-01 signal=EngineSpeed status=ok raw=42")
}

func TestRejectedFramesDoNotPollute(t *testing.T) {
	g := newTestGateway(t, dbcV1)
	if _, pe := g.ProcessFrame(decode.Frame{ID: 256, Data: []byte{11, 2, 0, 0, 0, 0, 0, 0}, Timestamp: 10}); pe != nil {
		t.Fatal(pe)
	}

	cases := []struct {
		id    string
		frame decode.Frame
		want  string
	}{
		{"GW-POL-01", decode.Frame{ID: 404, Data: make([]byte, 8), Timestamp: 20}, "unknown_message"},
		{"GW-POL-02", decode.Frame{ID: 256, Data: []byte{9, 9}, Timestamp: 21}, "frame_too_short"},
		{"GW-POL-03", decode.Frame{ID: 256, Data: make([]byte, 9), Timestamp: 22}, "length_mismatch"},
		{"GW-POL-04", decode.Frame{ID: 258, Data: []byte{0x09, 0, 0, 0, 0, 0, 0, 0}, Timestamp: 23}, "mux_value_illegal"},
		{"GW-POL-05", decode.Frame{ID: 256, Data: []byte{99, 0, 0, 0, 0, 0, 0, 0}, Timestamp: 5}, "stale_timestamp"},
	}
	for _, tc := range cases {
		_, pe := g.ProcessFrame(tc.frame)
		if pe == nil {
			t.Fatalf("%s expected rejection", tc.id)
		}
		t.Logf("business=reject-no-pollution case=%s input=id%d@%d reason=%s",
			tc.id, tc.frame.ID, tc.frame.Timestamp, pe.ErrorReason())
		if pe.ErrorReason() != tc.want {
			t.Fatalf("%s reason got %s want %s", tc.id, pe.ErrorReason(), tc.want)
		}
		if s := g.Signal(256, "EngineSpeed", 30); s.Raw != 11 || s.UpdatedAt != 10 {
			t.Fatalf("%s state polluted: %+v", tc.id, s)
		}
	}
}

func TestMultiplexedStaleness(t *testing.T) {
	g := newTestGateway(t, dbcV1)
	if _, pe := g.ProcessFrame(decode.Frame{ID: 258, Data: []byte{0, 0, 77, 0, 0, 0, 0, 0}, Timestamp: 10}); pe != nil {
		t.Fatal(pe)
	}
	if s := g.Signal(258, "OilPressure", 20); s.Status != StatusOK || s.Raw != 77 || s.Branch != 0 {
		t.Fatalf("GW-MUX-01 %+v", s)
	}
	t.Logf("business=mux-validity case=GW-MUX-01 t=20 signal=OilPressure status=ok branch=0")

	if _, pe := g.ProcessFrame(decode.Frame{ID: 258, Data: []byte{1, 0, 55, 0, 0, 0, 0, 0}, Timestamp: 30}); pe != nil {
		t.Fatal(pe)
	}
	if s := g.Signal(258, "OilPressure", 40); s.Status != StatusStaleBranch || s.Raw != 77 {
		t.Fatalf("GW-MUX-02 expected stale_branch retaining raw 77, got %+v", s)
	}
	if s := g.Signal(258, "FuelLevel", 40); s.Status != StatusOK || s.Raw != 55 {
		t.Fatalf("GW-MUX-03 active branch signal %+v", s)
	}
	t.Logf("business=mux-validity case=GW-MUX-02 t=40 signal=OilPressure status=stale_branch retained_raw=77")

	if _, pe := g.ProcessFrame(decode.Frame{ID: 258, Data: []byte{0, 0, 78, 0, 0, 0, 0, 0}, Timestamp: 50}); pe != nil {
		t.Fatal(pe)
	}
	if s := g.Signal(258, "OilPressure", 60); s.Status != StatusOK || s.Raw != 78 {
		t.Fatalf("GW-MUX-04 expected recovered, got %+v", s)
	}
}

func TestTimestampOrderingRule(t *testing.T) {
	g := newTestGateway(t, dbcV1)
	feed := func(raw byte, ts int64, want string) {
		_, pe := g.ProcessFrame(decode.Frame{ID: 256, Data: []byte{raw, 0, 0, 0, 0, 0, 0, 0}, Timestamp: ts})
		got := ""
		if pe != nil {
			got = pe.ErrorReason()
		}
		if got != want {
			t.Fatalf("GW-ORD raw=%d@%d got %q want %q", raw, ts, got, want)
		}
	}
	feed(1, 100, "")
	feed(2, 100, "") // equal timestamps apply in call order
	if s := g.Signal(256, "EngineSpeed", 100); s.Raw != 2 {
		t.Fatalf("GW-ORD-01 raw=%d want 2", s.Raw)
	}
	feed(3, 99, "stale_timestamp")
	t.Logf("business=timestamp-order case=GW-ORD-02 input=id256@99 after=100 reason=stale_timestamp basis='per-message non-decreasing'")
	if s := g.Signal(256, "EngineSpeed", 100); s.Raw != 2 {
		t.Fatalf("GW-ORD-02 stale frame polluted state: raw=%d", s.Raw)
	}
	// Other message ids have independent clocks.
	feedOther := decode.Frame{ID: 258, Data: []byte{1, 0, 1, 0, 0, 0, 0, 0}, Timestamp: 50}
	if _, pe := g.ProcessFrame(feedOther); pe != nil {
		t.Fatalf("GW-ORD-03 other ids are independent: %v", pe)
	}
}
