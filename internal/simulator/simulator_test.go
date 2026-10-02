package simulator

import (
	"strings"
	"testing"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
)

const simDBC = `
BO_ 100 M: 4 ECU
 SG_ IntelU : 0|16@1+ (0.5,10) [0|0] "u" GW
 SG_ MotoU  : 23|8@0+ (1,0) [0|255] "km/h" GW
 SG_ IntelS : 24|8@1- (1,-40) [0|0] "d" GW
`

func TestEncodeDecodeRoundTrip(t *testing.T) {
	db, err := dbc.Parse(strings.NewReader(simDBC), "sim")
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewBuilder(db).
		SetPhysical(100, "IntelU", 305). // raw (305-10)/0.5 = 590
		SetRaw(100, "MotoU", 0x5A).
		SetRaw(100, "IntelS", -5).
		Build(100, 0)
	if err != nil {
		t.Fatalf("SIM-RT-01 build: %v", err)
	}

	// raw 590 = 0x024E little-endian at bits 0..15.
	if f.Data[0] != 0x4E || f.Data[1] != 0x02 {
		t.Fatalf("SIM-RT-01 intel bytes % x want 4e 02", f.Data[:4])
	}
	// Motorola 8-bit in byte2 (saw 23 is byte2 MSB) raw 0x5A.
	if f.Data[2] != 0x5A {
		t.Fatalf("SIM-RT-02 motorola byte %02x want 5a", f.Data[2])
	}
	// Signed -5 in 8 bits = 0xFB at byte3.
	if f.Data[3] != 0xFB {
		t.Fatalf("SIM-RT-03 signed byte %02x want fb", f.Data[3])
	}
	t.Logf("business=simulator-encode case=SIM-RT-01 bytes=%x basis='physical->raw->bits'",
		f.Data)
}

func TestRejectsUnfittedValue(t *testing.T) {
	db, _ := dbc.Parse(strings.NewReader(simDBC), "sim")
	_, err := NewBuilder(db).SetRaw(100, "IntelU", 1<<20).Build(100, 0)
	if err == nil {
		t.Fatalf("SIM-FIT-01 expected build rejection for unfitted raw")
	}
	t.Logf("business=simulator-reject case=SIM-FIT-01 error=%v basis='raw exceeds signal width'", err)

	if _, err := NewBuilder(db).Build(999, 0); err == nil {
		t.Fatalf("SIM-FIT-02 expected unknown id rejection")
	}
}
