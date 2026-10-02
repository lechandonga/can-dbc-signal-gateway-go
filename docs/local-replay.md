# 本地复现指南

无需真实 CAN 硬件或任何外部服务：定义来自本地 DBC 文件，帧由脚本本地生成。

## 运行演示

```bash
go run ./cmd/demo
```

演示内置一份 DBC 和一条确定性帧序列，覆盖：正常帧、两种复用布局、非法复用选择值、
截断帧、长度不符帧、未知报文帧、时间戳回退帧、按信号超时与恢复，末尾打印各信号判定与统计。

## 运行测试（含竞态检测）

```bash
CGO_ENABLED=1 go test -race ./...
# 查看覆盖了哪些业务/输入/判定依据（每条用例均有结构化日志）：
CGO_ENABLED=1 go test -race ./... -v
```

测试日志统一形如：

```
[biz=<业务域>][input=<输入标识>] <判定依据与实际值>
```

业务域包括：`malformed-definition`、`error-location`、`valid-definition`、
`decode-endian-signed`、`multiplex`、`multiplex-illegal`、`reject-unknown`、
`reject-length`、`reject-truncated`、`no-pollution`、`timeout-occluded`、
`timeout`、`recovery`、`never-received`、`timestamp-order`、
`reload-fail`、`reload-ok`、`concurrent-race`、`roundtrip`、`simulator`。

## 自定义 DBC 与帧序列

1. 参照 `testdata/demo.dbc` 编写 DBC（支持子集见 `docs/dbc-subset.md`）。
2. 用 `dbc.Parse` 解析得到不可变 `*model.Database`。
3. 用 `cansim.NewBuilder(db)` 按物理量构造帧（自动处理字节序/符号/缩放/复用选择值）；
   用 `cansim.Truncate(frame, n)`、`cansim.WithID(frame, id)` 构造异常帧。
4. 用 `cansim.NewSequence(t0).Add(offset, id, data)...` 编写带时间戳的脚本，
   `Replay()` 多次重放结果完全一致。
5. 创建 `gateway.New(db, cfg)`，逐帧 `HandleFrame`，用显式 `now` 调用
   `GetSignal` / `Snapshot` 判定；`Reload(reader)` 热加载新定义。

最小骨架：

```go
db, _ := dbc.Parse(dbcFile)                       // 失败 -> dbc.ErrorList（逐条原因）
gw := gateway.New(db, gateway.Config{DefaultTimeout: 100 * time.Millisecond})
builder := cansim.NewBuilder(db)
f, _ := builder.Frame(256, nil, map[string]float64{"EngineSpeed": 800})
f.Timestamp = t0
res, err := gw.HandleFrame(f)                    // err 为 *canbus.DecodeError 时帧被拒绝
view, ok := gw.GetSignal(256, "EngineSpeed", t0.Add(150*time.Millisecond))
// view.Status: OK / TIMEOUT / NEVER_RECEIVED
```
