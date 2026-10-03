# 支持的 DBC 子集与边界

解析器位于 `internal/dbc`，只接受本地文本 DBC，不连接任何外部服务。
真实工具（Vector 导出、cantools、各厂家工具链）原样导出的文件可直接喂入，
不需要手工改写示例格式。

## 导入结论：采用 / 忽略 / 拒绝

`dbc.ParseDetailed(r)` 同时返回 `*model.Database` 与 `*dbc.Summary`：

- **Adopted（采用）**：参与解码的报文与信号，逐条列出名称、字节序、位长、
  复用角色（`M`/`m3`/普通）与关联数值表。
- **Ignored（明确忽略）**：已识别但**不影响解码**的构造，带行号、种类、对象与
  "为什么不影响解码"的说明。只有满足"不影响解码"才会进入 Ignored；会影响解码
  但当前不支持的构造一律 Rejected，绝不出现"加载成功但实际解错"。
- **Rejected（拒绝）**：`[]*DefError`，与返回的 `dbc.ErrorList` 一一对应，
  每条带行号、报文、信号与可区分 `Reason`。解析失败时 Summary 仍返回，便于定位。

`dbc.Parse(r)` 为兼容入口，仅返回 Database/Error。

## 真实写法差异（同一语义必须等价）

以下差异均被等价吸收，不改变解码结论（测试
`TestRealExportEquivalence` 用两份不同工具导出的同义文件逐字节比对）：

- 空白差异：`BO_ 100 N:8 S` 与 `BO_ 100 N : 8 S`、`SG_ S:0|16@1+...` 等冒号/竖线/@ 周边空白。
- 分号差异：`BS_:` / `BS_:;`，语句结尾分号可有可无（按 DBC 规则）。
- 断行：未闭合引号或缺少 `;` 的逻辑语句跨行自动拼接（`VAL_TABLE_`、`VAL_`、`CM_`、`BA_` 等）。
- 注释：`//` 行注释与 `CM_` 注释；`NS_ :` 块内的命名空间标记行。
- 属性：`BA_DEF_*`、`BA_`、`SGTYPE_*`、`CAT_*`、`SIG_GROUP_` 等元数据整体识别为 Ignored。
- 扩展帧：`BO_` / `VAL_` / `CM_` / `SIG_VALTYPE_` 中 ID 高位置 1 的 Vector 扩展帧写法
  （如 `2147483904` 表示 `0x80000100`）自动折算为 29 位真实 ID。
- 接收器列表：无接收方的信号（空节点导出）照常采用，接收器只是信息性字段。

## 支持的语句

| 语句 | 处理方式 |
| --- | --- |
| `VERSION "..."` | 解析，无分号 |
| `NS_ :` 命名空间块 | 块内标记行逐条记入 Ignored（不影响解码） |
| `BS_[:;]` | 解析（仅空总线参数） |
| `BU_: ECU1 ECU2` | 解析节点列表 |
| `BO_ <id> <Name>: <DLC> <Sender>` | 报文定义；11/29 位 ID（高位 1 为扩展帧标记），DLC 1..8 |
| `SG_ Name [mux] : start|len@order+- (factor,offset) [min,max] "unit" Receivers` | 信号定义（空白宽容） |
| `CM_ ...` | 全局/报文/信号注释；引用对象必须存在，否则拒绝 |
| `VAL_TABLE_ Name v "label" ...;` | 文件级具名数值表，**真正参与解码（枚举标签）**；可断行、条目顺序任意 |
| `VAL_ id sig v "label" ...;` | 内联数值表；参与解码 |
| `VAL_ id sig TableName;` | 引用具名表；表未定义 -> `DANGLING_REFERENCE` |
| `SIG_VALTYPE_ id sig : 0` | 整型（默认），记为 Ignored；类型 1/2（float/double）明确拒绝 |
| `SIG_GROUP_`、`BA_`、`BA_DEF_*`、`BO_TX_BU_`、`EV_`、`CAT_*` 等 | 识别并记入 Ignored（不参与解码） |

未识别语句返回 `UNKNOWN_STATEMENT` 拒绝，不做静默跳过。
文件结束仍有未闭合引号/分号返回 `SYNTAX`。不支持 C 风格块注释。

## 数值表（枚举）语义

- 信号可关联内联 `VAL_` 或具名 `VAL_TABLE_`；两种写法解析结果等价。
- 查询信号时同时返回原始值与标签：
  - 命中：`State.Label` 为标签文本，`State.LabelKnown=true`；
  - 未收录（含信号没有数值表）：`LabelKnown=false`，调用方按**稳定回退**
    使用 `strconv.FormatUint(State.Raw, 10)` 展示原始值。回退不依赖 map 迭代顺序，结论确定。
- 数值表只影响标签展示，**不影响原始位与物理量解码**；因此未知枚举值不拒绝帧。

## 信号语法

- 起始位/长度：`start|length`，`length` 1..64；起始位必须在帧内。
- 字节序：`@1` Intel（小端，start=LSB）；`@0` Motorola（大端，DBC sawtooth，start=MSB）。
- 符号：`+` 无符号，`-` 有符号（二补码）。
- 缩放：`physical = raw*factor + offset`；`factor > 0`，`min <= max`。

## Motorola 布局与混排字节序

DBC sawtooth 行号与线性位序（bit0=byte0 最低位）一一对应；
从 MSB 起每走一位：在字节左边界（`pos%8==0`）时 `pos+=15` 下跳一行，否则 `pos--`。

自洽判据：`(start % 8) >= (start-length+1) mod 8`（非负模）。不满足（如 `8|16@0`）
为斜跨字节，返回 `BAD_START_BIT`。

**混排验收口径**：同一报文内 Intel/Motorola 信号按真实占用的线性位检查——
- 真正占用同一位（无论字节序是否相同）-> `BIT_OVERLAP`，定位到具体报文与后定义信号；
- 只是紧挨着、线性位不相交 -> 正常加载，编解码结论逐位正确
  （测试 `TestMixedEndianAdjacentOK` / `TestMixedEndianTrueOverlapRejected`）。

## 多路复用

- 一个报文至多一个 `M` 复用器；多个 -> `BAD_MULTIPLEX`。
- 分支信号 `SG_ Name m<value> : ...`，value 非负。
- 分支存在但无复用器 -> `BAD_MULTIPLEX`。
- 扩展复用 `m<value>M`（嵌套）不支持 -> `BAD_MULTIPLEX`（会影响解码，明确拒绝而非猜解）。
- 同一选择值布局内位不得重叠（`BIT_OVERLAP`）；不同选择值允许复用相同位。

## 拒绝原因

`SYNTAX`、`UNKNOWN_STATEMENT`、`DUPLICATE_MESSAGE`、`DUPLICATE_SIGNAL`、
`ORPHAN_SIGNAL`、`BAD_LENGTH`、`BAD_START_BIT`、`SIGNAL_OUT_OF_FRAME`、
`BIT_OVERLAP`、`BAD_SCALING`、`BAD_MULTIPLEX`、`BAD_FLOAT_TYPE`、
`DANGLING_REFERENCE`、`INCOMPLETE`、`DUPLICATE_VALUE_TABLE`。

一次解析收集全部错误，不因首个错误短路；每条带 `Line`、`MsgID/MsgName`、`SigName`。
