// demo runs a fully local pipeline: parse testdata/vehicle.dbc, synthesize a
// deterministic frame sequence (including a gap, a truncated frame, an illegal
// selector, a timestamp reorder and a hot reload), and print the resulting
// vehicle states with their machine-readable status.
package main

import (
	"fmt"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/decode"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/gateway"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/simulator"
)

func main() {
	db, err := dbc.ParseFile("testdata/vehicle.dbc")
	if err != nil {
		panic(err)
	}
	gw := gateway.New(db, gateway.Config{})
	b := simulator.NewBuilder(db)

	// t=100: normal engine frame.
	f1, err := b.SetPhysical(256, "EngineSpeed", 800).
		SetRaw(256, "EngineTemp", 90).Build(256, 100)
	mustBuild(err)
	ingest(gw, toDecode(f1), "normal engine frame")

	// t=110: truncated frame must be rejected without touching state.
	ingest(gw, decode.Frame{ID: 256, Data: []byte{0xff, 0xff}, Timestamp: 110},
		"truncated engine frame")

	// t=120: multiplexed branch 0 (Oil).
	f3, _ := simulator.NewBuilder(db).SetRaw(258, "StatusType", 0).
		SetRaw(258, "CommonVoltage", 135).
		SetRaw(258, "OilPressure", 200).Build(258, 120)
	ingest(gw, toDecode(f3), "mux branch 0 (oil)")

	// t=130: branch 1 (Fuel); OilPressure becomes stale_branch.
	f4, _ := simulator.NewBuilder(db).SetRaw(258, "StatusType", 1).
		SetRaw(258, "CommonVoltage", 135).
		SetRaw(258, "FuelLevel", 150).Build(258, 130)
	ingest(gw, toDecode(f4), "mux branch 1 (fuel)")

	// t=125: out-of-order frame rejected.
	f5, _ := simulator.NewBuilder(db).SetRaw(258, "StatusType", 1).
		SetRaw(258, "FuelLevel", 1).Build(258, 125)
	ingest(gw, toDecode(f5), "late frame t=125 after t=130")

	fmt.Println("\nstate snapshot at t=140:")
	for _, st := range gw.All(140) {
		fmt.Printf("  id=%-4d %-16s raw=%-8d physical=%10.3f %-5q status=%s t=%d\n",
			st.MessageID, st.Signal, st.Raw, st.Physical, st.Unit, st.Status, st.UpdatedAt)
	}
}

func toDecode(f simulator.Frame) decode.Frame {
	return decode.Frame{ID: f.ID, Data: f.Data, Timestamp: f.Timestamp}
}

func ingest(gw *gateway.Gateway, f decode.Frame, label string) {
	res, pe := gw.ProcessFrame(f)
	if pe != nil {
		fmt.Printf("reject: %-28s id=%d t=%d reason=%s\n", label, f.ID, f.Timestamp, pe.ErrorReason())
		return
	}
	fmt.Printf("accept: %-28s id=%d t=%d signals=%d branch=%d\n",
		label, f.ID, f.Timestamp, len(res.Signals), res.Branch)
}

func mustBuild(err error) {
	if err != nil {
		panic(err)
	}
}
