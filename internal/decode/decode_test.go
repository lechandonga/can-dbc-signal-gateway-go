package decode

import (
	"strings"
	"testing"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	simframedecode "github.com/lechandonga/can-dbc-signal-gateway-go/internal/simulator"
)

const dbcSrc = `
BO_ 256 Engine: 8 ECM
 SG_ EngineSpeed : 0|8@1+ (0.25,0) [0|63.75] "rpm" GW
 SG_ EngineTemp : 16|8@1- (1,-40) [-40|215] "degC" GW
BO_ 257 Body: 8 BCM
 SG_ VehicleSpeed : 7|16@0+ (0.01,0) [0|655.35] "km/h" GW
 SG_ GearLever : 23|4@0+ (1,0) [0|7] "" GW
BO_ 258 Mux: 8 ECM
 SG_ Sel M : 0|4@1+ (1,0) [0|2] "" GW
 SG_ Voltage : 8|8@1+ (0.1,0) [0|25.5] "V" GW
 SG_ OilPressure m0 : 16|8@1+ (1,0) [0|255] "kPa" GW
 SG_ FuelLevel m1 : 16|8@1+ (0.5,0) [0|127.5] "%" GW
 SG_ ReservedCode m2 : 16|8@1+ (1,0) [0|255] "" GW
VAL_ 258 Sel 0 "Oil" 1 "Fuel" 2 "Reserved" ;
`

func loadDB(t *testing.T) *dbc.Database {
	t.Helper()
	db, err := dbc.Parse(strings.NewReader(dbcSrc), "decode-test")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return db
}

func toFrame(f simframedecode.Frame) Frame {
	return Frame{ID: f.ID, Data: f.Data, Timestamp: f.Timestamp}
}

func find(res *FrameResult, name string) SignalValue {
	for _, v := range res.Signals {
		if v.Name == name {
			return v
		}
	}
	return SignalValue{Name: name + "#missing"}
}

func TestDecodeEndiannessAndSign(t *testing.T) {
	db := loadDB(t)
	b := simframedecode.NewBuilder(db).
		SetRaw(256, "EngineSpeed", 128).
		SetRaw(256, "EngineTemp", 90)
	f, err := b.Build(256, 1000)
	if err != nil {
		t.Fatalf("DEC-INTEL-01 build: %v", err)
	}
	if f.Data[0] != 0x80 || f.Data[2] != 0x5A {
		t.Fatalf("DEC-INTEL-01 bytes % x, want 80 .. 5a", f.Data[:4])
	}
	res, err := Decode(db, toFrame(f))
	if err != nil {
		t.Fatalf("DEC-INTEL-01 decode: %v", err)
	}
	speed := find(res, "EngineSpeed")
	if speed.Raw != 128 || speed.Physical != 32 || speed.Unit != "rpm" {
		t.Fatalf("DEC-INTEL-01 speed %+v", speed)
	}
	temp := find(res, "EngineTemp")
	// raw 90 signed, physical = 90 - 40 = 50 degC.
	if temp.Raw != 90 || temp.Physical != 50 {
		t.Fatalf("DEC-INTEL-02 temp %+v", temp)
	}
	t.Logf("business=decode-intel case=DEC-INTEL-01 input=id256@1000 raw=%d physical=%.2f %s",
		speed.Raw, speed.Physical, speed.Unit)

	b2 := simframedecode.NewBuilder(db).SetPhysical(256, "EngineTemp", -40)
	f2, _ := b2.Build(256, 1001)
	res2, err := Decode(db, toFrame(f2))
	if err != nil {
		t.Fatalf("DEC-INTEL-03: %v", err)
	}
	temp2 := find(res2, "EngineTemp")
	if temp2.Raw != 0 || temp2.Physical != -40 {
		t.Fatalf("DEC-INTEL-03 signed negative %+v", temp2)
	}
}

