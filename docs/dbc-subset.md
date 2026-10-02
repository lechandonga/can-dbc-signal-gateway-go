# 支持的 DBC 子集与边界

解析器位于 `internal/dbc`，只接受本地文本 DBC，不连接任何外部服务。

## 支持的语句

| 语句 | 处理方式 |
| --- | --- |
| `VERSION "..."` | 解析，无分号 |
| `NS_ :` 命名空间块 | 块内容（到第一个空行）原样忽略 |
| `BS_:` | 解析（仅空总线参数） |
| `BU_: ECU1 ECU2` | 解析节点列表 |
| `BO_ <id> <Name>: <DLC> <Sender>` | 报文定义；`id` 支持 11/29 位（>0x1FFFFFFF 拒绝），DLC 必须 1..8 |
| `SG_ Name [mux] : start|len@order+- (factor,offset) [min,max] "unit" Receivers` | 信号定义 |
| `CM_ "..."`、`CM_ BO_ id "..."`、`CM_ SG_ id sig "..."` | 注释；引用的报文/信号必须存在 |
| `VAL_ id sig v "label" ...` | 取值表引用；引用对象必须存在 |
| `SIG_VALTYPE_ id sig : 0` | 允许（整型）；类型 1/2（float/double）明确拒绝 |
| `SIG_GROUP_`、`BA_`、`BA_DEF_`、`VAL_TABLE_`、`BO_TX_BU_`、`EV_` 等 | 语法上识别并容忍（内容不参与解码） |

未识别的语句一律返回 `UNKNOWN_STATEMENT` 拒绝，不做静默跳过。
多行语句（未闭合引号或缺少 `;`）会拼接为逻辑行；文件结束仍未闭合返回 `SYNTAX`。
仅支持单行 `//` 注释与 DBC 自带 `CM_`，不支持 C 风格块注释。

## 信号语法

- 起始位/长度：`start|length`，`length` 必须 1..64；起始位必须在帧内。
- 字节序：`@1` = Intel（小端，start 为 LSB）；`@0` = Motorola（大端，DBC sawtooth，start 为 MSB）。
- 符号：`+` 无符号，`-` 有符号（二补码）。
- 缩放：`physical = raw*factor + offset`；`factor` 必须 > 0，`min <= max`。
- 单位/接收方：保留并随状态输出。

## Motorola 布局自洽规则

sawtooth 位号到线性位（bit0 = 字节0最低位）映射：`linear = 8*floor(pos/8) + (7-pos mod 8)`，
从 MSB 起每走一位：行号在字节左边界时下跳一行（`pos += 15`），否则左移（`pos--`）。

合法判据（与 Vector DBEditor / cantools 一致）：MSB 所在列必须容纳信号在首字节内向左展开：

```
(start % 8) >= (start - length + 1) mod 8   // mod 为非负模
```

例如 `7|16@0`（跨 byte0/byte1 的标准大端字）合法；`8|16@0`、`8|5@0` 斜跨字节边界，
返回 `BAD_START_BIT` 拒绝。

## 多路复用

- 一个报文至多一个 `M` 复用器信号；多个 -> `BAD_MULTIPLEX`。
- 普通分支信号写作 `SG_ Name m<value> : ...`；value 必须非负。
- 分支信号存在但报文没有复用器 -> `BAD_MULTIPLEX`。
- 扩展复用 `m<value>M`（嵌套/扩展多路复用）本子集不支持，返回 `BAD_MULTIPLEX`。
- 同一选择值布局内信号位不得重叠（`BIT_OVERLAP`）；不同选择值允许复用相同位。

## 拒绝原因（`dbc.Reason`，可区分且定位到报文/信号/行号）

`SYNTAX`、`UNKNOWN_STATEMENT`、`DUPLICATE_MESSAGE`、`DUPLICATE_SIGNAL`、
`ORPHAN_SIGNAL`、`BAD_LENGTH`、`BAD_START_BIT`、`SIGNAL_OUT_OF_FRAME`、
`BIT_OVERLAP`、`BAD_SCALING`、`BAD_MULTIPLEX`、`BAD_FLOAT_TYPE`、
`DANGLING_REFERENCE`、`INCOMPLETE`。

一次解析会收集全部错误（`dbc.ErrorList`），不会因首个错误短路；
每条错误带 `Line`（1 基）、`MsgID/MsgName`、`SigName`。
