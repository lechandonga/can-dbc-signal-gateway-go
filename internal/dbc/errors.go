package dbc

import "fmt"

// Reason 为定义被拒绝的可区分原因。
type Reason string

const (
	ReasonSyntax              Reason = "SYNTAX"                // 行语法不符合 DBC 语法
	ReasonUnknownStatement    Reason = "UNKNOWN_STATEMENT"     // 出现解析器不支持的语句
	ReasonDuplicateMessage    Reason = "DUPLICATE_MESSAGE"     // 报文 ID/名称重复
	ReasonDuplicateSignal     Reason = "DUPLICATE_SIGNAL"      // 同一报文内信号重名
	ReasonOrphanSignal        Reason = "ORPHAN_SIGNAL"         // 信号定义出现在任何 BO_ 之前
	ReasonBadLength           Reason = "BAD_LENGTH"            // 位长非法（<=0 或 >64）
	ReasonBadStartBit         Reason = "BAD_START_BIT"         // 起始位越界或字节序下不自洽
	ReasonSignalOutOfFrame    Reason = "SIGNAL_OUT_OF_FRAME"   // 信号位跨越了报文 DLC
	ReasonBitOverlap          Reason = "BIT_OVERLAP"           // 同一复用布局内信号位重叠
	ReasonBadScaling          Reason = "BAD_SCALING"           // factor<=0 或 min>max
	ReasonBadMultiplex        Reason = "BAD_MULTIPLEX"         // 复用结构非法
	ReasonBadFloatType        Reason = "BAD_FLOAT_TYPE"        // SIG_VALTYPE_ 声明了浮点信号
	ReasonDanglingReference   Reason = "DANGLING_REFERENCE"    // VAL_/CM_/BA_ 引用了不存在的对象
	ReasonIncomplete          Reason = "INCOMPLETE"            // 必需结构缺失（无报文等）
	ReasonDuplicateValueTable Reason = "DUPLICATE_VALUE_TABLE" // 数值表重名或信号重复挂表
)

// DefError 描述一条非法定义，带行号与报文/信号定位。
type DefError struct {
	Reason  Reason
	Line    int    // 1 基行号；0 表示文件级问题
	Message string // 人类可读说明
	MsgID   uint32
	MsgName string
	SigName string
}

func (e *DefError) Error() string {
	loc := ""
	if e.MsgName != "" || e.MsgID != 0 {
		loc = fmt.Sprintf(" [message=%s(id=%d)", e.MsgName, e.MsgID)
		if e.SigName != "" {
			loc += fmt.Sprintf(" signal=%s", e.SigName)
		}
		loc += "]"
	} else if e.SigName != "" {
		loc = fmt.Sprintf(" [signal=%s]", e.SigName)
	}
	if e.Line > 0 {
		return fmt.Sprintf("dbc line %d: %s%s", e.Line, e.Reason, loc+": "+e.Message)
	}
	return fmt.Sprintf("dbc: %s%s: %s", e.Reason, loc, e.Message)
}

// ErrorList 为一次解析中收集到的全部错误；非法定义不会被静默跳过。
type ErrorList []*DefError

func (el ErrorList) Error() string {
	if len(el) == 0 {
		return "dbc: no errors"
	}
	return fmt.Sprintf("dbc definition rejected with %d error(s): first: %s", len(el), el[0].Error())
}

// Errors 返回全部逐条错误（调用方可用于日志与可解释性）。
func (el ErrorList) Errors() []*DefError { return el }
