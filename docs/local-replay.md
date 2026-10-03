# 本地复现指南

所有验证都使用本地 DBC、本地构造帧和调用方显式时间戳，不接真实硬件或外部服务。

## 运行

```bash
go run ./cmd/demo
go test ./...
CGO_ENABLED=1 go test -race ./...
CGO_ENABLED=1 go test -race ./... -v
```

测试日志统一形如：

```text
[biz=<业务域>][input=<输入标识>] <判定依据与实际值>
```

业务域覆盖真实 DBC 等价写法、混排字节序边界、数值表与回退、导入摘要、
解码、注入往返与保持、注入拒绝分类、复用失效/不激活、多路隔离、并发和规模定义。

## 造一份 DBC

最小定义：

```dbc
VERSION "local"

NS_ :

BS_:
BU_: GW SENSOR

BO_ 100 SENSOR_DATA: 8 SENSOR
 SG_ Speed : 0|16@1+ (0.25,0) [0,1000] "rpm" GW
 SG_ Mode M : 16|2@1+ (1,0) [0,2] "" GW
 SG_ Door m0 : 24|8@1+ (1,0) [0,100] "%" GW
 SG_ Gear m1 : 31|4@0+ (1,0) [0,3] "" GW

VAL_TABLE_ GearTable 0 "P" 1 "R" 2 "N" 3 "D";
VAL_ 100 Gear GearTable;
```

导入并检查摘要：

```go
db, summary, err := dbc.ParseDetailed(file)
// summary.AdoptedMessages / Ignored / Rejected
```

## 造帧和注入

脚本造帧：

```go
b := cansim.NewBuilder(db)
frame, _ := b.Frame(100, ptr(1), map[string]float64{"Gear": 3})
frame.Timestamp = t0
res, err := gw.HandleFrame(frame)
```

按信号注入：

```go
res, err := gw.Inject(gateway.Injection{
    MessageID: 100,
    Values:    map[string]float64{"Mode": 1, "Gear": 3},
    Timestamp: t0,
})
view, ok := gw.GetSignal(100, "Gear", t0)
// view.State.Raw、view.State.Value、view.State.Label / HasLabel
```

再次只注入 `Mode=0, Door=50` 时，`Gear` 查询状态会变成 `INACTIVE`，不是 `TIMEOUT`。

## 多路总线

```go
registry := gateway.NewBusRegistry()
registry.AddBus("powertrain", dbA, gateway.Config{DefaultTimeout: 100 * time.Millisecond})
registry.AddBus("body", dbB, gateway.Config{DefaultTimeout: 200 * time.Millisecond})
bus, _ := registry.Get("powertrain")
bus.Inject(...)
all := registry.SnapshotAll(now)
```

未知总线名通过 `MustGet` 返回 `UNKNOWN_BUS`；单路场景仍可直接使用 `gateway.New(db, cfg)`，
不需要经过 `BusRegistry`。
