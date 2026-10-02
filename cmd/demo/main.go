// 本地演示：从内置 DBC 定义出发，构造一条确定性帧序列（正常帧、复用帧、
// 丢帧、超时、截断帧、未知帧、时间戳回退帧），逐步解码并打印判定结果。
// 不依赖任何真实硬件或外部服务。
//
// 运行：go run ./cmd/demo
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/cansim"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/gateway"
)

const demoDBC = `
VERSION "demo subset for local replay"

NS_ :

BS_:

BU_: ECU_GW ECU_ENG ECU_BODY

BO_ 256 ENGINE_DATA: 8 ECU_ENG
 SG_ EngineSpeed : 0|16@1+ (0.25,0) [0,16383.75] "rpm" ECU_GW
 SG_ EngineTemp : 16|8@1+ (1,-40) [-40,215] "degC" ECU_GW

BO_ 257 BODY_STATUS: 8 ECU_BODY
 SG_ StatusType M : 0|4@1+ (1,0) [0,15] "" ECU_GW
 SG_ DoorOpenDeg m0 : 8|8@1+ (1,0) [0,180] "deg" ECU_GW
 SG_ GearEngaged m1 : 7|4@0+ (1,0) [0,15] "" ECU_GW
`

func main() {
	db, err := dbc.Parse(strings.NewReader(demoDBC))
	if err != nil {
		panic(err)
	}
	t0 := time.Unix(0, 0)
	gw := gateway.New(db, gateway.Config{
		DefaultTimeout: 200 * time.Millisecond,
		SignalTimeouts: map[string]time.Duration{"EngineSpeed": 500 * time.Millisecond},
	})
	builder := cansim.NewBuilder(db)
	seq := cansim.NewSequence(t0)

	// t=0ms：发动机帧，转速 800rpm、温度 90degC。
	f1, _ := builder.Frame(256, nil, map[string]float64{"EngineSpeed": 800, "EngineTemp": 90})
	seq.Add(0, f1.ID, f1.Data)
	// t=100ms：车身复用帧，选择值 0 -> 车门角度。
	f2, _ := builder.Frame(257, ptr(0), map[string]float64{"DoorOpenDeg": 45})
	seq.Add(100*time.Millisecond, f2.ID, f2.Data)
	// t=200ms：车身复用帧，选择值 1 -> 挡位（Motorola 跨字节）。
	f3, _ := builder.Frame(257, ptr(1), map[string]float64{"GearEngaged": 3})
	seq.Add(200*time.Millisecond, f3.ID, f3.Data)
	// t=250ms：非法选择值 9（无对应布局）。
	f4, _ := builder.Frame(257, ptr(9), nil)
	seq.Add(250*time.Millisecond, f4.ID, f4.Data)
	// t=260ms：截断帧（2 字节 < DLC 8）。
	seq.Add(260*time.Millisecond, cansim.Truncate(f1, 2).ID, cansim.Truncate(f1, 2).Data)
	// t=270ms：未知报文 0x7FF。
	seq.Add(270*time.Millisecond, 0x7FF, []byte{1, 2, 3, 4, 5, 6, 7, 8})
	// t=400ms：发动机帧（确立 msg256 的最新时间戳）。
	seq.Add(400*time.Millisecond, f1.ID, f1.Data)
	// t=150ms：时间戳回退帧。
	seq.Add(150*time.Millisecond, f1.ID, f1.Data)
	// t=600ms：下一帧发动机数据（EngineSpeed 仍在 500ms 窗口内；EngineTemp 已超时）。
	f5, _ := builder.Frame(256, nil, map[string]float64{"EngineSpeed": 1200, "EngineTemp": 92})
	seq.Add(600*time.Millisecond, f5.ID, f5.Data)

	for _, f := range seq.Replay() {
		res, derr := gw.HandleFrame(f)
		if derr != nil {
			fmt.Printf("[seq=%d t=%-6v] REJECT %s\n", f.Seq, f.Timestamp.Sub(t0), derr)
			continue
		}
		mux := "-"
		if res.MuxValue != nil {
			mux = fmt.Sprintf("%d", *res.MuxValue)
		}
		fmt.Printf("[seq=%d t=%-6v] OK %s mux=%s signals=%d\n", f.Seq, f.Timestamp.Sub(t0), res.MsgName, mux, len(res.Updated))
	}

	now := t0.Add(650 * time.Millisecond)
	fmt.Println("\n-- query @650ms --")
	for _, id := range []uint32{256, 257} {
		for _, sig := range db.Messages[id].Signals {
			view, ok := gw.GetSignal(id, sig.Name, now)
			if !ok {
				continue
			}
			fmt.Printf("%-14s %-8v raw=%-6d value=%-8g %-5q %s %s\n",
				db.Messages[id].Name, sig.Name, view.State.Raw, view.State.Value, view.State.Unit, view.Status, view.Reason)
		}
	}
	st := gw.Stats()
	fmt.Printf("\nstats: total=%d ok=%d rejected=%d reasons=%v\n", st.TotalFrames, st.DecodedOK, st.Rejected, st.RejectReasons)
	_ = canbus.Frame{}
}

func ptr(v int) *int { return &v }
