// Package dbc 解析本地 DBC 定义文件的一个严格子集。
//
// 支持的语句见 docs/dbc-subset.md。未知语句一律拒绝（不静默跳过）；
// 解析成功后还会执行结构校验（位重叠、越界、复用合法性等）。
package dbc

import (
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

var (
	versionRe = regexp.MustCompile(`^VERSION\s+"(.*)"\s*$`)
	boRe      = regexp.MustCompile(`^BO_\s+(\d+)\s+([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(\d+)\s+(\S+)\s*$`)
	// 对空白更宽容的 SG_ 形式：允许冒号/竖线/@ 周围多余空白、接收器可为空（Vector 空节点写法）。
	sgRe       = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)(?:\s+(M|m\d+M?))?\s*:\s*(\d+)\s*\|\s*(\d+)\s*@\s*([01])([+-])\s*\(\s*([^,]*?)\s*,\s*([^)]*?)\s*\)\s*\[\s*([^,]*?)\s*,\s*([^\]]*?)\s*\]\s*"([^"]*)"\s*(.*)$`)
	muxSimple  = regexp.MustCompile(`^m(\d+)$`)
	muxBoth    = regexp.MustCompile(`^m(\d+)M$`)
	valTableRe = regexp.MustCompile(`^(?s)([A-Za-z_][A-Za-z0-9_]*)\s+(.+)$`)
	valPairRe  = regexp.MustCompile(`(-?\d+)\s+"((?:[^"\\]|\\.)*)"`)
	sgValRe    = regexp.MustCompile(`^SIG_VALTYPE_\s+(\d+)\s+([A-Za-z_][A-Za-z0-9_]*)\s*:\s*(\d+)\s*$`)
)

// Summary 描述一次 DBC 导入的完整结论：采用了什么、明确忽略了什么、拒绝了什么。
// 即使解析失败（Parse 返回 error），Summary 仍会随结果返回，便于定位与日志。
type Summary struct {
	// Adopted 为采用的报文（ID 升序），每条列出信号名与是否关联数值表。
	Adopted []AdoptedMessage
	// Ignored 为已识别但不影响解码、因此明确忽略的内容；只收录不参与解码的构造。
	Ignored []IgnoredItem
	// Rejected 为拒绝明细，与 Parse 返回的 ErrorList 一一对应。
	Rejected []*DefError
}

// AdoptedMessage 为摘要中一条被采用的报文。
type AdoptedMessage struct {
	MsgID   uint32
	MsgName string
	DLC     int
	Signals []AdoptedSignal
}

// AdoptedSignal 为摘要中一个被采用的信号。
type AdoptedSignal struct {
	Name       string
	Order      string
	Length     int
	Mux        string // "" 普通；"M" 复用器；"m3" 选择值 3 的分支
	ValueTable string // 关联数值表名；无数值表为 ""
}

// IgnoredItem 为一条明确忽略的内容。
type IgnoredItem struct {
	Line    int
	Kind    string // 忽略项种类，如 BA_、BA_DEF_、NS_BLOCK、EMPTY_RECEIVERS、FLOAT_SIG_VALTYPE_0
	Subject string // 涉及的对象（可为空）
	Why     string // 为什么忽略不影响解码
}

// Result 为详细解析结果。
type Result struct {
	Database *model.Database
	Summary  *Summary
}

// Parse 解析 DBC 内容，成功返回不可变的 model.Database。
// 失败时返回 ErrorList，包含全部逐条错误（含行号与报文/信号定位）。
// 兼容既有调用；需要导入摘要时使用 ParseDetailed。
func Parse(r io.Reader) (*model.Database, error) {
	res, err := ParseDetailed(r)
	if err != nil {
		return nil, err
	}
	return res.Database, nil
}

