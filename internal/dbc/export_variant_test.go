package dbc

import (
	"strings"
	"testing"
)

// TestCompactSpacingAccepted 覆盖真实导出工具常见的紧凑写法：
// 冒号/括号/引号周围不留空格、BO_ 行尾分号、无接收方、无空格的缩放与范围。
func TestCompactSpacingAccepted(t *testing.T) {
	compact := `VERSION""
NS_:
BS_:
BU_:GW
BO_ 2147484281 EXT_MSG:8 GW
 SG_ Speed:0|16@1+(0.25,0)[0,16383.75]"rpm"
 SG_ Temp:16|8@1-(1,-40)[-40,215]"degC"
 SG_ Gear M:24|2@1+(1,0)[0,3]""
 SG_ Door m0:32|8@1+(1,0)[0,100]"%"
VAL_ 633 Speed 0"idle" 1600"cruise";
VAL_ 633 Temp -1"cold" 90"warm";
`
	spaced := `VERSION ""

NS_ :

BS_ :
BU_: GW

BO_ 633 EXT_MSG: 8 GW
 SG_ Speed : 0|16@1+ (0.25,0) [0,16383.75] "rpm" GW
 SG_ Temp : 16|8@1- (1,-40) [-40,215] "degC" GW
 SG_ Gear M : 24|2@1+ (1,0) [0,3] "" GW
 SG_ Door m0 : 32|8@1+ (1,0) [0,100] "%" GW
VAL_ 633 Speed 0 "idle" 1600 "cruise";
VAL_ 633 Temp 255 "cold" 90 "warm";
`
	a, sumA, err := ParseDetailed(strings.NewReader(compact))
	if err != nil {
		t.Fatalf("compact export variant must load: %v", err)
	}
	b, _, err := ParseDetailed(strings.NewReader(spaced))
	if err != nil {
		t.Fatalf("spaced variant must load: %v", err)
	}

	// 2147484009 = 2^31 + 313：高位标记必须被识别为扩展帧，存储 ID 规范化为 313。
	msg := a.Messages[633]
	if msg == nil {
		t.Fatalf("extended id not canonicalized to 313: ids=%v", a.Messages)
	}
	if !msg.Extended {
		t.Fatalf("message 313 must be marked extended from high-bit DBC id")
	}
	if b.Messages[633] == nil || b.Messages[633].Extended {
		t.Fatalf("standard id 313 must stay standard; got=%+v", b.Messages[633])
	}
	t.Logf("[biz=extended-id][input=BO_ 2147484281 (2^31+633)] canonical id=633 extended=true; standard BO_ 633 extended=false")

	for _, name := range []string{"Speed", "Temp", "Gear", "Door"} {
		sa, _ := msg.SignalByName(name)
		sb, _ := b.Messages[633].SignalByName(name)
		if sa.StartBit != sb.StartBit || sa.Length != sb.Length || sa.Order != sb.Order ||
			sa.IsSigned != sb.IsSigned || sa.Factor != sb.Factor || sa.Offset != sb.Offset ||
			sa.Min != sb.Min || sa.Max != sb.Max || sa.Unit != sb.Unit {
			t.Fatalf("signal %s semantic mismatch between variants:\na=%+v\nb=%+v", name, sa, sb)
		}
	}
	if len(sumA.AdoptedMessages) != 1 || len(sumA.Rejected) != 0 {
		t.Fatalf("compact summary mismatch: %+v", sumA)
	}
	t.Logf("[biz=compact-spacing][input=no spaces around :()[]\" and no receivers] adopted=%d signals, parse result equals spaced variant",
		len(sumA.AdoptedMessages[0].SignalNames))

	// 负数标签：Temp 为有符号 8 位信号，-1 归一化为原始码 0xFF，与紧凑/松散两种写法等价。
	temp, _ := msg.SignalByName("Temp")
	if label, ok := LabelFor(temp, 0xFF); !ok || label != "cold" {
		t.Fatalf("signed negative label -1 must map to raw 0xFF, got %q/%v", label, ok)
	}
	tempB, _ := b.Messages[633].SignalByName("Temp")
	if label, ok := LabelFor(tempB, 0xFF); !ok || label != "cold" {
		t.Fatalf("spaced variant raw 255 label mismatch: %q/%v", label, ok)
	}
	if label, ok := LabelFor(temp, 90); !ok || label != "warm" {
		t.Fatalf("positive label mismatch: %q/%v", label, ok)
	}
	if _, ok := LabelFor(temp, 0); ok {
		t.Fatal("unlisted raw value must fall back to no label")
	}
	t.Logf("[biz=signed-value-table][input=VAL_ Temp -1 \"cold\"] normalized to 8-bit two's complement raw=0xFF; fallback empty preserved")
}

