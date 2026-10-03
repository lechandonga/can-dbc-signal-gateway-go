# 解码、注入、失效、复用与热加载规则

实现位于 `internal/gateway`（单路 `Gateway`）与 `internal/fleet`（多路 `Manager`），
帧与位编解码位于 `internal/canbus`。全部本地运行，不用墙上时钟。

## 接收解码流程（HandleFrame）

固定顺序判定，任一环节失败即整体拒绝，不写入任何状态：

1. **未知报文** -> `UNKNOWN_MESSAGE`。
2. **长度校验**：`len(Data) != DLC`，短 -> `TRUNCATED_FRAME`，长 -> `LENGTH_MISMATCH`。
3. **时间戳顺序**：按报文 ID 分别单调不减；回退 -> `TIMESTAMP_REWOUND`，相等合法。
4. **复用选择**：先解复用器；选择值无任何分支布局 -> `MUX_LAYOUT_MISSING`。
5. 通过后仅更新本次布局内的信号，并保存该帧原始字节为该报文的"最后接受帧"，推进时间戳。

被拒绝帧绝不修改已有信号值、最后字节与时间戳。

## 信号注入（Inject）

`Inject(InjectRequest{MsgID, Values, Timestamp})` 按"报文 + 一组信号物理值"本地造帧，
构出的帧随后走与 `HandleFrame` **完全相同**的解码链路；成功后查询立即读到写入值。

**构帧规则（只动明确给出的信号）**

- 基帧 = 该报文最后一次被接受帧的原始字节副本；**从未收到时为全 0 的确定默认帧**
  （即所有原始位 0、物理量 = offset，标签为数值表中 raw=0 的项，无表则回退原始值）。
- 目标信号先逐位清零再写入量化后的原始值（`EncodeRaw` 自带清位）；
  同一报文其它信号的原始位原样保留，包括跨字节、两种字节序、有符号、缩放偏移、复用布局。
- 时间戳由调用方显式给出，且必须不早于该报文最后接受时间，否则 `TIMESTAMP_REWOUND`。
- 所有校验先于任何写入完成；失败不留半成品、不污染已有状态/基帧。

**量化与往返**：写入值先做四舍五入逆换算，查询值与写入值在半个 `factor` 步长内一致；
`EnforceRange` 要求物理值落在 `[min,max]` 且原始值能由信号位宽表示。

**失败分类（各自可区分）**

| 原因 | 触发条件 |
| --- | --- |
| `UNKNOWN_MESSAGE` | 报文 ID 不在当前定义 |
| `UNKNOWN_SIGNAL` | 报文存在但信号名不存在 |
| `VALUE_OUT_OF_RANGE` | 物理值超出 `[min,max]` 或量化后超出位宽 |
| `MUX_SELECTOR_ILLEGAL` | 复用器选择值本身不合法（非非负整数/超出选择器位宽） |
| `MUX_LAYOUT_MISMATCH` | 选择值与目标信号所属布局对不上（目标在别的 `m<v>` 分支） |
| `MUX_NO_WRITABLE_LAYOUT` | 该选择值下没有任何已定义的可写分支布局 |
| `MUX_LAYOUT_MISSING` | 构帧成功但解码链路上选择值无布局（与注入侧校验一致的最终防线） |
| `TIMESTAMP_REWOUND` | 注入时间早于最后接受时间 |

**复用切换语义**

- 注入值中包含复用器信号 -> 按其值切换；不包含 -> 沿用当前选择值，从未收到默认 0。
- 切换选择值后，旧布局信号的历史值仍保留可追溯，但查询结论为
  **`MUX_INACTIVE`（当前布局不生效）**，与 `TIMEOUT`、`NEVER_RECEIVED` 严格区分：
  - `MUX_INACTIVE`：信号定义存在、历史值存在，但当前选择值选不到它；
  - `TIMEOUT`：信号在当前布局内，但超时窗口内无更新；
  - `NEVER_RECEIVED`：定义存在但从未随任何帧到达。

## 数值表标签

状态快照含 `Raw`、物理量 `Value`、`Label`、`LabelKnown`、`Unit`、更新时间与 `MuxValue`。
命中数值表时给标签；未收录或无表 `LabelKnown=false`，展示层稳定回退为原始值十进制文本。

## 失效（超时）判定

- 配置：`Config.DefaultTimeout`（默认 1s）+ `SignalTimeouts`
  （`"报文名.信号名"` 优先于 `"信号名"`；`0` 回退默认，负值永不超时）。
- 查询时按传入 `now` 计算：`now-UpdatedAt > timeout` -> `TIMEOUT`，附带实际间隔与门限。
- 超时不改变已保存的值；新帧/成功注入立即恢复。

## 热加载（单路 Reload / 多路 per-bus Reload）

1. 新定义先在锁外完整解析+校验；期间解码/注入/查询不受影响。
2. 失败：返回 `dbc.ErrorList`，旧定义、旧状态、最后接受帧原样继续服务。
3. 成功：写锁内一次性替换整份 `Database` 并清空运行时状态与基帧（避免新旧混用）。
4. 多路下 `Manager.Reload(bus, r)` 只作用于命名的那一路；该路失败维持原样，
   其它路的定义/状态/统计互不串扰。

并发由每路独立的 `sync.RWMutex` 保证；`Manager` 自身另有一把锁仅保护路表。
竞态测试 `TestConcurrentDecodeQueryReload` 与 `TestMultiBusConcurrent` 在 `-race` 下运行。
