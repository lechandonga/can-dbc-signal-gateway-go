package dbc

import (
	"strconv"
	"strings"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

func tokenizeValuePairs(s string) ([]string, bool) {
	var tokens []string
	var cur strings.Builder
	inString := false
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch == '"':
			inString = !inString
			cur.WriteByte(ch)
		case inString:
			cur.WriteByte(ch)
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' || ch == ',':
			flush()
		default:
			cur.WriteByte(ch)
		}
	}
	flush()
	return tokens, !inString
}

func unquoteDBC(s string) (string, bool) {
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return "", false
	}
	return s[1 : len(s)-1], true
}

func parsePairs(tokens []string) (map[uint64]string, bool) {
	if len(tokens) == 1 {
		return nil, true
	}
	if len(tokens)%2 != 0 {
		return nil, false
	}
	out := map[uint64]string{}
	for i := 0; i < len(tokens); i += 2 {
		raw, err := strconv.ParseUint(tokens[i], 10, 64)
		if err != nil {
			return nil, false
		}
		label, ok := unquoteDBC(tokens[i+1])
		if !ok {
			return nil, false
		}
		out[raw] = label
	}
	return out, true
}

func (p *parser) parseVALTable(lineNo int, s string) {
	body := strings.TrimSpace(strings.TrimPrefix(s, "VAL_TABLE_"))
	fields := strings.Fields(body)
	if len(fields) < 1 {
		p.errf(lineNo, ReasonSyntax, "malformed VAL_TABLE_ statement")
		return
	}
	name := strings.TrimSuffix(fields[0], ":")
	tokens, ok := tokenizeValuePairs(strings.TrimSpace(strings.TrimPrefix(body, fields[0])))
	if !ok {
		p.errf(lineNo, ReasonSyntax, "unterminated string in VAL_TABLE_ %s", name)
		return
	}
	values, ok := parsePairs(tokens)
	if !ok {
		p.errf(lineNo, ReasonSyntax, "malformed value/label pairs in VAL_TABLE_ %s", name)
		return
	}
	if _, exists := p.valueTables[name]; exists {
		p.errf(lineNo, ReasonDuplicateMessage, "duplicate value table %q", name)
		return
	}
	p.valueTables[name] = values
}

func (p *parser) parseValueBinding(lineNo int, s string) {
	body := strings.TrimSpace(strings.TrimPrefix(s, "VAL_"))
	idText, rest, ok := parseLeadingID(body)
	if !ok {
		p.errf(lineNo, ReasonSyntax, "malformed VAL_ statement")
		return
	}
	msg := p.msgs[uint32(idText)]
	if msg == nil {
		p.errf(lineNo, ReasonDanglingReference, "VAL_ references unknown message id %d", idText)
		return
	}
	tokens, quoted := tokenizeValuePairs(rest)
	if !quoted || len(tokens) == 0 {
		p.errf(lineNo, ReasonSyntax, "malformed VAL_ statement")
		return
	}
	signalName := tokens[0]
	sig, exists := msg.SignalByName(signalName)
	if !exists {
		p.sigErrIn(msg, lineNo, signalName, ReasonDanglingReference, "VAL_ references unknown signal in message %d", idText)
		return
	}
	values, ok := parsePairs(tokens[1:])
	if !ok {
		p.sigErrIn(msg, lineNo, signalName, ReasonSyntax, "malformed VAL_ value/label pairs")
		return
	}
	pending := pendingVAL{line: lineNo, msg: msg, signal: signalName}
	if len(values) == 0 {
		pending.tableName = tokens[1]
	} else {
		pending.entries = values
	}
	p.pendingVAL = append(p.pendingVAL, pending)
	_ = sig
}

func (p *parser) resolveValueReferences() {
	for _, pending := range p.pendingVAL {
		sig, _ := pending.msg.SignalByName(pending.signal)
		values := pending.entries
		if values == nil {
			ref, ok := p.valueTables[pending.tableName]
			if !ok {
				p.sigErrIn(pending.msg, pending.line, pending.signal, ReasonDanglingReference,
					"VAL_ references unknown value table %q", pending.tableName)
				continue
			}
			values = cloneValues(ref)
		}
		sig.ValueLabels = cloneValues(values)
	}
}

func cloneValues(in map[uint64]string) map[uint64]string {
	out := make(map[uint64]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// LabelFor 返回信号原始值对应标签；未收录时稳定返回 ("", false)。
func LabelFor(sig *model.Signal, raw uint64) (string, bool) {
	if sig == nil || sig.ValueLabels == nil {
		return "", false
	}
	label, ok := sig.ValueLabels[raw]
	return label, ok
}
