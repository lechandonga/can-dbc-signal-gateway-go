package dbc

import (
	"strings"
	"testing"
)

const validMini = `
BO_ 100 Basic: 4 ECU
 SG_ Counter : 16|8@1+ (1,0) [0|255] "" GW
 SG_ Big : 7|16@0- (0.1,-5) [-5|60] "deg" GW
`

func TestParseHappy(t *testing.T) {
	db, err := Parse(strings.NewReader(validMini), "mini")
	if err != nil {
		t.Fatalf("HAPPY-01 expected parse success: %v", err)
	}
	m := db.Messages[100]
	if m == nil || m.DLC != 4 || m.Name != "Basic" {
		t.Fatalf("HAPPY-01 message structure mismatch: %+v", m)
	}
	big := m.Signal("Big")
	bits, perr := SignalBitPositions(big)
	if perr != nil {
		t.Fatalf("HAPPY-02 bit positions: %v", perr)
	}
	// Motorola start 7, 16 bits, MSB-first: linear 0..7 then 15..8.
	want := []int{7, 6, 5, 4, 3, 2, 1, 0, 15, 14, 13, 12, 11, 10, 9, 8}
	if len(bits) != len(want) {
		t.Fatalf("HAPPY-02 got %v want %v", bits, want)
	}
	for i := range want {
		if bits[i] != want[i] {
			t.Fatalf("HAPPY-02 bit %d got %d want %d (full %v)", i, bits[i], want[i], bits)
		}
	}
}

type rejectCase struct {
	id      string
	src     string
	reason  Reason
	message string
	signal  string
}

func TestRejectMalformedDefinitions(t *testing.T) {
	cases := []rejectCase{
		{
			id: "DBC-SYN-01", reason: ReasonSyntax,
			src: "FOOBAR_ 1 x;\n",
		},
		{
			id: "DBC-SYN-02", reason: ReasonSyntax,
			src: "BO_ 100 NoColon 4 ECU\n",
		},
		{
			id: "DBC-SYN-03", reason: ReasonSyntax, message: "M", signal: "Bad",
			src: "BO_ 100 M: 4 ECU\n SG_ Bad : 0|8@2+ (1,0) [0|0] \"\" GW\n",
		},
		{
			id: "DBC-INC-01", reason: ReasonIncomplete,
			src: " SG_ Orphan : 0|8@1+ (1,0) [0|0] \"\" GW\n",
		},
		{
			id: "DBC-INC-02", reason: ReasonIncomplete,
			src: "BO_ 100 M: 4 ECU\n SG_ A : 0|8@1+ (1,0) [0|0] \"\" GW\n" +
				"VAL_ 101 A 0 \"x\" ;\n",
		},
		{
			id: "DBC-DUP-01", reason: ReasonDuplicate, message: "Second",
			src: "BO_ 100 First: 4 ECU\nBO_ 100 Second: 4 ECU\n",
		},
		{
			id: "DBC-DUP-02", reason: ReasonDuplicate, message: "M", signal: "A",
			src: "BO_ 100 M: 4 ECU\n SG_ A : 0|8@1+ (1,0) [0|0] \"\" GW\n" +
				" SG_ A : 8|8@1+ (1,0) [0|0] \"\" GW\n",
		},
		{
			id: "DBC-RANGE-01", reason: ReasonBitOutOfRange, message: "M", signal: "Wide",
			src: "BO_ 100 M: 1 ECU\n SG_ Wide : 0|16@1+ (1,0) [0|0] \"\" GW\n",
		},
		{
			id: "DBC-RANGE-02", reason: ReasonInvalidSignal, message: "M", signal: "Zero",
			src: "BO_ 100 M: 4 ECU\n SG_ Zero : 0|8@1+ (0,0) [0|0] \"\" GW\n",
		},
		{
			id: "DBC-RANGE-03", reason: ReasonInvalidSignal, message: "M", signal: "Rev",
			src: "BO_ 100 M: 4 ECU\n SG_ Rev : 0|8@1+ (1,0) [9|8] \"\" GW\n",
		},
		{
			id: "DBC-OVL-01", reason: ReasonBitOverlap, message: "M", signal: "B",
			src: "BO_ 100 M: 4 ECU\n SG_ A : 0|16@1+ (1,0) [0|0] \"\" GW\n" +
				" SG_ B : 8|8@1+ (1,0) [0|0] \"\" GW\n",
		},
		{
			id: "DBC-MUX-01", reason: ReasonMuxDangling, signal: "Branch",
			src: "BO_ 100 M: 4 ECU\n SG_ Branch m0 : 0|8@1+ (1,0) [0|0] \"\" GW\n",
		},
		{
			id: "DBC-MUX-02", reason: ReasonMuxConflict, message: "M", signal: "Sw2",
			src: "BO_ 100 M: 4 ECU\n SG_ Sw1 M : 0|4@1+ (1,0) [0|1] \"\" GW\n" +
				" SG_ Sw2 M : 4|4@1+ (1,0) [0|1] \"\" GW\n",
		},
		{
			id: "DBC-MUX-03", reason: ReasonMuxDangling, message: "M", signal: "Sw",
			src: "BO_ 100 M: 4 ECU\n SG_ Sw M : 0|4@1+ (1,0) [0|3] \"\" GW\n" +
				" SG_ A m1 : 8|8@1+ (1,0) [0|0] \"\" GW\n" +
				"VAL_ 100 Sw 0 \"A\" 1 \"B\" ;\n",
		},
		{
			id: "DBC-UNSUP-01", reason: ReasonUnsupported, signal: "F",
			src: "BO_ 100 M: 8 ECU\n SG_ F : 0|32@1+ (1,0) [0|0] \"\" GW\n" +
				"SIG_VALTYPE_ 100 F : 1;\n",
		},
		{
			id: "DBC-UNSUP-02", reason: ReasonUnsupported,
			src: "SG_MUL_VAL_ 100 Sw A 0-5;\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			_, err := Parse(strings.NewReader(tc.src), tc.id)
			if err == nil {
				t.Fatalf("%s expected rejection reason %s, but parse succeeded", tc.id, tc.reason)
			}
			de, ok := err.(*Error)
			if !ok {
				t.Fatalf("%s expected *dbc.Error, got %T: %v", tc.id, err, err)
			}
			t.Logf("business=definition-reject case=%s input=%q reason=%s location=%s/%s detail=%q",
				tc.id, oneLine(tc.src), de.Reason, de.Message, de.Signal, de.Detail)
			if de.Reason != tc.reason {
				t.Fatalf("%s reason got %s want %s", tc.id, de.Reason, tc.reason)
			}
			if tc.message != "" && de.Message != tc.message {
				t.Fatalf("%s message location got %q want %q", tc.id, de.Message, tc.message)
			}
			if tc.signal != "" && de.Signal != tc.signal {
				t.Fatalf("%s signal location got %q want %q", tc.id, de.Signal, tc.signal)
			}
		})
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", "\\n")
	if len(s) > 60 {
		return s[:57] + "..."
	}
	return s
}