// ParseDetailed 与 Parse 相同，但额外返回导入摘要（解析失败时摘要同样可用）。
func ParseDetailed(r io.Reader) (*Result, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		s := &Summary{}
		el := ErrorList{{Reason: ReasonSyntax, Message: err.Error()}}
		s.Rejected = el
		return &Result{Summary: s}, el
	}
	p := &parser{
		msgs:          map[uint32]*model.Message{},
		names:         map[string]uint32{},
		tables:        map[string]*model.ValueTable{},
		pendingInline: []pendingInline{},
	}
	p.parseLines(strings.Split(string(data), "\n"))
	if len(p.errors) == 0 {
		p.resolveInlineTables()
	}
	if len(p.errors) == 0 {
		p.validate()
	}
	summary := p.buildSummary()
	if len(p.errors) > 0 {
		summary.Rejected = p.errors
		return &Result{Summary: summary}, ErrorList(p.errors)
	}
	if len(p.msgs) == 0 {
		p.errf(0, ReasonIncomplete, "DBC defines no messages")
		summary.Rejected = p.errors
		return &Result{Summary: summary}, ErrorList(p.errors)
	}
	db := &model.Database{Messages: p.msgs, Names: p.names, ValueTables: p.tables}
	return &Result{Database: db, Summary: summary}, nil
}

type parser struct {
	msgs          map[uint32]*model.Message
	names         map[string]uint32
	tables        map[string]*model.ValueTable
	pendingInline []pendingInline // 引用具名数值表的内联 VAL_，待全部 VAL_TABLE_ 收集完再解析
	errors        []*DefError
	current       *model.Message
	inNS          bool
	ignored       []IgnoredItem
}

// pendingInline 记录形如 VAL_ id Sig TableName; 的具名表引用（空表名内联则就地解析）。
type pendingInline struct {
	line      int
	msgID     uint32
	sigName   string
	tableName string
}

func (p *parser) errf(line int, reason Reason, msg string, args ...any) {
	e := &DefError{Reason: reason, Line: line, Message: fmt.Sprintf(msg, args...)}
	if p.current != nil {
		e.MsgID = p.current.ID
		e.MsgName = p.current.Name
	}
	p.errors = append(p.errors, e)
}

func (p *parser) sigErr(line int, sig string, reason Reason, msg string, args ...any) {
	p.sigErrIn(p.current, line, sig, reason, msg, args...)
}

func (p *parser) sigErrIn(m *model.Message, line int, sig string, reason Reason, msg string, args ...any) {
	e := &DefError{Reason: reason, Line: line, SigName: sig, Message: fmt.Sprintf(msg, args...)}
	if m != nil {
		e.MsgID = m.ID
		e.MsgName = m.Name
	}
	p.errors = append(p.errors, e)
}

func (p *parser) parseLines(lines []string) {
	i := 0
	for i < len(lines) {
		lineNo := i + 1
		raw := strings.TrimRight(lines[i], "\r")
		trimmed := strings.TrimSpace(raw)
		i++

		if p.inNS {
			if trimmed == "" {
				p.inNS = false
			} else {
				p.ignored = append(p.ignored, IgnoredItem{Line: lineNo, Kind: "NS_BLOCK",
					Subject: trimmed, Why: "namespace declaration token does not affect decoding"})
			}
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}

		// 带分号或引号的逻辑行可能跨多行，先拼接为完整语句。
		full := raw
		if needsContinuation(trimmed) {
			for i < len(lines) && !statementComplete(full) {
				full += "\n" + strings.TrimRight(lines[i], "\r")
				i++
			}
			if !statementComplete(full) {
				p.errf(lineNo, ReasonSyntax, "statement not terminated (';' missing or unterminated string)")
				continue
			}
		}
		p.parseStatement(lineNo, strings.TrimSpace(full))
	}
}

func needsContinuation(s string) bool {
	if strings.Count(s, `"`)%2 == 1 {
		return true
	}
	prefixes := []string{
		"CM_", "BA_", "BA_DEF_", "VAL_ ", "SIG_GROUP_",
		"SIG_VALTYPE_", "EV_ ", "ENVVAR_DATA_", "BO_TX_BU_", "SGT_REL_",
		"SIG_TYPE_REF_", "VAL_TABLE_",
	}
	if strings.HasPrefix(s, "VERSION") {
		return false // VERSION "..." 不带分号，引号闭合即为完整
	}
	for _, pre := range prefixes {
		if strings.HasPrefix(s, pre) {
			return !strings.HasSuffix(s, ";")
		}
	}
	return false
}

func statementComplete(s string) bool {
	return strings.Count(s, `"`)%2 == 0 && strings.HasSuffix(strings.TrimSpace(s), ";")
}

