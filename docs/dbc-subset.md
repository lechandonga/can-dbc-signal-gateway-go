# 支持的 DBC 范围与导入口径

解析器位于 `internal/dbc`，只读取本地文本 DBC，不连接任何外部服务。

## 推荐入口

- `dbc.Parse(reader)`：原有单返回值入口继续可用。
- `dbc.ParseDetailed(reader)`：返回 `(*model.Database, *dbc.ImportSummary, error)`，同时给出采用、忽略和拒绝明细。

`ImportSummary` 包含：

- `AdoptedMessages`：被采用的报文、DLC、信号名列表。
- `Ignored`：已识别且确认不改变位解码的元数据，按类型聚合并给出首尾行号、数量和原因。
- `Rejected`：所有拒绝原因；每条包含行号、报文 ID/名称、信号名与具体原因。

解析采用“先完整校验，成功后才交付定义”的事务语义。任何拒绝存在时，不返回可加载的数据库。

## 支持的真实写法

| 写法 | 口径 |
| --- | --- |
| `VERSION "..."`、可选分号 | 采用，跨行引号拼接为逻辑行 |
| `NS_ :` 块、空行结束 | 块内内容忽略 |
| `BS_:`、`BU_:` | 采用；空节点列表只作为文件头语法接受 |
| `BO_ <id> <Name>: <DLC> <Sender>` | 采用；DLC 1..8，ID 不超过 29 位 |
| `SG_ ... @1/@0 +/−` | 采用；空格、接收方缺失、断行和分号差异不改变语义 |
| `CM_` 全局/报文/信号注释 | 校验引用，内容不参与解码 |
| `SIG_VALTYPE_ ... : 0` | 采用为整型；1/2 浮点信号拒绝 |
| `VAL_TABLE_`、`VAL_ id sig Table;` | 采用为枚举/数值表 |
| `VAL_ id sig v "label"...` | 采用为信号内联枚举 |
| `BA_`、`BA_DEF_`、`SIG_GROUP_`、`BO_TX_BU_` 等元数据 | 明确记录为忽略，不影响解码 |

未识别或会改变位布局/解码结果但当前不支持的构造返回 `UNKNOWN_STATEMENT` 或更具体原因，不会伪装成加载成功。

## 数值表语义

- 查询状态同时返回 `Raw`、`Value`、`Label`、`HasLabel`。
- 内联 `VAL_` 与 `VAL_TABLE_` 引用对同一组值产生等价标签映射。
- 未收录原始值稳定返回 `Label==""`、`HasLabel=false`，不猜测标签。
- 引用不存在的报文、信号或数值表会被拒绝并定位到具体行。

## 位布局和字节序

- `@1` Intel：start bit 为 LSB，线性 CAN 位从 start 向高位展开。
- `@0` Motorola：start bit 为 MSB，按 DBC sawtooth 规则向低位/下一字节展开。
- 混排只在同一位真实重叠时拒绝；Intel/Motorola 信号仅相邻且位集合不相交时正常采用。
- 复用报文按“复用器 + 普通信号 + 同一选择分支”检查重叠；不同选择分支可复用相同物理位。
- 斜跨字节边界且不自洽的 Motorola 布局返回 `BAD_START_BIT`。

## 多路复用

- `M` 为复用器；`m<value>` 为分支信号。
- 一个报文至多一个一级复用器。
- 扩展/嵌套复用 `m<value>M` 当前明确拒绝，不会按普通信号误解。
- 没有任何分支信号的选择值可通过定义加载，但收到或注入该选择值时拒绝为 `MUX_LAYOUT_MISSING`。

## 拒绝原因

`SYNTAX`、`UNKNOWN_STATEMENT`、`DUPLICATE_MESSAGE`、`DUPLICATE_SIGNAL`、
`ORPHAN_SIGNAL`、`BAD_LENGTH`、`BAD_START_BIT`、`SIGNAL_OUT_OF_FRAME`、
`BIT_OVERLAP`、`BAD_SCALING`、`BAD_MULTIPLEX`、`BAD_FLOAT_TYPE`、
`DANGLING_REFERENCE`、`INCOMPLETE`。