func TestDecodeMotorolaCrossByte(t *testing.T) {
	db := loadDB(t)
	// VehicleSpeed Motorola start 7 length 16, factor .01. 123.45 km/h -> 12345
	f, err := simframedecode.NewBuilder(db).SetPhysical(257, "VehicleSpeed", 123.45).
		Build(257, 2000)
	if err != nil {
		t.Fatalf("DEC-MOTO-01 build: %v", err)
	}
	res, err := Decode(db, toFrame(f))
	if err != nil {
		t.Fatalf("DEC-MOTO-01: %v", err)
	}
	v := find(res, "VehicleSpeed")
	if v.Raw != 12345 || v.Physical < 123.449 || v.Physical > 123.451 || v.Unit != "km/h" {
		t.Fatalf("DEC-MOTO-01 %+v", v)
	}
	t.Logf("business=decode-motorola case=DEC-MOTO-01 input=id257@2000 bytes=%x raw=%d physical=%.2f",
		f.Data[:3], v.Raw, v.Physical)

	// Hand-built known answer: raw 0x3039 (12345) big-endian across byte 0..1.
	f2 := Frame{ID: 257, Data: []byte{0x30, 0x39, 0, 0, 0, 0, 0, 0}, Timestamp: 2001}
	res2, err := Decode(db, f2)
	if err != nil {
		t.Fatalf("DEC-MOTO-02: %v", err)
	}
	if got := find(res2, "VehicleSpeed"); got.Raw != 12345 {
		t.Fatalf("DEC-MOTO-02 raw=%d want 12345", got.Raw)
	}
}

func TestDecodeMultiplexed(t *testing.T) {
	db := loadDB(t)
	f0, _ := simframedecode.NewBuilder(db).
		SetRaw(258, "Sel", 0).SetRaw(258, "Voltage", 120).
		SetRaw(258, "OilPressure", 77).Build(258, 3000)
	res, err := Decode(db, toFrame(f0))
	if err != nil {
		t.Fatalf("DEC-MUX-01: %v", err)
	}
	if res.Branch != 0 || find(res, "OilPressure").Raw != 77 {
		t.Fatalf("DEC-MUX-01 branch=%d signals=%+v", res.Branch, res.Signals)
	}
	if find(res, "FuelLevel").Name == "FuelLevel" {
		t.Fatalf("DEC-MUX-02 fuel signal must not decode in branch 0")
	}
	if v := find(res, "Voltage"); v.Raw != 120 || v.Unit != "V" {
		t.Fatalf("DEC-MUX-03 common signal %+v", v)
	}
	t.Logf("business=decode-mux case=DEC-MUX-01 input=id258 sel=0 decoded=OilPressure,Voltage")

	// Selector 3: not a legal enum value -> ReasonMuxIllegal.
	bad := Frame{ID: 258, Data: []byte{0x03, 0, 0, 0, 0, 0, 0, 0}, Timestamp: 3001}
	_, err = Decode(db, bad)
	expectReject(t, "DEC-MUX-04", err, ReasonMuxIllegal, 3)

	// Definition without enum: selector value with no layout -> ReasonMuxNoLayout.
	src := strings.ReplaceAll(dbcSrc,
		"VAL_ 258 Sel 0 \"Oil\" 1 \"Fuel\" 2 \"Reserved\" ;\n", "")
	dbNoEnum, perr := dbc.Parse(strings.NewReader(src), "no-enum")
	if perr != nil {
		t.Fatalf("setup no-enum: %v", perr)
	}
	_, err = Decode(dbNoEnum, Frame{ID: 258, Data: []byte{0x07, 0, 0, 0, 0, 0, 0, 0}, Timestamp: 3002})
	expectReject(t, "DEC-MUX-05", err, ReasonMuxNoLayout, 7)
}

func TestDecodeFrameRejections(t *testing.T) {
	db := loadDB(t)
	cases := []struct {
		id     string
		frame  Frame
		reason RejectReason
	}{
		{"DEC-FRAME-01", Frame{ID: 999, Data: make([]byte, 8)}, ReasonUnknownMessage},
		{"DEC-FRAME-02", Frame{ID: 256, Data: []byte{0, 0, 0}}, ReasonFrameTooShort},
		{"DEC-FRAME-03", Frame{ID: 256, Data: make([]byte, 7)}, ReasonFrameTooShort},
		{"DEC-FRAME-04", Frame{ID: 256, Data: make([]byte, 9)}, ReasonLengthMismatch},
	}
	for _, tc := range cases {
		_, err := Decode(db, tc.frame)
		expectReject(t, tc.id, err, tc.reason, 0)
	}
}

func expectReject(t *testing.T, caseID string, err error, reason RejectReason, sel int64) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s expected rejection %s", caseID, reason)
	}
	fe, ok := err.(*FrameError)
	if !ok {
		t.Fatalf("%s unexpected error type %T", caseID, err)
	}
	t.Logf("business=decode-reject case=%s input=id%d reason=%s selector=%d basis=%q",
		caseID, fe.MessageID, fe.Reason, fe.Value, fe.Detail)
	if fe.Reason != reason {
		t.Fatalf("%s reason got %s want %s", caseID, fe.Reason, reason)
	}
	if reason == ReasonMuxIllegal && fe.Value != sel {
		t.Fatalf("%s selector got %d want %d", caseID, fe.Value, sel)
	}
}
