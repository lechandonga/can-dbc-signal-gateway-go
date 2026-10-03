# 本地复现指南

无需真实 CAN 硬件或任何外部服务：定义来自本地 DBC 文件（可用真实工具导出文件原样喂入），
帧由脚本本地生成或按信号注入，时间戳全部显式给出。

## 运行演示

```bash
go run ./cmd/demo
```

演示覆盖：正常帧、两种复用布局、按信号注入（保持未指定信号位）、非法复用选择值、
截断帧、长度不符帧、未知报文帧、时间戳回退帧、超时与恢复、数值表标签与回退，
末尾打印各信号判定（含 `MUX_INACTIVE`）与统计。

## 运行测试（含竞态检测）

```bash
CGO_ENABLED=1 go test -race ./...
CGO_ENABLED=1 go test -race ./... -v
```

日志统一形如 `[biz=<业务域>][input=<输入标识>] <判定依据与实际值>`。
本轮新增业务域：`real-export-equivalence`、`value-table`、`value-table-query`、
`import-summary`、`mixed-endian-adjacent`、`mixed-endian-overlap`、
`inject-roundtrip`、`inject-preserve`、`inject-default`、`inject-mux-mismatch`、
`inject-mux-no-layout`、`inject-mux-selector-illegal`、`inject-mux-inactive`、
`inject-reject`、`multibus-unknown`、`multibus-isolation`、`multibus-summary`、
`multibus-reload`、`multibus-race`、`multibus-scale`。

## 造定义文件（验证真实写法等价性）

- `testdata/real_tool_a.dbc` 与 `testdata/real_tool_b.dbc` 模拟两种工具导出同一语义：
  空白/分号/断行差异、`VAL_TABLE_` 条目乱序、具名表与内联 `VAL_` 混用、
  `BA_`/`SIG_GROUP_`/`NS_` 块等。改其中措辞后跑 `TestRealExportEquivalence`，
  逐字节的构帧结果与解码原始值必须仍一致。
- 混排字节序边界直接看 `TestMixedEndianAdjacentOK`（相邻不冲突，加载且解码正确）
  与 `TestMixedEndianTrueOverlapRejected`（真占同一位 -> `BIT_OVERLAP`，
  错误定位到报文 `MIX_BAD` 与信号 `MotoNibble`）。

导入后拿摘要：

```go
res, err := dbc.ParseDetailed(f)   // 失败时 res.Summary 仍可用
for _, a := range res.Summary.Adopted { /* 报文/信号/数值表 */ }
for _, i := range res.Summary.Ignored { /* 行号/种类/为什么不影响解码 */ }
for _, e := range res.Summary.Rejected { /* 行号/报文/信号/Reason */ }
```

## 造帧与注入

1. `dbc.Parse`/`ParseDetailed` 得到不可变 `*model.Database`。
2. `cansim.NewBuilder(db)` 按物理量造帧（自动处理字节序/符号/缩放/复用选择值）；
   `cansim.Truncate`、`cansim.WithID` 造异常帧。
3. 或直接按信号注入，未指定信号自动保持上一帧原始位：

```go
gw := gateway.New(db, gateway.Config{DefaultTimeout: 100 * time.Millisecond})
res, err := gw.Inject(gateway.InjectRequest{
    MsgID:     256,
    Values:    map[string]float64{"EngineSpeed": 800}, // 同报文其它信号位保持不变
    Timestamp: t0,                                      // 时间戳必须显式给出
})
view, ok := gw.GetSignal(256, "EngineSpeed", t0)
// view.State.Raw/Value/Label/LabelKnown；标签未收录时回退为 raw 十进制文本
```

4. 多路：`fleet.NewManager()` 后 `Load("PT", reader, cfg)`，按总线名
   `HandleFrame`/`Inject`/`GetSignal`；未知总线得到 `*fleet.BusError`。
5. `cansim.NewSequence(t0).Add(offset, id, data)...` 编确定性脚本，
   `Replay()` 多次重放结果完全一致。
