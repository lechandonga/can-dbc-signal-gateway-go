package dbc

import (
	"strings"
	"testing"
)

func TestRealDBCVariantEquivalenceAndValues(t *testing.T) {
	compact := strings.NewReader(`VERSION "x"
NS_ :

BS_:
BU_: A B
VAL_TABLE_ Gear 0 "P" 1 "R" 2 "N" 3 "D";
BO_ 100 M: 2 A
 SG_ Sel M : 0|4@1+ (1,0) [0,3] "" B
 SG_ Gear m1 : 7|4@0+ (1,0) [0,3] "" B
BA_ "GenMsgCycleTime" BO_ 100 100;
VAL_ 100 Gear Gear;
`)
	spaced := strings.NewReader(`VERSION "x" ;

NS_ :

BS_ :
BU_ : A B

VAL_TABLE_ Gear 0 "P"
 1 "R"
 2 "N"
 3 "D" ;

BO_ 100 M: 2 A
 SG_ Sel M : 0|4@1+ (1,0) [0,3] "" B
 SG_ Gear m1 : 7|4@0+ (1,0) [0,3] "" B
CM_ SG_ 100 Gear "gear selector";
BA_ "GenMsgCycleTime" BO_ 100 100 ;
VAL_ 100 Gear 0 "P" 1 "R" 2 "N" 3 "D" ;
`)
	a, sumA, err := ParseDetailed(compact)
	if err != nil {
		t.Fatalf("compact variant rejected: %v", err)
	}
	b, sumB, err := ParseDetailed(spaced)
	if err != nil {
		t.Fatalf("spaced variant rejected: %v", err)
	}
	if len(a.Messages) != len(b.Messages) || len(a.Messages[100].Signals) != len(b.Messages[100].Signals) {
		t.Fatalf("message/signal shape mismatch: a=%+v b=%+v", a.Messages, b.Messages)
	}
	for _, name := range []string{"Sel", "Gear"} {
		sa, _ := a.Messages[100].SignalByName(name)
		sb, _ := b.Messages[100].SignalByName(name)
		if sa.StartBit != sb.StartBit || sa.Length != sb.Length || sa.Order != sb.Order ||
			sa.IsSigned != sb.IsSigned || sa.Factor != sb.Factor || sa.Offset != sb.Offset {
			t.Fatalf("signal %s semantic mismatch: %+v vs %+v", name, sa, sb)
		}
	}
	ga, _ := a.Messages[100].SignalByName("Gear")
	gb, _ := b.Messages[100].SignalByName("Gear")
	if label, ok := LabelFor(ga, 3); !ok || label != "D" {
		t.Fatalf("table label lookup=%q ok=%v", label, ok)
	}
	if label, ok := LabelFor(gb, 9); ok || label != "" {
		t.Fatalf("missing value must fall back to empty/false, got %q/%v", label, ok)
	}
	if len(sumA.AdoptedMessages) != 1 || len(sumA.AdoptedMessages[0].SignalNames) != 2 ||
		len(sumA.Ignored) == 0 || len(sumA.Rejected) != 0 {
		t.Fatalf("summary mismatch: %+v", sumA)
	}
	if len(sumB.AdoptedMessages) != 1 || len(sumB.Ignored) == 0 || len(sumB.Rejected) != 0 {
		t.Fatalf("spaced variant summary mismatch: %+v", sumB)
	}
	t.Logf("[biz=real-dbc-equivalence][input=compact/spaced VAL_TABLE variants] adopted=%d signals=%d ignored=%d rejected=%d label D=%q",
		len(sumA.AdoptedMessages), len(sumA.AdoptedMessages[0].SignalNames), len(sumA.Ignored), len(sumA.Rejected), ga.ValueLabels[3])
}

func TestMixedEndianAdjacentAndOverlap(t *testing.T) {
	adjacent := `BO_ 1 M: 2 GW
 SG_ A : 7|8@0+ (1,0) [0,255] "" GW
 SG_ B : 8|8@1+ (1,0) [0,255] "" GW
`
	db, _, err := ParseDetailed(strings.NewReader(adjacent))
	if err != nil {
		t.Fatalf("adjacent mixed-endian signals must load: %v", err)
	}
	if len(db.Messages[1].Signals) != 2 {
		t.Fatalf("both adjacent signals should be adopted")
	}
	overlap := `BO_ 1 M: 2 GW
 SG_ A : 7|9@0+ (1,0) [0,511] "" GW
 SG_ B : 8|8@1+ (1,0) [0,255] "" GW
`
	_, sum, err := ParseDetailed(strings.NewReader(overlap))
	if err == nil {
		t.Fatal("overlapping mixed-endian signals must be rejected")
	}
	var hit bool
	for _, e := range sum.Rejected {
		if e.Reason == ReasonBitOverlap && e.MsgName == "M" && e.SigName == "B" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("overlap not located to M/B: %+v", sum.Rejected)
	}
	t.Logf("[biz=mixed-endian-boundary][input=7|8@0 adjacent 8|8@1; 7|9@0 overlap] adjacent accepted, overlap rejected at message=M signal=B")
}
