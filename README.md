# CAN DBC Signal Gateway (local, Go)

本地可运行的 CAN 信号解析与车辆状态维护组件：总线原始帧来自本地生成的确定性
帧序列，信号定义来自本地 DBC 文件，不依赖真实硬件、网络或外部服务。

## 模块结构

- `internal/dbc`：DBC 解析与整体校验。非法定义整体拒绝（all-or-nothing），
  返回带 `Reason`、行号、报文名、信号名的类型化错误。
- `internal/decode`：无状态的帧解码器。输出原始值、物理值与单位，失败返回
  可区分的 `RejectReason`。
- `internal/gateway`：车辆状态维护。保存每个信号的最新值、时间与有效性；
  支持超时、时间戳乱序规则、复用失效和热加载，并发安全。
- `internal/simulator`：按定义把物理/原始值编码成帧，生成可复现帧序列；
  提供截断/改字节工具用于负向测试。
- `cmd/demo`：端到端演示（`go run ./cmd/demo`）。
- `testdata/vehicle.dbc`：示例定义。

## 支持的 DBC 子集

- `VERSION / NS_ / BS_ / BU_` 段；常见元数据段（`CM_`、`BA_DEF_`、`BA_`、
  `BO_TX_BU_`、`SIG_GROUP_`、`EV_` 等）被识别并安全忽略。
- `BO_ ID Name: DLC Sender`，DLC 范围 0..8，ID 范围 11/29 位 CAN ID。
- `SG_ Name [M|mN] : start|len@ord sign (factor,offset) [min|max] "unit" rx...`
  - 起始位/长度（1..64 位）、`@1` Intel 与 `@0` Motorola、`+/-` 有无符号、
    缩放/偏移、单位、取值范围。
  - 复用：每报文至多一个 `M` 开关；`mN` 分支信号；`VAL_` 可定义合法选择值。
- `VAL_TABLE_`、`VAL_`（枚举值表；开关枚举键即合法选择值集合）。
- `SIG_VALTYPE_` 仅接受整数 `0`；浮点信号显式拒绝。

### 明确的边界

- 不支持扩展多路复用 `SG_MUL_VAL_`、浮点信号：识别到语法后以
  `unsupported_feature` 拒绝，而不是静默忽略。
- 不做行拼接（无续行符处理），只面向单文件定义。
- 无法识别的顶层关键字按 `syntax_error` 拒绝，避免“跳过即丢定义”。

### 拒绝原因（可机读、可区分）

`syntax_error`、`incomplete_structure`、`duplicate`、`invalid_signal`、
`bit_out_of_range`、`bit_overlap`、`mux_conflict`、`mux_dangling`、
`unsupported_feature`。错误会定位到行号、报文名与信号名（适用时）。

校验规则包括：信号位不得超出 DLC；同一布局内信号位不得重叠（不同 `mN`
分支的信号允许占用相同位）；存在 `mN` 必须有 `M`，反之亦然；开关枚举值
与分支布局必须一一对应；factor 不得为 0；max 不得小于 min。

## 字节序与位编号

线性位号统一为 `byte*8 + bit`，`bit 0` 为该字节 LSB，与 DBC 的 Intel 起始位
编号一致。

- Intel（`@1`）：起始位是原始 LSB，原始第 k 位在线性 `start+k`。
- Motorola（`@0`）：起始位（DBC 锯齿位号）是原始 MSB，随后位沿锯齿位号
  递减；越过偏移 0 时回绕到下一个更高字节的偏移 7（即锯齿位 `+15`）。
  最终 LSB 可能落在起始字节的下一个字节。

物理值：`physical = raw*factor + offset`；有符号原始值按长度做符号扩展。

## 解码与失效规则

帧处理顺序与结果稳定、可复现：

1. 查定义：未知 ID -> `unknown_message`，不影响任何状态。
2. 查长度：数据短于 DLC -> `frame_too_short`（截断）；长于 DLC ->
   `length_mismatch`（超长）。均不影响状态。
