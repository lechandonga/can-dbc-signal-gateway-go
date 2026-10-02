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
	boRe      = regexp.MustCompile(`^BO_\s+(\d+)\s+([A-Za-z_][A-Za-z0-9_]*):\s+(\d+)\s+(\S+)\s*$`)
	sgRe      = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)(?:\s+(M|m\d+M?))?\s*:\s+(\d+)\|(\d+)@([01])([+-])\s+\(([^,]*),([^)]*)\)\s+\[([^,]*),([^\]]*)\]\s+"([^"]*)"\s+(.+)$`)
	muxSimple = regexp.MustCompile(`^m(\d+)$`)
	muxBoth   = regexp.MustCompile(`^m(\d+)M$`)
)

// Parse 解析 DBC 内容，成功返回不可变的 model.Database。
// 失败时返回 ErrorList，包含全部逐条错误（含行号与报文/信号定位）。
func Parse(r io.Reader) (*model.Database, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, ErrorList{{Reason: ReasonSyntax, Message: err.Error()}}
	}
	p := &parser{
		msgs:       map[uint32]*model.Message{},
		names:      map[string]uint32{},
		sigValType: map[uint32]map[string]int{},
	}
	p.parseLines(strings.Split(string(data), "\n"))
	if len(p.errors) == 0 {
		p.validate()
	}
	if len(p.errors) > 0 {
		return nil, ErrorList(p.errors)
	}
	if len(p.msgs) == 0 {
		p.errf(0, ReasonIncomplete, "DBC defines no messages")
		return nil, ErrorList(p.errors)
	}
	return &model.Database{Messages: p.msgs, Names: p.names}, nil
}

type parser struct {
	msgs       map[uint32]*model.Message
	names      map[string]uint32
	errors     []*DefError
	current    *model.Message
	inNS       bool
	sigValType map[uint32]map[string]int
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
	case strings.HasPrefix(s, "SIG_VALTYPE_"):
		p.parseSigValType(lineNo, strings.TrimSuffix(s, ";"))
	case isKnownPassThrough(s):
		// 已知但本子集不使用内容的语句：容忍。
	default:
		p.errf(lineNo, ReasonUnknownStatement, "unsupported or malformed statement: %q", truncate(s, 80))
	}
}

func isKnownPassThrough(s string) bool {
	prefixes := []string{
		"SIG_GROUP_", "BA_ ", "BA_DEF_", "VAL_TABLE_", "BO_TX_BU_", "EV_ ",
		"ENVVAR_DATA_", "SGT_REL_", "SIG_TYPE_REF_", "BA_DEF_REL_", "BA_REL_",
		"CAT_DEF_", "CAT_", "FILTER", "NS_DESC_",
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
	msg := &model.Message{ID: uint32(id), Name: m[2], DLC: dlc, Sender: m[4]}
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
	id, err := strconv.ParseUint(s[:end], 10, 32)
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
		if !ok || p.msgs[uint32(id)] == nil {
			p.errf(lineNo, ReasonDanglingReference, "CM_ BO_ references unknown message: %q", truncate(rest, 40))
		}
	case strings.HasPrefix(body, "SG_"):
		rest := strings.TrimSpace(strings.TrimPrefix(body, "SG_"))
		id, after, ok := parseLeadingID(rest)
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

// VAL_ 取值表引用：VAL_ <id> <SignalName> <v> "label" ...;
func (p *parser) parseVAL(lineNo int, s string) {
	body := strings.TrimSpace(strings.TrimPrefix(s, "VAL_"))
	id, after, ok := parseLeadingID(body)
	if !ok {
		p.errf(lineNo, ReasonSyntax, "malformed VAL_ statement")
		return
	}
	fields := strings.Fields(after)
	if len(fields) < 3 {
		p.errf(lineNo, ReasonSyntax, "malformed VAL_ statement: need signal name and at least one value/label pair")
		return
	}
	msg := p.msgs[uint32(id)]
	if msg == nil {
		p.errf(lineNo, ReasonDanglingReference, "VAL_ references unknown message id %d", id)
		return
	}
	if _, ok := msg.SignalByName(fields[0]); !ok {
		p.sigErrIn(msg, lineNo, fields[0], ReasonDanglingReference, "VAL_ references unknown signal in message %d", id)
	}
}

// SIG_VALTYPE_ <id> <SignalName> : <type>;  仅允许 0（整型）；1/2 浮点信号明确拒绝。
func (p *parser) parseSigValType(lineNo int, s string) {
	body := strings.TrimSpace(strings.TrimPrefix(s, "SIG_VALTYPE_"))
	parts := strings.Split(body, ":")
	if len(parts) != 2 {
		p.errf(lineNo, ReasonSyntax, "malformed SIG_VALTYPE_ statement")
		return
	}
	left := strings.Fields(parts[0])
	right := strings.Fields(strings.TrimSuffix(strings.TrimSpace(parts[1]), ";"))
	if len(left) != 2 || len(right) != 1 {
		p.errf(lineNo, ReasonSyntax, "malformed SIG_VALTYPE_ statement")
		return
	}
	t, err := strconv.Atoi(right[0])
	if err != nil || t < 0 || t > 3 {
		p.errf(lineNo, ReasonSyntax, "malformed SIG_VALTYPE_ type")
		return
	}
	id, convErr := strconv.ParseUint(left[0], 10, 32)
	if convErr != nil {
		p.errf(lineNo, ReasonSyntax, "malformed SIG_VALTYPE_: bad message id")
		return
	}
	msg := p.msgs[uint32(id)]
	if msg == nil {
		p.errf(lineNo, ReasonDanglingReference, "SIG_VALTYPE_ references unknown message id %d", id)
		return
	}
	if _, ok := msg.SignalByName(left[1]); !ok {
		p.sigErrIn(msg, lineNo, left[1], ReasonDanglingReference, "SIG_VALTYPE_ references unknown signal in message %d", id)
		return
	}
	if t != 0 {
		p.sigErrIn(msg, lineNo, left[1], ReasonBadFloatType, "float/double signals (SIG_VALTYPE_ type %d) are not supported", t)
	}
}