func (p *parser) parseStatement(lineNo int, s string) {
	switch {
	case strings.HasPrefix(s, "VERSION"):
		if !versionRe.MatchString(s) {
			p.errf(lineNo, ReasonSyntax, `malformed VERSION statement (expect VERSION "...")`)
		}
	case s == "NS_ :":
		p.inNS = true
	case strings.HasPrefix(s, "BS_"):
		t := strings.TrimSpace(s)
		if t != "BS_:;" && t != "BS_ :" && t != "BS_:" {
			p.errf(lineNo, ReasonSyntax, "malformed BS_ statement")
		}
	case strings.HasPrefix(s, "BU_"):
		body := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(strings.TrimPrefix(s, "BU_")), ";"))
		body = strings.TrimPrefix(body, ":")
		if strings.TrimSpace(body) == "" {
			p.errf(lineNo, ReasonSyntax, "malformed BU_ statement (expect BU_: <nodes>)")
		}
	case strings.HasPrefix(s, "BO_ "):
		p.parseBO(lineNo, strings.TrimSuffix(s, ";"))
	case strings.HasPrefix(s, "SG_ "):
		if p.current == nil {
			p.errf(lineNo, ReasonOrphanSignal, "SG_ without preceding BO_")
			return
		}
		p.parseSG(lineNo, strings.TrimSpace(strings.TrimPrefix(s, "SG_")))
	case strings.HasPrefix(s, "CM_"):
		p.parseCM(lineNo, strings.TrimSuffix(s, ";"))
	case strings.HasPrefix(s, "VAL_ "):
		p.parseVAL(lineNo, strings.TrimSuffix(s, ";"))
	case strings.HasPrefix(s, "VAL_TABLE_"):
		p.parseValTable(lineNo, strings.TrimSuffix(s, ";"))
	case strings.HasPrefix(s, "SIG_VALTYPE_"):
		p.parseSigValType(lineNo, strings.TrimSuffix(s, ";"))
	case isKnownPassThrough(s):
		// 已知但本子集不使用内容的语句：明确记录为忽略（内容不参与解码）。
		p.ignored = append(p.ignored, IgnoredItem{Line: lineNo, Kind: strings.SplitN(s, " ", 2)[0],
			Why: "statement recognized as non-decoding metadata; content does not affect frame decoding"})
	default:
		p.errf(lineNo, ReasonUnknownStatement, "unsupported or malformed statement: %q", truncate(s, 80))
	}
}