3. 读复用开关：
   - 开关原始值不在 `VAL_` 合法枚举内 -> `mux_value_illegal`；
   - 开关值合法但没有对应 `mN` 布局 -> `mux_layout_missing`。
4. 时间戳规则（在同一报文 ID 内独立判定）：
   - 仅接受非递减时间戳；更早的帧 -> `stale_timestamp`，不更新状态；
   - 相等时间戳按调用顺序应用。不同 ID 的时钟互不影响。
5. 通过全部检查后一次性更新该帧布局内信号的原始值、物理值、单位和时间。

信号状态：

- `never_received`：定义生效后从未收到；
- `ok`：在超时窗口内收到；
- `timeout`：`now - lastSeen > timeout`（严格大于，边界点仍为 ok）；
- `stale_branch`：复用报文最新一帧选择了别的分支；旧值保留但标记失效，
  再次收到该分支即恢复为 `ok`。

超时通过 `Config.DefaultTimeout` 全局配置，并可用
`Config.SignalTimeout["MessageName.SignalName"]` 按信号覆盖；`0` 表示不超时。
偶发单帧丢失（间隔不超过阈值）不会误判。

## 热加载语义

- `Reload(reader, source)` 先在锁外完成全部解析与校验；任何非法都会整体
  失败并返回原因，旧定义与既有状态继续可用，不会出现“部分生效却报失败”。
- 激活是单个写临界区：并发的解码与查询看到的要么是完整旧定义，要么是
  完整新定义，不会看到新旧混用。
- 激活后所有后续帧按新定义解码。布局（起始位、长度、字节序、符号、factor、
  offset）未变的同名同 ID 信号保留状态；新增/删除/布局变化的信号重置为
  `never_received`，保证旧字节不会被新布局误读。

## 本地复现

```bash
# 全部单测（含可区分原因日志）
go test ./...

# 竞态检测（并发用例要求稳定通过）
CGO_ENABLED=1 go test -race ./...

# 端到端演示
go run ./cmd/demo
```

### 如何构造帧序列

```go
db, _ := dbc.ParseFile("testdata/vehicle.dbc")
gw := gateway.New(db, gateway.Config{DefaultTimeout: 100 * time.Millisecond})

f, _ := simulator.NewBuilder(db).
    SetPhysical(256, "EngineSpeed", 800).   // raw = (800-0)/0.25
    SetRaw(256, "EngineTemp", 90).
    Build(256, 1000)                         // 时间戳单位为毫秒
gw.ProcessFrame(decode.Frame{ID: f.ID, Data: f.Data, Timestamp: f.Timestamp})

for _, s := range gw.Snapshot(1050) {       // 单次锁内一致快照
    fmt.Printf("%s raw=%d phys=%.2f %s %s\n",
        s.Signal, s.Raw, s.Physical, s.Unit, s.Status)
}
```

负向场景可直接构造：`simulator.Truncate(f, 3)`（截断）、
`simulator.WithData(f, make([]byte, 9))`（长度不符）、
复用帧把开关字节设为非法值（`mux_value_illegal`）或回退时间戳
（`stale_timestamp`）。

## 测试覆盖与日志

测试日志以 `business=... case=<输入标识> ... basis=<判定依据>` 输出，覆盖：

- 畸形/不完整/自相矛盾定义（15 个分类拒绝用例，校验原因与定位）；
- 跨字节 Intel/Motorola、有符号、缩放偏移；
- 复用分支选择、非法选择值、缺失布局、分支失效与恢复；
- 超时边界、偶发丢帧、恢复、按信号超时覆盖；
- 未知报文、截断、长度不符、乱序帧不污染状态；
- 热加载失败回滚、成功激活、兼容状态保留；
- 4 读 + 4 写 + 热加载的并发用例，`-race` 下稳定通过。
