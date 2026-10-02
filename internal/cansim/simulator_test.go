package cansim

import (
	"testing"
	"time"

	"strings"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
)

const simDBC = `VERSION "x"
BO_ 256 ENGINE_DATA: 4 GW
 SG_ Speed : 0|16@1+ (0.25,0) [0,1000] "rpm" GW
 SG_ Temp : 16|8@1+ (1,-40) [-40,215] "degC" GW
BO_ 257 BODY: 8 GW
 SG_ Sel M : 0|4@1+ (1,0) [0,15] "" GW
 SG_ Door m0 : 8|8@1+ (1,0) [0,180] "deg" GW
 SG_ Gear m1 : 7|4@0+ (1,0) [0,15] "" GW
`

// TestSequenceReplayDeterministic 验证本地帧序列可复现：重放时间戳/序号/数据完全确定。
func TestSequenceReplayDeterministic(t *testing.T) {
	db, err := dbc.Parse(strings.NewReader(simDBC))
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1700000000, 0)
	b := NewBuilder(db)
	f1, _ := b.Frame(256, nil, map[string]float64{"Speed": 800})
	seq := NewSequence(t0).
		Add(10*time.Millisecond, f1.ID, f1.Data).
		Add(20*time.Millisecond, Truncate(f1, 2).ID, Truncate(f1, 2).Data)
	first := seq.Replay()
	second := seq.Replay()
	if len(first) != 2 || first[0].Timestamp != t0.Add(10*time.Millisecond) || first[0].Seq != 1 {
		t.Fatalf("unexpected first frame: %+v", first[0])
	}
	for i := range first {
		if !frameEqual(first[i], second[i]) {
			t.Fatalf("replay nondeterministic at %d: %+v vs %+v", i, first[i], second[i])
		}
	}
	if len(first[1].Data) != 2 {
		t.Fatalf("truncated helper failed: %x", first[1].Data)
	}
	t.Logf("[biz=simulator][input=2-frame script] replay identical across runs, seq/timestamps deterministic")
}

func frameEqual(a, z canbus.Frame) bool {
	if a.ID != z.ID || a.Seq != z.Seq || !a.Timestamp.Equal(z.Timestamp) || len(a.Data) != len(z.Data) {
		return false
	}
	for i := range a.Data {
		if a.Data[i] != z.Data[i] {
			return false
		}
	}
	return true
}

// TestBuilderMuxFrame 验证构造器按选择值只填充对应布局。
func TestBuilderMuxFrame(t *testing.T) {
	db, _ := dbc.Parse(strings.NewReader(simDBC))
	b := NewBuilder(db)
	mux := 1
	f, err := b.Frame(257, &mux, map[string]float64{"Gear": 3})
	if err != nil {
		t.Fatal(err)
	}
	if f.Data[0]&0x0F != 1 {
		t.Fatalf("mux raw selector not encoded: %x", f.Data[0])
	}
	gear, _ := db.Messages[257].SignalByName("Gear")
	raw := canbus.ExtractRaw(gear, f.Data)
	if raw != 3 {
		t.Fatalf("gear raw=%d want 3, data=%x", raw, f.Data)
	}
	t.Logf("[biz=simulator-mux][input=mux=1 Gear=3] selector+layout encoded deterministically data=%x", f.Data)
}
