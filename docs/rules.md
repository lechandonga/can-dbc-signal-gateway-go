# 解码、失效判定与热加载规则

实现位于 `internal/gateway`（`Gateway`），帧与位编解码位于 `internal/canbus`。

## 解码流程

对每帧按固定顺序判定，任一环节失败即整体拒绝，不写入任何状态：

1. **未知报文**：ID 不在当前定义中 -> `UNKNOWN_MESSAGE`。
2. **长度校验**：`len(Data) != DLC`：
   - `len < DLC` -> `TRUNCATED_FRAME`（被截断）；
   - `len > DLC` -> `LENGTH_MISMATCH`（多数据）。两种原因可区分。
3. **时间戳顺序**：按报文 ID 分别维护最后接受时间，严格早于它 -> `TIMESTAMP_REWOUND`；
   相等时间戳合法。不同 ID 互不影响。
4. **复用选择**：先解复用器原始值；
   - 该值没有任何 `m<value>` 分支信号 -> `MUX_LAYOUT_MISSING`（含"非法选择值"语义）。
5. 全部通过后，仅更新本次布局内的信号（复用器 + 该分支信号；非复用报文更新所有普通信号），
   并推进该 ID 的最后时间戳。

被拒绝帧绝不修改已有信号值与 `UpdatedAt`（测试 `TestRejectUnknownLengthTruncatedNoPollution`、
`TestMultiplexLayouts` 验证"坏帧不污染"）。

## 物理量与状态

- 无符号：`value = raw*factor + offset`；有符号：先对信号位宽做二补码扩展。
- 状态保存：`Raw`、物理量 `Value`、`Unit`、更新时间 `UpdatedAt`，复用信号还记录本次 `MuxValue`。
- 查询 API：`GetSignal(id, sig, now)` 返回值与判定；定义中不存在的信号返回 `ok=false`，
  与"定义存在但从未收到"（`NEVER_RECEIVED`）严格区分。`Snapshot(now)` 返回全量拷贝。

## 失效（超时）判定

- 配置：`Config.DefaultTimeout`（默认 1s）+ `SignalTimeouts`，
  键 `"报文名.信号名"` 优先于 `"信号名"`；`0` 回退默认值，负值表示该信号永不超时。
- 判定在查询时按传入的 `now` 计算：`now - UpdatedAt > timeout` -> `TIMEOUT`，
  返回值带可读原因（实际间隔与门限）。
- 偶发丢帧不误判：只要间隔不超过门限即为 `OK`；新帧到达立即恢复为 `OK`。
- 超时判定不改变已保存的值：超时期间仍可读到最后一次有效值与它的更新时间。

## 时间戳规则（确定性与自洽）

- 不使用墙上时钟参与任何业务判定；所有帧显式携带时间戳，查询显式给 `now`，
  因此同一帧序列在任何机器上结论一致、可复现。
- 每路报文（按 CAN ID）时间戳单调不减；回退帧拒绝，保证状态只沿时间前进。

## 热加载语义

`Reload(reader)`：

1. 先在锁外用 `dbc.Parse` 完整解析并校验新定义；解析期间解码/查询完全不受影响。
2. 非法：返回 `dbc.ErrorList`，旧定义与旧状态原封不动继续可用，绝无部分生效。
3. 合法：在写锁内一次性替换整份 `Database` 与状态表，然后解锁。
4. 并发的解码与查询持有读锁，观察到的要么是整份旧定义+旧状态，要么是整份新定义+空状态，
   不存在新旧混用的半更新视图。
5. 新定义生效后，后续帧一律按新定义解码；旧定义下的历史状态被清空
   （它引用的信号可能已不存在，保留会造成新旧混用）。

并发安全由单个 `sync.RWMutex` 保证；竞态测试 `TestConcurrentDecodeQueryReload`
在 `go test -race` 下运行。
