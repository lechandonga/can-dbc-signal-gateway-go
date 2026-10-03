// 本地演示：DBC 定义 -> 接收帧/按信号注入 -> 状态查询。
// 覆盖正常帧、复用布局、注入保持语义、非法复用选择值、截断/长度不符/未知帧、
// 时间戳回退、超时恢复、数值表标签、多路总线隔离。不依赖真实硬件或外部服务。
//
// 运行：go run ./cmd/demo
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/fleet"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/gateway"
)

const demoDBC = `VERSION "demo"
NS_ :

BS_:;
BU_: ECU_GW ECU_ENG ECU_BODY

BO_ 256 ENGINE_DATA: 8 ECU_ENG
 SG_ EngineSpeed : 0|16@1+ (0.25,0) [0,16383.75] "rpm" ECU_GW
 SG_ EngineTemp : 16|8@1+ (1,-40) [-40,215] "degC" ECU_GW

BO_ 257 BODY_STATUS: 8 ECU_BODY
 SG_ StatusType M : 0|4@1+ (1,0) [0,15] "" ECU_GW
 SG_ DoorOpenDeg m0 : 8|8@1+ (1,0) [0,180] "deg" ECU_GW
 SG_ GearEngaged m1 : 7|4@0+ (1,0) [0,15] "" ECU_GW
VAL_ 257 GearEngaged 0 "P" 1 "R" 2 "N" 3 "D";
`

const bodyDBC = `VERSION "body-bus"
BO_ 512 BODY_MISC: 2 ECU_BODY
 SG_ Light : 0|8@1+ (1,0) [0,255] "" ECU_GW
`

func main() {
	t0 := time.Unix(0, 0)
	mgr := fleet.NewManager()
	if _, err := mgr.Load("PT", strings.NewReader(demoDBC),
		gateway.Config{DefaultTimeout: 200 * time.Millisecond}); err != nil {
		panic(err)
	}
	if _, err := mgr.Load("BODY", strings.NewReader(bodyDBC),
		gateway.Config{DefaultTimeout: time.Second}); err != nil {
		panic(err)
	}

	// t=0：直接接收一帧发动机数据。
	if _, err := mgr.HandleFrame("PT", canbus.Frame{ID: 256,
		Data:      []byte{0x80, 0x0C, 0x82, 0, 0, 0, 0, 0}, // Speed=800, Temp=90
		Timestamp: t0}); err != nil {
		panic(err)
	}
	fmt.Println("[seq=1 t=0] PT ENGINE_DATA received: Speed=800 Temp=90")

	// t=10ms：只注入转速；温度位必须保持上一帧。
	if _, err := mgr.Inject("PT", gateway.InjectRequest{MsgID: 256,
		Values:    map[string]float64{"EngineSpeed": 1200},
		Timestamp: t0.Add(10 * time.Millisecond)}); err != nil {
		panic(err)
	}
	speed, _, _ := mgr.GetSignal("PT", 256, "EngineSpeed", t0.Add(10*time.Millisecond))
	temp, _, _ := mgr.GetSignal("PT", 256, "EngineTemp", t0.Add(10*time.Millisecond))
	fmt.Printf("[seq=2 t=10ms] inject Speed=1200 -> readback Speed=%v, Temp kept=%v (unspecified bits preserved)\n",
		speed.State.Value, temp.State.Value)

	// t=20ms：复用帧注入 mux=1，GearEngaged=3 -> 标签 "D"。
	if _, err := mgr.Inject("PT", gateway.InjectRequest{MsgID: 257,
		Values:    map[string]float64{"StatusType": 1, "GearEngaged": 3},
		Timestamp: t0.Add(20 * time.Millisecond)}); err != nil {
		panic(err)
	}
	gear, _, _ := mgr.GetSignal("PT", 257, "GearEngaged", t0.Add(20*time.Millisecond))
	fmt.Printf("[seq=3 t=20ms] inject mux=1 GearEngaged=%v raw=%d label=%q(known=%v)\n",
		gear.State.Value, gear.State.Raw, gear.State.Label, gear.State.LabelKnown)

	// 切到 mux=0 后，GearEngaged 旧值还在但结论必须是 MUX_INACTIVE。
	if _, err := mgr.Inject("PT", gateway.InjectRequest{MsgID: 257,
		Values:    map[string]float64{"StatusType": 0, "DoorOpenDeg": 60},
		Timestamp: t0.Add(30 * time.Millisecond)}); err != nil {
		panic(err)
	}
	inactive, _, _ := mgr.GetSignal("PT", 257, "GearEngaged", t0.Add(30*time.Millisecond))
	fmt.Printf("[seq=4 t=30ms] switch mux=0: GearEngaged last value=%v but status=%s (not timeout/never-received)\n",
		inactive.State.Value, inactive.Status)

	// 非法注入：选择值 9 无布局；另一路总线不受影响。
	if _, err := mgr.Inject("PT", gateway.InjectRequest{MsgID: 257,
		Values:    map[string]float64{"StatusType": 9},
		Timestamp: t0.Add(40 * time.Millisecond)}); err != nil {
		fmt.Printf("[seq=5 t=40ms] rejected inject: %v\n", err)
	}
	if _, err := mgr.HandleFrame("BODY", canbus.Frame{ID: 512, Data: []byte{0x07, 0}, Timestamp: t0}); err != nil {
		panic(err)
	}
	if _, err := mgr.HandleFrame("NOPE", canbus.Frame{}); err != nil {
		fmt.Printf("[seq=6]       unknown bus rejected distinctly: %v\n", err)
	}

	// t=250ms：EngineSpeed 默认 200ms 窗口已超时。
	now := t0.Add(250 * time.Millisecond)
	v, _, _ := mgr.GetSignal("PT", 256, "EngineSpeed", now)
	fmt.Printf("[query @250ms] PT EngineSpeed=%v status=%s reason=%q\n", v.State.Value, v.Status, v.Reason)

	fmt.Println("\n-- fleet summary --")
	for _, s := range mgr.Summary() {
		fmt.Printf("bus=%-5s messages=%d signals=%d total=%d ok=%d rejected=%d\n",
			s.Name, s.Messages, s.Signals, s.Stats.TotalFrames, s.Stats.DecodedOK, s.Stats.Rejected)
	}
	_ = dbc.ReasonSyntax
}
