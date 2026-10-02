package dbc

import (
	"strconv"
	"strings"
)

// VAL_TABLE_ StatusTable 0 "Off" 1 "On" ;
func (p *parser) parseValTable(line string) error {
	fields, err := tokenizeEnumLine(line)
	if err != nil {
		return errf(p.lineNo, "", "", ReasonSyntax, "%v", err)
	}
	if len(fields) < 2 {
		return errf(p.lineNo, "", "", ReasonSyntax,
			"VAL_TABLE_ expects a table name and entries")
	}
	name := fields[0]
	if _, exists := p.enumTab[name]; exists {
		return errf(p.lineNo, "", "", ReasonDuplicate,
			"duplicate VAL_TABLE_ name %q", name)
	}
	tab, err := buildEnum(fields[1:], p.lineNo, "", "")
	if err != nil {
		return err
	}
	p.enumTab[name] = tab
	return nil
}

// VAL_ 123 Switch 0 "Idle" 1 "Run" ;
func (p *parser) parseVal(line string) error {
	fields, err := tokenizeEnumLine(line)
	if err != nil {
		return errf(p.lineNo, "", "", ReasonSyntax, "%v", err)
	}
	if len(fields) < 4 {
		return errf(p.lineNo, "", "", ReasonSyntax,
			"VAL_ expects \"ID Signal value \"label\" ... ;\"")
	}
	id, err := strconv.ParseUint(fields[0], 0, 32)
	if err != nil {
		return errf(p.lineNo, "", "", ReasonSyntax, "invalid message id %q", fields[0])
	}
	sigName := fields[1]
	tab, err := buildEnum(fields[2:], p.lineNo, "", sigName)
	if err != nil {
		return err
	}
	if p.pending[uint32(id)] == nil {
		p.pending[uint32(id)] = map[string]map[int64]string{}
	}
	if _, dup := p.pending[uint32(id)][sigName]; dup {
		return errf(p.lineNo, "", sigName, ReasonDuplicate,
			"duplicate VAL_ definition")
	}
	p.pending[uint32(id)][sigName] = tab
	return nil
}

// SIG_VALTYPE_ 123 FloatSignal : 1;
func (p *parser) parseSigValType(line string) error {
	body := strings.TrimSpace(strings.TrimPrefix(line, "SIG_VALTYPE_"))
	body = strings.TrimSuffix(strings.TrimSpace(body), ";")
	colon := strings.IndexByte(body, ':')
	if colon < 0 {
		return errf(p.lineNo, "", "", ReasonSyntax, "SIG_VALTYPE_ missing ':'")
	}
	left := strings.Fields(body[:colon])
	right := strings.Fields(body[colon+1:])
	if len(left) != 2 || len(right) != 1 {
		return errf(p.lineNo, "", "", ReasonSyntax,
			"SIG_VALTYPE_ expects \"ID Name : N;\"")
	}
	id, err := strconv.ParseUint(left[0], 0, 32)
	if err != nil {
		return errf(p.lineNo, "", left[1], ReasonSyntax, "invalid message id %q", left[0])
	}
	n, err := strconv.Atoi(right[0])
	if err != nil {
		return errf(p.lineNo, "", left[1], ReasonSyntax, "invalid value type %q", right[0])
	}
	switch n {
	case 0: // integer, ordinary signal; nothing to remember
	case 1, 2:
		return errf(p.lineNo, "", left[1], ReasonUnsupported,
			"float signals (SIG_VALTYPE_ %d) are outside the supported subset", n)
	default:
		return errf(p.lineNo, "", left[1], ReasonInvalidSignal,
			"SIG_VALTYPE_ value %d is not 0/1/2", n)
	}
	p.valType[strconv.FormatUint(id, 10)+" "+left[1]] = n
	return nil
}

// tokenizeEnumLine splits a VAL_/VAL_TABLE_ style line into raw tokens while
// keeping quoted strings intact and verifying the trailing semicolon.
func tokenizeEnumLine(line string) ([]string, error) {
	body := strings.TrimSpace(line[strings.IndexByte(line, ' '):])
	if !strings.HasSuffix(body, ";") {
		return nil, syntaxErrorString("enum line must end with ';'")
	}
	body = strings.TrimSuffix(body, ";")
	var out []string
	for i := 0; i < len(body); {
		for i < len(body) && (body[i] == ' ' || body[i] == '\t') {
			i++
		}
		if i >= len(body) {
			break
		}
		if body[i] == '"' {
			end := strings.IndexByte(body[i+1:], '"')
			if end < 0 {
				return nil, syntaxErrorString("unterminated quoted label")
			}
			out = append(out, body[i:i+end+2])
			i += end + 2
			continue
		}
		start := i
		for i < len(body) && body[i] != ' ' && body[i] != '\t' {
			i++
		}
		out = append(out, body[start:i])
	}
	return out, nil
}

func buildEnum(fields []string, line int, msg, sig string) (map[int64]string, error) {
	if len(fields)%2 != 0 {
		return nil, errf(line, msg, sig, ReasonSyntax,
			"enum entries must be value/label pairs")
	}
	tab := map[int64]string{}
	for i := 0; i < len(fields); i += 2 {
		v, err := strconv.ParseInt(fields[i], 0, 64)
		if err != nil {
			return nil, errf(line, msg, sig, ReasonSyntax,
				"invalid enum value %q", fields[i])
		}
		label := fields[i+1]
		if len(label) < 2 || label[0] != '"' || label[len(label)-1] != '"' {
			return nil, errf(line, msg, sig, ReasonSyntax,
				"enum label %q must be quoted", label)
		}
		if _, dup := tab[v]; dup {
			return nil, errf(line, msg, sig, ReasonDuplicate,
				"duplicate enum value %d", v)
		}
		tab[v] = label[1 : len(label)-1]
	}
	return tab, nil
}
