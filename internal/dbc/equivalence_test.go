package dbc

import (
	"os"
	"testing"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/cansim"
)

// TestRealExportEquivalence：两种工具导出的同一语义（断行、空白、分号、
// VAL_TABLE_ 条目顺序、内联/具名表写法差异）必须解析出等价的解码结果。
func TestRealExportEquivalence(t *testing.T) {
	open := func(path string) *Result {
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		res, err := ParseDetailed(f)
		if err != nil {
			t.Fatalf("%s must parse: %v", path, err)
		}
		return res
	}
	a := open("../../testdata/real_tool_a.dbc")
	b := open("../../testdata/real_tool_b.dbc")

	if len(a.Database.Messages) != len(b.Database.Messages) {
		t.Fatalf("message count differs: %d vs %d", len(a.Database.Messages), len(b.Database.Messages))
	}
	for id, ma := range a.Database.Messages {
		mb := b.Database.Messages[id]
		if mb == nil || mb.Name != ma.Name || mb.DLC != ma.DLC || len(mb.Signals) != len(ma.Signals) {
			t.Fatalf("message %d not equivalent: %+v vs %+v", id, ma, mb)
		}
		for _, sa := range ma.Signals {
			sb, ok := mb.SignalByName(sa.Name)
			if !ok {
				t.Fatalf("signal %s missing in tool B export", sa.Name)
			}
			if sb.Order != sa.Order || sb.StartBit != sa.StartBit || sb.Length != sa.Length ||
				sb.IsSigned != sa.IsSigned || sb.Factor != sa.Factor || sb.Offset != sa.Offset ||
				sb.Min != sa.Min || sb.Max != sa.Max || (sb.MuxSwitch == nil) != (sa.MuxSwitch == nil) {
				t.Fatalf("signal %s definition not equivalent: %+v vs %+v", sa.Name, sa, sb)
			}
		}
	}

	// 同一帧字节下两份定义的解码必须一致。
	ba := cansim.NewBuilder(a.Database)
	bb := cansim.NewBuilder(b.Database)
	fa, _ := ba.Frame(100, nil, map[string]float64{"EngineSpeed": 800, "EngineTemp": 90, "RequestTorque": -12.5})
	fb, _ := bb.Frame(100, nil, map[string]float64{"EngineSpeed": 800, "EngineTemp": 90, "RequestTorque": -12.5})
	if string(fa.Data) != string(fb.Data) {
		t.Fatalf("encoded bytes differ: %x vs %x", fa.Data, fb.Data)
	}
	msgA := a.Database.Messages[100]
	msgB := b.Database.Messages[100]
	for _, sa := range msgA.Signals {
		sb, _ := msgB.SignalByName(sa.Name)
		if canbus.ExtractRaw(sa, fa.Data) != canbus.ExtractRaw(sb, fb.Data) {
			t.Fatalf("raw decode differs for %s", sa.Name)
		}
	}
	t.Logf("[biz=real-export-equivalence][input=real_tool_a.dbc vs real_tool_b.dbc] definitions and byte-level decode equivalent data=%x", fa.Data)

	// 数值表：具名表（A）与条目乱序的具名表（B）标签一致；内联表也工作。
	gearA, _ := a.Database.Messages[101].SignalByName("GearEngaged")
	gearB, _ := b.Database.Messages[101].SignalByName("GearEngaged")
	for _, raw := range []int64{0, 1, 2, 3, 255, 9} {
		la, oka := gearA.ValueTable.LookupLabel(raw)
		lb, okb := gearB.ValueTable.LookupLabel(raw)
		if oka != okb || (oka && la != lb) {
			t.Fatalf("label mismatch raw=%d: %q(%v) vs %q(%v)", raw, la, oka, lb, okb)
		}
		if raw == 3 && la != "D" {
			t.Fatalf("raw=3 want D, got %q", la)
		}
	}
	leverA, _ := a.Database.Messages[101].SignalByName("GearLeverPos")
	if l, ok := leverA.ValueTable.LookupLabel(1); !ok || l != "ON" {
		t.Fatalf("inline VAL_ labels broken: %q %v", l, ok)
	}
	t.Logf("[biz=value-table][input=GEAR_TBL named + inline VAL_] labels equal across exports; unlisted raw=9 falls back stably (ok=false)")

	// 摘要：两份导出采用的报文/信号集合一致，且 BA_/SIG_GROUP_ 等被明确记录为忽略。
	if len(a.Summary.Adopted) != len(b.Summary.Adopted) {
		t.Fatalf("adopted summary length differs")
	}
	if len(a.Summary.Ignored) == 0 {
		t.Fatalf("summary must explicitly list ignored non-decoding statements")
	}
	for _, ig := range a.Summary.Ignored {
		if ig.Why == "" || ig.Kind == "" {
			t.Fatalf("ignored item must explain why: %+v", ig)
		}
	}
	if len(a.Summary.Rejected) != 0 {
		t.Fatalf("valid export must have no rejected entries: %+v", a.Summary.Rejected)
	}
	t.Logf("[biz=import-summary][input=real_tool_a.dbc] adopted=%d msgs ignored=%d items rejected=0",
		len(a.Summary.Adopted), len(a.Summary.Ignored))
}

