package dbc

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// tlog 统一用例日志：业务域 / 输入标识 / 判定依据一目了然。
func tlog(t *testing.T, biz, input, expect string, args ...any) {
	t.Helper()
	t.Logf("[biz=%s][input=%s] %s", biz, input, fmtsprintf(expect, args...))
}

func fmtsprintf(s string, args ...any) string {
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

func TestParseRejects(t *testing.T) {
	// 每个用例：标识 -> 非法定义片段；期望原因与定位字段。
	cases := []struct {
		name    string
		id      string
		content string
		reason  Reason
		sig     string
	}{
		{
			"unknown statement is rejected, not skipped",
			"UNKNOWN_STATEMENT/FOOBAR",
			"VERSION \"x\"\nFOOBAR_ 1 2 3;\n",
			ReasonUnknownStatement, "",
		},
		{
			"malformed BO_ syntax",
			"SYNTAX/BO-line2",
			"BO_ abc NOT_A_NAME: 8 GW\n SG_ S : 0|8@1+ (1,0) [0,255] \"\" GW\n",
			ReasonSyntax, "",
		},
		{
			"orphan signal without BO_",
			"ORPHAN_SIGNAL/line1",
			" SG_ S : 0|8@1+ (1,0) [0,255] \"\" GW\n",
			ReasonOrphanSignal, "",
		},
		{
			"duplicate message id",
			"DUPLICATE_MESSAGE/id=256",
			"BO_ 256 A: 8 GW\n SG_ S : 0|8@1+ (1,0) [0,255] \"\" GW\nBO_ 256 B: 8 GW\n SG_ T : 0|8@1+ (1,0) [0,255] \"\" GW\n",
			ReasonDuplicateMessage, "",
		},
		{
			"duplicate signal in same message",
			"DUPLICATE_SIGNAL/ENGINE_DATA/Speed",
			"BO_ 256 ENGINE_DATA: 8 GW\n SG_ Speed : 0|8@1+ (1,0) [0,255] \"\" GW\n SG_ Speed : 8|8@1+ (1,0) [0,255] \"\" GW\n",
			ReasonDuplicateSignal, "Speed",
		},
		{
			"signal length out of 1..64",
			"BAD_LENGTH/ENGINE_DATA/Speed",
			"BO_ 256 ENGINE_DATA: 8 GW\n SG_ Speed : 0|65@1+ (1,0) [0,255] \"\" GW\n",
			ReasonBadLength, "Speed",
		},
		{
			"start bit outside frame",
			"BAD_START_BIT/ENGINE_DATA/Speed",
			"BO_ 256 ENGINE_DATA: 2 GW\n SG_ Speed : 24|8@1+ (1,0) [0,255] \"\" GW\n",
			ReasonBadStartBit, "Speed",
		},
		{
			"intel signal spanning beyond DLC",
			"SIGNAL_OUT_OF_FRAME/ENGINE_DATA/Speed",
			"BO_ 256 ENGINE_DATA: 2 GW\n SG_ Speed : 12|8@1+ (1,0) [0,255] \"\" GW\n",
			ReasonSignalOutOfFrame, "Speed",
		},
		{
			"diagonal motorola layout rejected",
			"BAD_START_BIT/M/S=8|16@0",
			"BO_ 256 M: 8 GW\n SG_ S : 8|16@0+ (1,0) [0,65535] \"\" GW\n",
			ReasonBadStartBit, "S",
		},
		{
			"bit overlap in same layout",
			"BIT_OVERLAP/ENGINE_DATA/T",
			"BO_ 256 ENGINE_DATA: 8 GW\n SG_ S : 0|16@1+ (1,0) [0,65535] \"\" GW\n SG_ T : 8|8@1+ (1,0) [0,255] \"\" GW\n",
			ReasonBitOverlap, "T",
		},
		{
			"non-positive factor",
			"BAD_SCALING/ENGINE_DATA/Speed",
			"BO_ 256 ENGINE_DATA: 8 GW\n SG_ Speed : 0|8@1+ (0,0) [0,255] \"\" GW\n",
			ReasonBadScaling, "Speed",
		},
		{
			"min greater than max",
			"BAD_SCALING/ENGINE_DATA/Speed",
			"BO_ 256 ENGINE_DATA: 8 GW\n SG_ Speed : 0|8@1+ (1,0) [9,1] \"\" GW\n",
			ReasonBadScaling, "Speed",
		},
		{
			"switched signal without multiplexor",
			"BAD_MULTIPLEX/MSG/S",
			"BO_ 256 MSG: 8 GW\n SG_ S m1 : 0|8@1+ (1,0) [0,255] \"\" GW\n",
			ReasonBadMultiplex, "S",
		},
		{
			"extended multiplexing mNM rejected",
			"BAD_MULTIPLEX/MSG/S",
			"BO_ 256 MSG: 8 GW\n SG_ M M : 0|4@1+ (1,0) [0,15] \"\" GW\n SG_ S m1M : 8|8@1+ (1,0) [0,255] \"\" GW\n",
			ReasonBadMultiplex, "S",
		},
		{
			"float sig valtype rejected",
			"BAD_FLOAT_TYPE/MSG/S",
			"BO_ 256 MSG: 8 GW\n SG_ S : 0|32@1+ (1,0) [0,0] \"\" GW\nSIG_VALTYPE_ 256 S : 1;\n",
			ReasonBadFloatType, "S",
		},
		{
			"VAL_ referencing unknown signal",
			"DANGLING_REFERENCE/256/Ghost",
			"BO_ 256 MSG: 8 GW\n SG_ S : 0|8@1+ (1,0) [0,255] \"\" GW\nVAL_ 256 Ghost 0 \"x\";\n",
			ReasonDanglingReference, "Ghost",
		},
		{
			"no messages at all",
			"INCOMPLETE/empty",
			"VERSION \"\"\n",
			ReasonIncomplete, "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.content))
			if err == nil {
				t.Fatalf("expected rejection reason=%s, but parse succeeded", tc.reason)
			}
			el, ok := err.(ErrorList)
			if !ok {
				t.Fatalf("expected ErrorList, got %T: %v", err, err)
			}
			var hit *DefError
			reasons := map[Reason]int{}
			for _, e := range el.Errors() {
				reasons[e.Reason]++
				if e.Reason == tc.reason && (tc.sig == "" || e.SigName == tc.sig) {
					hit = e
				}
			}
			if hit == nil {
				t.Fatalf("input=%s: reason %s(signal=%s) not found; got reasons=%v errors=%v",
					tc.id, tc.reason, tc.sig, reasons, el.Errors())
			}
			tlog(t, "malformed-definition", tc.id,
				"rejected with distinct reason=%s at line=%d msg=%q sig=%q: %s",
				string(hit.Reason), hit.Line, hit.MsgName, hit.SigName, hit.Message)
		})
	}
}