func isKnownPassThrough(s string) bool {
	prefixes := []string{
		"SIG_GROUP_", "BA_ ", "BA_DEF_", "BA_DEF_DEF_", "BO_TX_BU_", "EV_ ",
		"ENVVAR_DATA_", "SGT_REL_", "SIG_TYPE_REF_", "BA_DEF_REL_", "BA_REL_",
		"BA_DEF_SGTYPE_", "BA_SGTYPE_", "CAT_DEF_", "CAT_", "FILTER", "NS_DESC_",
	}
	for _, pre := range prefixes {
		if strings.HasPrefix(s, pre) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func (p *parser) parseBO(lineNo int, s string) {
	m := boRe.FindStringSubmatch(s)
	if m == nil {
		p.errf(lineNo, ReasonSyntax, "malformed BO_ (expect: BO_ <id> <Name>: <DLC> <Sender>)")
		p.current = nil
		return
	}
	id, _ := strconv.ParseUint(m[1], 10, 32)
	dlc, _ := strconv.Atoi(m[3])
	extended := false
	if id&0x80000000 != 0 {
		// Vector 扩展帧写法：高位置 1，低 29 位为真实 ID。
		id &^= 0x80000000
		extended = true
	}
	if id > 0x1FFFFFFF {
		p.errf(lineNo, ReasonSyntax, "message id %d exceeds 29-bit CAN ID", id)
	}
	if dlc < 1 || dlc > 8 {
		p.errf(lineNo, ReasonBadLength, "message DLC must be in 1..8, got %d", dlc)
	}
	if _, dup := p.msgs[uint32(id)]; dup {
		p.errf(lineNo, ReasonDuplicateMessage, "duplicate message id %d", id)
	}
	if other, dup := p.names[m[2]]; dup {
		p.errf(lineNo, ReasonDuplicateMessage, "message name %q already used by id %d", m[2], other)
	}
	msg := &model.Message{ID: uint32(id), Name: m[2], DLC: dlc, Sender: m[4], Extended: extended}
	p.msgs[msg.ID] = msg
	p.names[msg.Name] = msg.ID
	p.current = msg
}

func (p *parser) parseSG(lineNo int, s string) {
	m := sgRe.FindStringSubmatch(s)
	if m == nil {
		p.errf(lineNo, ReasonSyntax, "malformed SG_ (expect: SG_ Name [mux] : start|len@order+/- (factor,offset) [min,max] \"unit\" receivers)")
		return
	}
	name := m[1]
	start, _ := strconv.Atoi(m[3])
	length, _ := strconv.Atoi(m[4])
	order := model.Intel
	if m[5] == "0" {
		order = model.Motorola
	}
	factor, _ := strconv.ParseFloat(m[7], 64)
	offset, _ := strconv.ParseFloat(m[8], 64)
	minV, errMin := strconv.ParseFloat(m[9], 64)
	maxV, errMax := strconv.ParseFloat(m[10], 64)
	if errMin != nil || errMax != nil {
		p.sigErr(lineNo, name, ReasonSyntax, "non-numeric min/max")
		return
	}

	if p.current.HasSignal(name) {
		p.sigErr(lineNo, name, ReasonDuplicateSignal, "duplicate signal in message %q", p.current.Name)
	}

	sig := &model.Signal{
		Line:      lineNo,
		Name:      name,
		Order:     order,
		IsSigned:  m[6] == "-",
		StartBit:  start,
		Length:    length,
		Factor:    factor,
		Offset:    offset,
		Min:       minV,
		Max:       maxV,
		Unit:      m[11],
		MuxIndex:  0,
		MuxSwitch: nil,
		Receivers: splitReceivers(m[12]),
	}
	if len(sig.Receivers) == 0 {
		p.ignored = append(p.ignored, IgnoredItem{Line: lineNo, Kind: "EMPTY_RECEIVERS",
			Subject: p.current.Name + "." + name, Why: "signal with no receiver nodes is still decoded; receiver list is informational"})
	}
	muxTok := strings.TrimSpace(m[2])
	switch {
	case muxTok == "":
		// 普通信号
	case muxTok == "M":
		sig.MuxIndex = -1
	case muxBoth.MatchString(muxTok):
		// 扩展复用（mNM）在本子集中不支持，明确拒绝而不是按普通选择器误解。
		p.sigErr(lineNo, name, ReasonBadMultiplex, "extended multiplexing (mNM) is not supported by this subset")
		return
	default:
		mm := muxSimple.FindStringSubmatch(muxTok)
		if mm == nil {
			p.sigErr(lineNo, name, ReasonSyntax, "malformed multiplex indicator %q", muxTok)
			return
		}
		v, _ := strconv.Atoi(mm[1])
		if v < 0 {
			p.sigErr(lineNo, name, ReasonBadMultiplex, "multiplex switch value out of range: %d", v)
			return
		}
		sig.MuxSwitch = &v
	}
	p.current.AddSignal(sig)
}

func splitReceivers(s string) []string {
	fields := strings.Fields(strings.TrimSpace(s))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, strings.TrimSuffix(f, ","))
	}
	return out
}

func parseLeadingID(s string) (uint64, string, bool) {
	s = strings.TrimSpace(s)
	end := strings.IndexAny(s, " ,\t")
	if end < 0 {
		end = len(s)
	}
	id, err := strconv.ParseUint(s[:end], 10, 64)
	if err != nil {
		return 0, "", false
	}
	return id, strings.TrimSpace(s[end:]), true
}

