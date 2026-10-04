package canbus

import (
	"testing"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// 混排字节序在字节边界两侧：Intel 低字节 + Motorola 高字节，
// 同一信号先大后小编码必须逐位清掉旧值，且相邻异序信号互不踩位。
func TestMixedEndianBoundaryOverwriteIsolation(t *testing.T) {
	intel := &model.Signal{Name: "i", Order: model.Intel, StartBit: 0, Length: 8, Factor: 1}
	moto := &model.Signal{Name: "m", Order: model.Motorola, StartBit: 15, Length: 8, Factor: 1}
	data := make([]byte, 2)

	EncodePhysical(intel, 0xA5, data)
	EncodePhysical(moto, 0x3C, data)
	if data[0] != 0xA5 || data[1] != 0x3C {
		t.Fatalf("mixed-endian adjacent encode bytes=% x, want a5 3c", data)
	}
	// 大值 -> 小值：两边都不得残留旧位。
	EncodePhysical(intel, 0x01, data)
	EncodePhysical(moto, 0x02, data)
	if data[0] != 0x01 || data[1] != 0x02 {
		t.Fatalf("overwrite left stale bits across endian boundary: % x, want 01 02", data)
	}
	if ExtractRaw(intel, data) != 1 || ExtractRaw(moto, data) != 2 {
		t.Fatalf("extract after overwrite: intel=%d moto=%d", ExtractRaw(intel, data), ExtractRaw(moto, data))
	}
	t.Logf("[biz=mixed-endian-bits][input=Intel@0 + Motorola@15 write a5/3c then 1/2] bytes=01 02, no stale bits, endians isolated")

	// 跨字节 Motorola 与紧邻 Intel 信号同样完整覆盖。
	moto16 := &model.Signal{Name: "m16", Order: model.Motorola, StartBit: 7, Length: 16, Factor: 1}
	data2 := make([]byte, 4)
	EncodePhysical(moto16, 0xFFFF, data2)
	EncodePhysical(moto16, 0x0001, data2)
	if data2[0] != 0x00 || data2[1] != 0x01 {
		t.Fatalf("motorola16 overwrite left stale bits: % x, want 00 01", data2[:2])
	}
	if ExtractRaw(moto16, data2) != 1 {
		t.Fatalf("motorola16 extract=%#x want 1", ExtractRaw(moto16, data2))
	}
	t.Logf("[biz=motorola-crossbyte-bits][input=start=7 len=16 write ffff then 0001] full overwrite raw=1")
}

// 有符号信号在同位边界连续编码时负数与正数互不残留。
func TestSignedOverwriteBoundary(t *testing.T) {
	sig := &model.Signal{Name: "s", Order: model.Intel, StartBit: 24, Length: 16, IsSigned: true, Factor: 0.1}
	data := make([]byte, 8)
	for _, v := range []float64{-100, 100, -0.1, 0, -100} {
		EncodePhysical(sig, v, data)
		got := Physical(sig, ExtractRaw(sig, data))
		if abs(got-v) > 0.05 {
			t.Fatalf("signed overwrite v=%v got=%v bytes=%x", v, got, data[3:6])
		}
	}
	t.Logf("[biz=signed-overwrite-bits][input=signed16@24 -100->100->-0.1->0->-100] each round-trip within half quantum")
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
