package dbc

import (
	"strings"
	"testing"
)

// 紧凑导出（冒号/括号/引号周围不留空格）与宽松写法必须等价；
// 扩展帧 ID 带最高位标记；有符号信号数值表带负数条目。
func TestRealExportCompactExtendedIDNegativeLabels(t *testing.T) {
	compact := "VERSION\"x\"\n" +
		"NS_:\n\nBS_:\nBU_:GW BODY\n" +
		"BO_ 2147484953 BODY:8 BODY\n" +
		" SG_ Sel M:0|4@1+(1,0)[0,3]\"\"GW\n" +
		" SG_ Temp:8|8@1-(1,-40)[-40,215]\"degC\"GW\n" +
		" SG_ Gear m1:7|4@0+(1,0)[0,3]\"\"GW\n" +
		"VAL_ 2147484953 Temp -1\"ERR\" -40\"LO\" 0\"OK\"; \n"
	spaced := "VERSION  \"x\"   ;\n\nNS_ :\n\nBS_ :\nBU_ : GW BODY\n\n" +
		"BO_ 1305 BODY : 8 BODY\n" +
		" SG_  Sel  M : 0|4@1+ ( 1 , 0 ) [ 0 , 3 ] \"\" GW\n" +
		" SG_  Temp : 8|8@1- (1 , -40) [-40 , 215] \"degC\" GW\n" +
		" SG_  Gear m1 : 7|4@0+ (1, 0) [0, 3] \"\" GW\n" +
		"VAL_ 1305 Temp -1 \"ERR\" -40 \"LO\" 0 \"OK\" ;\n"

	a, sumA, err := ParseDetailed(strings.NewReader(compact))
	if err != nil {
		t.Fatalf("compact real export rejected: %v", err)
	}
	b, sumB, err := ParseDetailed(strings.NewReader(spaced))
	if err != nil {
		t.Fatalf("spaced real export rejected: %v", err)
	}

	ma, mb := a.Messages[1305], b.Messages[1305]
	if ma == nil || mb == nil {
		t.Fatalf("canonical 29-bit id 1305 missing: a=%v b=%v", a.Messages, b.Messages)
	}
	if !ma.Extended {
		t.Fatal("compact BO_ with high-bit flag must mark message Extended")
	}
	if mb.Extended {
		t.Fatal("plain id without high-bit flag must not be marked extended")
	}
	if ma.DLC != mb.DLC || ma.Name != mb.Name {
		t.Fatalf("message semantics differ: %+v vs %+v", ma, mb)
	}
	for _, name := range []string{"Sel", "Temp", "Gear"} {
		sa, _ := ma.SignalByName(name)
		sb, _ := mb.SignalByName(name)
		if sa.StartBit != sb.StartBit || sa.Length != sb.Length || sa.Order != sb.Order ||
			sa.IsSigned != sb.IsSigned || sa.Factor != sb.Factor || sa.Offset != sb.Offset ||
			sa.Min != sb.Min || sa.Max != sb.Max {
			t.Fatalf("signal %s semantics differ between variants:\n%+v\n%+v", name, sa, sb)
		}
	}

	ta, _ := ma.SignalByName("Temp")
	tb, _ := mb.SignalByName("Temp")
	// 有符号负数标签：raw 0xFF -> -1 "ERR"；raw 0xD8(216) -> -40 "LO"；raw 0 -> "OK"。
	for _, tc := range []struct {
		raw   uint64
		label string
	}{
		{0xFF, "ERR"},
		{216, "LO"},
		{0, "OK"},
	} {
		la, oka := LabelFor(ta, tc.raw)
		lb, okb := LabelFor(tb, tc.raw)
		if !oka || !okb || la != tc.label || lb != tc.label {
			t.Fatalf("negative label raw=%#x compact=%q/%v spaced=%q/%v want %q", tc.raw, la, oka, lb, okb, tc.label)
		}
	}
	// 未收录值回退为 ("", false)。
	if l, ok := LabelFor(ta, 1); ok || l != "" {
		t.Fatalf("missing value must fall back to empty/false, got %q/%v", l, ok)
	}
	if len(sumA.Rejected) != 0 || len(sumB.Rejected) != 0 {
		t.Fatalf("variants must be adopted without rejection: %v %v", sumA.Rejected, sumB.Rejected)
	}
	if len(sumA.AdoptedMessages) != 1 || len(sumA.AdoptedMessages[0].SignalNames) != 3 {
		t.Fatalf("summary mismatch: %+v", sumA.AdoptedMessages)
	}
	t.Logf("[biz=real-export-equiv][input=compact(BO_2147484953,-1 label) vs spaced(BO_1305)] canonical id=1305 extended=%v, 3 signals, signed labels {0xff:ERR 0xd8:LO 0:OK}", ma.Extended)
}

// 扩展帧高位标记 + 超 29 位的 ID 必须明确拒绝并定位行号。
func TestExtendedIDBoundsRejected(t *testing.T) {
	// 带高位标记但剩余部分超过 29 位：0xA0000000。
	bad := "BO_ 2684354560 M: 8 GW\n SG_ S : 0|8@1+ (1,0) [0,255] \"\" GW\n"
	_, sum, err := ParseDetailed(strings.NewReader(bad))
	if err == nil {
		t.Fatal("over-29-bit extended id must be rejected")
	}
	var hit bool
	for _, e := range sum.Rejected {
		if e.Reason == ReasonSyntax && e.Line == 1 {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("over-29-bit id error not located to line 1: %+v", sum.Rejected)
	}
	t.Logf("[biz=extended-id-bound][input=BO_2684354560] rejected at line 1 rather than silently decoded")
	_ = err
}

// VAL_TABLE_ 中带负数条目，被有符号信号引用时同样生效。
func TestValueTableNegativeEntries(t *testing.T) {
	content := "VAL_TABLE_ Err -1 \"FAULT\" 0 \"NORMAL\";\n" +
		"BO_ 100 M: 1 GW\n SG_ S : 0|8@1- (1,0) [-128,127] \"\" GW\nVAL_ 100 S Err;\n"
	db, _, err := ParseDetailed(strings.NewReader(content))
	if err != nil {
		t.Fatalf("negative value table entries must be accepted: %v", err)
	}
	sig, _ := db.Messages[100].SignalByName("S")
	if l, ok := LabelFor(sig, 0xFF); !ok || l != "FAULT" {
		t.Fatalf("negative table label lookup: %q %v", l, ok)
	}
	t.Logf("[biz=value-table-negative][input=VAL_TABLE_ Err -1 FAULT; signed signal ref] raw=0xff -> FAULT")
}