// CM_ 注释：仅支持单行形式 CM_ BO_ id "..."; / CM_ SG_ id sigName "..."; / CM_ "...";
func (p *parser) parseCM(lineNo int, s string) {
	body := strings.TrimSpace(strings.TrimPrefix(s, "CM_"))
	switch {
	case strings.HasPrefix(body, "BO_"):
		rest := strings.TrimSpace(strings.TrimPrefix(body, "BO_"))
		id, _, ok := parseLeadingID(rest)
		id &^= 0x80000000
		if !ok || p.msgs[uint32(id)] == nil {
			p.errf(lineNo, ReasonDanglingReference, "CM_ BO_ references unknown message: %q", truncate(rest, 40))
		}
	case strings.HasPrefix(body, "SG_"):
		rest := strings.TrimSpace(strings.TrimPrefix(body, "SG_"))
		id, after, ok := parseLeadingID(rest)
		id &^= 0x80000000
		if !ok {
			p.errf(lineNo, ReasonSyntax, "malformed CM_ SG_")
			return
		}
		fields := strings.FieldsFunc(after, func(r rune) bool { return r == ' ' || r == ',' })
		if len(fields) == 0 {
			p.errf(lineNo, ReasonSyntax, "malformed CM_ SG_: missing signal name")
			return
		}
		msg := p.msgs[uint32(id)]
		if msg == nil {
			p.errf(lineNo, ReasonDanglingReference, "CM_ SG_ references unknown message id %d", id)
			return
		}
		if _, ok := msg.SignalByName(fields[0]); !ok {
			p.sigErrIn(msg, lineNo, fields[0], ReasonDanglingReference, "CM_ SG_ references unknown signal in message %d", id)
		}
	case strings.HasPrefix(body, `"`):
		// 全局注释，容忍
	default:
		p.errf(lineNo, ReasonSyntax, "malformed CM_ statement (only single-line BO_/SG_/global comments supported)")
	}
}

// parseValuePairs 解析 "v \"label\" v \"label\" ..." 序列（容忍反斜杠转义与断行空白）。
func parseValuePairs(after string) (map[int64]string, bool) {
	idx := valPairRe.FindAllStringSubmatchIndex(after, -1)
	if len(idx) == 0 {
		return nil, false
	}
	out := make(map[int64]string, len(idx))
	for _, m := range idx {
		v, _ := strconv.ParseInt(after[m[2]:m[3]], 10, 64)
		label := unquoteDBC(after[m[4]:m[5]])
		out[v] = label
	}
	return out, true
}

func unquoteDBC(s string) string {
	if uq, err := strconv.Unquote(`"` + s + `"`); err == nil {
		return uq
	}
	return s
}

// VAL_TABLE_ Name v "label" ...;  文件级具名数值表，真正参与解码（枚举标签）。
func (p *parser) parseValTable(lineNo int, s string) {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), ";"))
	m := valTableRe.FindStringSubmatch(strings.TrimSpace(strings.TrimPrefix(body, "VAL_TABLE_")))
	if m == nil {
		p.errf(lineNo, ReasonSyntax, "malformed VAL_TABLE_ statement")
		return
	}
	name := m[1]
	if _, dup := p.tables[name]; dup {
		p.errf(lineNo, ReasonDuplicateValueTable, "duplicate value table %q", name)
		return
	}
	pairs, ok := parseValuePairs(m[2])
	if !ok {
		p.errf(lineNo, ReasonSyntax, "malformed VAL_TABLE_ %q: need at least one value/label pair", name)
		return
	}
	p.tables[name] = &model.ValueTable{Name: name, Labels: pairs}
}

// VAL_ 取值表：支持两种等价写法（解析结果一致）：
//
//	VAL_ <id> <Signal> <v> "label" ...;   // 内联枚举
//	VAL_ <id> <Signal> TableName;         // 引用具名 VAL_TABLE_
func (p *parser) parseVAL(lineNo int, s string) {
	body := strings.TrimSpace(strings.TrimPrefix(s, "VAL_"))
	id, after, ok := parseLeadingID(body)
	if id&0x80000000 != 0 {
		id &^= 0x80000000
	}
	if !ok {
		p.errf(lineNo, ReasonSyntax, "malformed VAL_ statement")
		return
	}
	fields := strings.Fields(after)
	if len(fields) < 2 {
		p.errf(lineNo, ReasonSyntax, "malformed VAL_ statement: need signal name and table/pairs")
		return
	}
	msg := p.msgs[uint32(id)]
	if msg == nil {
		p.errf(lineNo, ReasonDanglingReference, "VAL_ references unknown message id %d", id)
		return
	}
	sig, ok := msg.SignalByName(fields[0])
	if !ok {
		p.sigErrIn(msg, lineNo, fields[0], ReasonDanglingReference, "VAL_ references unknown signal in message %d", id)
		return
	}
	rest := strings.TrimSpace(strings.TrimPrefix(after, fields[0]))
	if !strings.Contains(rest, `"`) {
		// 具名表引用：表可能在文件后段才定义，登记待解析。
		name := strings.TrimSpace(strings.TrimSuffix(rest, ";"))
		p.pendingInline = append(p.pendingInline, pendingInline{line: lineNo, msgID: uint32(id), sigName: sig.Name, tableName: name})
		return
	}
	pairs, ok := parseValuePairs(rest)
	if !ok {
		p.errf(lineNo, ReasonSyntax, "malformed VAL_ inline pairs for %s.%s", msg.Name, sig.Name)
		return
	}
	if sig.ValueTable != nil {
		p.errf(lineNo, ReasonDuplicateValueTable, "signal %s.%s already has a value table", msg.Name, sig.Name)
		return
	}
	sig.ValueTable = &model.ValueTable{Labels: pairs}
}

