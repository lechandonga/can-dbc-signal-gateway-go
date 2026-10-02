package canbus

import (
	"testing"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// TestEncodeDecodeRoundTrip 覆盖 Intel/Motorola、有符号/无符号、跨字节信号的往返一致性。
func TestEncodeDecodeRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		sig  *model.Signal
		phys float64
	}{
		{"intel-unsigned-16", &model.Signal{Name: "a", Order: model.Intel, StartBit: 0, Length: 16, Factor: 0.25}, 800},
		{"intel-signed-16-neg", &model.Signal{Name: "b", Order: model.Intel, StartBit: 24, Length: 16, IsSigned: true, Factor: 0.1}, -12.5},
		{"intel-signed-8-pos", &model.Signal{Name: "c", Order: model.Intel, StartBit: 40, Length: 8, IsSigned: true, Factor: 1, Offset: -40}, 50},
		{"moto-crossbyte-16", &model.Signal{Name: "d", Order: model.Motorola, StartBit: 7, Length: 16, Factor: 1}, 0xABCD},
		{"moto-4", &model.Signal{Name: "e", Order: model.Motorola, StartBit: 7, Length: 4, Factor: 1}, 10},
		{"moto-signed-12-neg", &model.Signal{Name: "f", Order: model.Motorola, StartBit: 23, Length: 12, IsSigned: true, Factor: 0.5}, -300.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := make([]byte, 8)
			EncodePhysical(tc.sig, tc.phys, data)
			raw := ExtractRaw(tc.sig, data)
			got := Physical(tc.sig, raw)
			if got != tc.phys {
				t.Fatalf("round-trip mismatch: want %v got %v (data=%x raw=%#x)", tc.phys, got, data, raw)
			}
			t.Logf("[biz=roundtrip][input=%s phys=%v] data=%x raw=%#x -> %v", tc.name, tc.phys, data, raw, got)
		})
	}
}

func TestToSigned(t *testing.T) {
	if ToSigned(0xFF, 8) != -1 || ToSigned(0x80, 8) != -128 || ToSigned(0x7F, 8) != 127 {
		t.Fatal("8-bit two's complement wrong")
	}
}