func TestParseErrorsCollectAllAndLocate(t *testing.T) {
	// 一个文件里多个问题必须全部收集，且错误可定位行号/报文/信号。
	content := "BO_ 256 ENGINE_DATA: 8 GW\n" +
		" SG_ Speed : 0|65@1+ (1,0) [0,255] \"\" GW\n" +
		" SG_ Temp : 0|8@1+ (1,0) [9,1] \"\" GW\n"
	_, err := Parse(strings.NewReader(content))
	el := err.(ErrorList)
	if len(el.Errors()) < 2 {
		t.Fatalf("expected >=2 collected errors, got %d", len(el.Errors()))
	}
	for _, e := range el.Errors() {
		if e.MsgName != "ENGINE_DATA" || e.Line == 0 {
			t.Fatalf("error not located to message/line: %+v", e)
		}
		tlog(t, "error-location", "ENGINE_DATA multi-error",
			"line=%d sig=%q reason=%s", e.Line, e.SigName, e.Reason)
	}
}

func TestParseDemoDBC(t *testing.T) {
	// 基线：随仓库提供的 testdata/demo.dbc 必须可解析（其它测试共用）。
	f, err := os.Open("../../testdata/demo.dbc")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	db, err := Parse(f)
	if err != nil {
		t.Fatalf("demo.dbc must parse: %v", err)
	}
	if len(db.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(db.Messages))
	}
	tlog(t, "valid-definition", "testdata/demo.dbc", "parsed messages=%d", len(db.Messages))
}