// resolveInlineTables 在所有语句收集完后，把具名表引用绑定到信号。
func (p *parser) resolveInlineTables() {
	for _, ref := range p.pendingInline {
		tbl := p.tables[ref.tableName]
		if tbl == nil {
			e := &DefError{Reason: ReasonDanglingReference, Line: ref.line,
				MsgID: ref.msgID, SigName: ref.sigName,
				Message: fmt.Sprintf("VAL_ references undefined value table %q", ref.tableName)}
			if msg := p.msgs[ref.msgID]; msg != nil {
				e.MsgName = msg.Name
			}
			p.errors = append(p.errors, e)
			continue
		}
		msg := p.msgs[ref.msgID]
		sig, _ := msg.SignalByName(ref.sigName)
		if sig.ValueTable != nil {
			p.sigErrIn(msg, ref.line, ref.sigName, ReasonDuplicateValueTable,
				"signal %s.%s already has a value table", msg.Name, ref.sigName)
			continue
		}
		sig.ValueTable = tbl
	}
}

// SIG_VALTYPE_ <id> <SignalName> : <type>;  仅允许 0（整型）；1/2 浮点信号明确拒绝。
func (p *parser) parseSigValType(lineNo int, s string) {
	m := sgValRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		p.errf(lineNo, ReasonSyntax, "malformed SIG_VALTYPE_ statement")
		return
	}
	id, _ := strconv.ParseUint(m[1], 10, 32)
	if id&0x80000000 != 0 {
		id &^= 0x80000000
	}
	t, _ := strconv.Atoi(m[3])
	msg := p.msgs[uint32(id)]
	if msg == nil {
		p.errf(lineNo, ReasonDanglingReference, "SIG_VALTYPE_ references unknown message id %d", id)
		return
	}
	sig, ok := msg.SignalByName(m[2])
	if !ok {
		p.sigErrIn(msg, lineNo, m[2], ReasonDanglingReference, "SIG_VALTYPE_ references unknown signal in message %d", id)
		return
	}
	if t == 0 {
		p.ignored = append(p.ignored, IgnoredItem{Line: lineNo, Kind: "SIG_VALTYPE_",
			Subject: msg.Name + "." + sig.Name, Why: "type 0 (integer) is the default and affects nothing"})
		return
	}
	// 1=float32、2=float64 会改变原始位的解释方式，属于影响解码但不支持的构造，明确拒绝。
	p.sigErrIn(msg, lineNo, sig.Name, ReasonBadFloatType, "float/double signals (SIG_VALTYPE_ type %d) are not supported", t)
}

func (p *parser) buildSummary() *Summary {
	s := &Summary{Ignored: append([]IgnoredItem(nil), p.ignored...)}
	ids := make([]uint32, 0, len(p.msgs))
	for id := range p.msgs {
		ids = append(ids, id)
	}
	sortU32(ids)
	for _, id := range ids {
		msg := p.msgs[id]
		am := AdoptedMessage{MsgID: id, MsgName: msg.Name, DLC: msg.DLC}
		for _, sig := range msg.Signals {
			as := AdoptedSignal{Name: sig.Name, Length: sig.Length, Order: sig.Order.String()}
			switch {
			case sig.IsMuxMultiplexor():
				as.Mux = "M"
			case sig.MuxSwitch != nil:
				as.Mux = "m" + strconv.Itoa(*sig.MuxSwitch)
			}
			if sig.ValueTable != nil {
				as.ValueTable = sig.ValueTable.Name
				if as.ValueTable == "" {
					as.ValueTable = "(inline)"
				}
			}
			am.Signals = append(am.Signals, as)
		}
		s.Adopted = append(s.Adopted, am)
	}
	return s
}

func sortU32(v []uint32) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j-1] > v[j]; j-- {
			v[j-1], v[j] = v[j], v[j-1]
		}
	}
}