// TestNegativeLabelOnUnsignedRejected 负数标签只允许出现在有符号信号上；
// 无符号信号带负数条目必须明确拒绝并定位到行/报文/信号。
func TestNegativeLabelOnUnsignedRejected(t *testing.T) {
	content := `BO_ 100 M: 2 GW
 SG_ S : 0|8@1+ (1,0) [0,255] "" GW
VAL_ 100 S -1 "bad" 3 "ok";
`
	_, sum, err := ParseDetailed(strings.NewReader(content))
	if err == nil {
		t.Fatal("negative label on unsigned signal must be rejected")
	}
	var hit bool
	for _, e := range sum.Rejected {
		if e.Reason == ReasonSyntax && e.MsgID == 100 && e.SigName == "S" && e.Line == 3 {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("negative-label error not located to line=3 msg=100 sig=S: %+v", sum.Rejected)
	}
	t.Logf("[biz=signed-value-table-reject][input=VAL_ unsigned S -1] rejected SYNTAX at line=3 message=M signal=S: %s",
		sum.Rejected[0].Message)
}

// TestExtendedIDDanglingReference 扩展帧 ID 在 VAL_/CM_/SIG_VALTYPE_ 引用处
// 同样接受带高位标记或规范化 ID，两种写法等价。
func TestExtendedIDReferences(t *testing.T) {
	withFlag := `BO_ 2147483948 M: 2 GW
 SG_ S : 0|8@1+ (1,0) [0,255] "" GW
VAL_ 2147483948 S 1 "on";
CM_ SG_ 2147483948 S "flag-id reference";
SIG_VALTYPE_ 2147483948 S : 0;
`
	canonical := `BO_ 2147483948 M: 2 GW
 SG_ S : 0|8@1+ (1,0) [0,255] "" GW
VAL_ 300 S 1 "on";
CM_ SG_ 300 S "canonical id reference";
SIG_VALTYPE_ 300 S : 0;
`
	a, _, err := ParseDetailed(strings.NewReader(withFlag))
	if err != nil {
		t.Fatalf("flag-id references must resolve: %v", err)
	}
	b, _, err := ParseDetailed(strings.NewReader(canonical))
	if err != nil {
		t.Fatalf("canonical-id references must resolve: %v", err)
	}
	sa, _ := a.Messages[300].SignalByName("S")
	sb, _ := b.Messages[300].SignalByName("S")
	if l, _ := LabelFor(sa, 1); l != "on" {
		t.Fatalf("flag-id VAL_ not resolved: labels=%v", sa.ValueLabels)
	}
	if l, _ := LabelFor(sb, 1); l != "on" {
		t.Fatalf("canonical VAL_ not resolved: labels=%v", sb.ValueLabels)
	}
	t.Logf("[biz=extended-id-references][input=VAL_/CM_/SIG_VALTYPE_ with 2^31 flag vs canonical 300] both resolve to message id=300")
}

// TestMixedEndianSamePositionBoundary 混排字节序的同位边界：
// 仅相邻时采用；真实落在同一个线性位上时拒绝并定位。
func TestMixedEndianSamePositionBoundary(t *testing.T) {
	// Motorola 23|8 占线性位 23,22,21,20,31,30,29,28；Intel 16|8 占 16..23 -> 在 20..23 同位，拒绝。
	sameBit := `BO_ 1 M: 8 GW
 SG_ Moto : 23|8@0+ (1,0) [0,255] "" GW
 SG_ Inte : 16|8@1+ (1,0) [0,255] "" GW
`
	_, sum, err := ParseDetailed(strings.NewReader(sameBit))
	if err == nil {
		t.Fatal("mixed-endian signals sharing physical bits must be rejected")
	}
	var hit bool
	for _, e := range sum.Rejected {
		if e.Reason == ReasonBitOverlap && e.SigName == "Inte" {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("same-bit overlap not located to Inte: %+v", sum.Rejected)
	}
	t.Logf("[biz=mixed-endian-same-bit][input=Moto 23|8@0 vs Intel 16|8@1 share linear bits 20..23] BIT_OVERLAP at signal=Inte")

	// 相邻不重叠：Motorola 15|8 占 15,14,13,12,23,22,21,20；Intel 16|8 占 16..23 -> 20..23 也重叠，
	// 改用 Moto 7|8 占 7..0 与 Intel 8|8 占 8..15，仅边界相邻。
	adjacent := `BO_ 1 M: 2 GW
 SG_ Moto : 7|8@0+ (1,0) [0,255] "" GW
 SG_ Inte : 8|8@1+ (1,0) [0,255] "" GW
`
	db, _, err := ParseDetailed(strings.NewReader(adjacent))
	if err != nil {
		t.Fatalf("adjacent mixed-endian signals must load: %v", err)
	}
	if len(db.Messages[1].Signals) != 2 {
		t.Fatal("both adjacent signals must be adopted")
	}
	t.Logf("[biz=mixed-endian-adjacent][input=Moto 7|8@0 bits0..7 adjacent Intel 8|8@1 bits8..15] both adopted")
}
