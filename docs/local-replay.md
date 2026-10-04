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

与本次修复直接对应的用例：

- `TestInjectReadbackLargeThenSmall`：先 800 后 100（以及无符号/Motorola/有符号缩放信号）
  注入后立即查询，必须在半量化步长内一致。
- `TestInjectSingleSignalDoesNotClearOthers`：只写单个信号时同报文其它信号逐位保持。
- `TestInjectMuxOrdinaryOnlyWithoutSelector`：复用报文只写普通信号、不给选择值也能构造。
- `TestInjectMuxPreservesOtherBranchRawBits`：选择值不变只重注选择器，分支信号不被清零；
  切换选择值后旧分支判 `INACTIVE`，切回后保留位重新生效。
- `TestInjectMuxThreeFailureClassesAndAtomicity` /
  `TestInjectFailuresConcurrentWithReload`：三类选择值矛盾、超范围、时间戳回退
  各自可区分；失败不留半成品、不污染状态，且与热加载并发互不干扰。
- `TestRealExportCompactExtendedIDNegativeLabels`：紧凑空格写法、扩展帧高位 ID、
  负数数值表条目与宽松写法解析等价。
- `TestMixedEndianBoundaryOverwriteIsolation` / `TestSignedOverwriteBoundary`：
  混排字节序相邻/跨字节边界与有符号连续覆盖。

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

真实工具的紧凑导出（冒号/括号/引号周围不留空格）与扩展帧高位 ID 同样可直接加载：

```dbc
VERSION""
NS_:
BS_:
BU_:GW BODY
BO_ 2147484953 BODY:8 BODY
 SG_ Sel M:0|4@1+(1,0)[0,3]""GW
 SG_ Temp:8|8@1-(1,-40)[-40,215]"degC"GW
VAL_ 2147484953 Temp -1"ERR" 0"OK";
```

`2147484953` 带最高位标记，解析后是扩展帧 29 位 ID `1305`（`Message.Extended=true`），
查询和注入都用规范 ID `1305`。负数标签条目按信号位宽二补码解释（`0xFF -> -1`）。

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

保持语义可直接这样验证：先注入 `Mode=1, Gear=3`，再只注入 `Mode=1`，
立刻查询 `Gear` 仍为 3；同一信号连续注入（800 然后 100）后查询应为 100，
不会读出残留高位拼出的值。只写普通信号时不提 `Mode` 也允许，
选择值沿用上一帧（无历史则默认 0）。

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