// TestMixedEndianAdjacentOK：Intel/Motorola 混排且只是紧挨着（不共位）必须加载并解码正确。
func TestMixedEndianAdjacentOK(t *testing.T) {
	dbc := `VERSION "mixed"
BO_ 300 MIX: 2 GW
 SG_ LowIntel : 0|8@1+ (1,0) [0,255] "" GW
 SG_ HighMoto : 15|8@0+ (1,0) [0,255] "" GW
`
	res, err := ParseDetailed(stringsReader(dbc))
	if err != nil {
		t.Fatalf("adjacent mixed-endian layout must load: %v", err)
	}
	msg := res.Database.Messages[300]
	intel, _ := msg.SignalByName("LowIntel")
	moto, _ := msg.SignalByName("HighMoto")
	data := []byte{0x11, 0x22}
	if got := canbus.ExtractRaw(intel, data); got != 0x11 {
		t.Fatalf("intel raw=%#x want 0x11", got)
	}
	if got := canbus.ExtractRaw(moto, data); got != 0x22 {
		t.Fatalf("motorola raw=%#x want 0x22 (data=%x)", got, data)
	}
	// Motorola start=15 占 byte1；Intel 占 byte0：线性位不相交。
	t.Logf("[biz=mixed-endian-adjacent][input=MIX LowIntel@0|8 + HighMoto@15|8] loads; decode 0x11/0x22 from %x", data)
}

// TestMixedEndianTrueOverlapRejected：两种字节序真正占同一位 -> BIT_OVERLAP，
// 定位到具体报文和信号。
func TestMixedEndianTrueOverlapRejected(t *testing.T) {
	dbc := `VERSION "mixed-bad"
BO_ 301 MIX_BAD: 2 GW
 SG_ IntelWide : 0|16@1+ (1,0) [0,65535] "" GW
 SG_ MotoNibble : 15|4@0+ (1,0) [0,15] "" GW
`
	_, err := ParseDetailed(stringsReader(dbc))
	if err == nil {
		t.Fatal("overlapping mixed-endian signals must be rejected")
	}
	var el ErrorList
	if !asErrorList(err, &el) {
		t.Fatalf("want ErrorList, got %T", err)
	}
	found := false
	for _, e := range el.Errors() {
		t.Logf("[biz=mixed-endian-overlap][input=MIX_BAD] line=%d reason=%s msg=%s sig=%s: %s",
			e.Line, e.Reason, e.MsgName, e.SigName, e.Message)
		if e.Reason == ReasonBitOverlap && e.MsgID == 301 && e.SigName == "MotoNibble" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected BIT_OVERLAP pinpointing MotoNibble in MIX_BAD, got %v", el)
	}
}
