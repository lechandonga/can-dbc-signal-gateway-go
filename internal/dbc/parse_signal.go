package dbc

import (
	"strconv"
	"strings"
)

// SG_ Speed : 0|8@1+ (1,0) [0|255] "km/h" Vector__xxx
// SG_ S1 M : 8|4@1+ (1,0) [0|3] "" Receiver
// SG_ PartA S0 : 0|8@1+ (1,0) [0|0] "" Receiver
func (p *parser) parseSG(line string) error {
	if p.cur == nil {
		return errf(p.lineNo, "", "", ReasonIncomplete,
			"SG_ line without a preceding BO_ message")
	}
	m := p.cur
	body := strings.TrimSpace(strings.TrimPrefix(line, "SG_"))
	colon := strings.IndexByte(body, ':')
	if colon < 0 {
		return errf(p.lineNo, m.Name, "", ReasonSyntax, "SG_ line missing ':'")
	}
	nameFields := strings.Fields(body[:colon])
	if len(nameFields) < 1 || len(nameFields) > 2 {
		return errf(p.lineNo, m.Name, "", ReasonSyntax,
			"SG_ expects \"Name [M|mN] : ...\", got %q", strings.TrimSpace(body[:colon]))
	}
	s := &Signal{Name: nameFields[0], Factor: 1}
	if len(nameFields) == 2 {
		tag := nameFields[1]
		switch {
		case tag == "M":
			s.IsSwitch = true
		case len(tag) >= 2 && tag[0] == 'm':
			n, err := strconv.Atoi(tag[1:])
			if err != nil || n < 0 {
				return errf(p.lineNo, m.Name, s.Name, ReasonSyntax,
					"invalid multiplexor tag %q (want M or mN with N>=0)", tag)
			}
			s.HasBranch = true
			s.Branch = n
		default:
			return errf(p.lineNo, m.Name, s.Name, ReasonSyntax,
				"invalid multiplexor tag %q (want M or mN)", tag)
		}
	}

	rhs := strings.Fields(body[colon+1:])
	// DBC writes layout and order fused, e.g. "0|16@1+"; split that token.
	if len(rhs) >= 1 {
		if at := strings.IndexByte(rhs[0], '@'); at > 0 {
			fused := rhs[0]
			rhs = append([]string{fused[:at], fused[at:]}, rhs[1:]...)
		}
	}
	if len(rhs) < 4 {
		return errf(p.lineNo, m.Name, s.Name, ReasonIncomplete,
			"signal definition is incomplete: %q", strings.TrimSpace(body[colon+1:]))
	}
	parseSteps := []func(string, *Signal) *Error{
		parseBitLayout, parseSignEndian, parseFactorOffset, parseMinMax,
	}
	for i, step := range parseSteps {
		if e := step(rhs[i], s); e != nil {
			e.Line, e.Message, e.Signal = p.lineNo, m.Name, s.Name
			return e
		}
	}

	rest := strings.TrimSpace(body[colon+1:])
	rest = strings.TrimSpace(body[colon+1:])
	rest = dropLeadingTokens(rest, rhs[:4])
	unit, afterUnit, err := extractQuoted(rest)
	if err != nil {
		return errf(p.lineNo, m.Name, s.Name, ReasonSyntax, "%v", err)
	}
	s.Unit = unit
	s.Receivers = strings.Fields(afterUnit)

	if s.Length > 64 {
		return errf(p.lineNo, m.Name, s.Name, ReasonInvalidSignal,
			"signal length %d exceeds 64 bits", s.Length)
	}
	if s.StartBit >= 64 {
		return errf(p.lineNo, m.Name, s.Name, ReasonInvalidSignal,
			"start bit %d exceeds 63", s.StartBit)
	}
	if s.Factor == 0 {
		return errf(p.lineNo, m.Name, s.Name, ReasonInvalidSignal,
			"factor must not be zero")
	}
	if s.Max < s.Min {
		return errf(p.lineNo, m.Name, s.Name, ReasonInvalidSignal,
			"max %v is smaller than min %v", s.Max, s.Min)
	}
	if _, dup := m.signalMap[s.Name]; dup {
		return errf(p.lineNo, m.Name, s.Name, ReasonDuplicate,
			"duplicate signal name %q in message", s.Name)
	}
	m.Signals = append(m.Signals, s)
	m.signalMap[s.Name] = s
	return nil
}

func dropLeadingTokens(s string, tokens []string) string {
	for _, t := range tokens {
		idx := strings.Index(s, t)
		if idx >= 0 {
			s = s[idx+len(t):]
		}
	}
	return strings.TrimSpace(s)
}

func parseBitLayout(tok string, s *Signal) *Error {
	parts := strings.Split(tok, "|")
	if len(parts) != 2 {
		return errf(0, "", "", ReasonSyntax, "bit layout %q must be start|length", tok)
	}
	start, err1 := strconv.Atoi(parts[0])
	length, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || start < 0 || length <= 0 {
		return errf(0, "", "", ReasonSyntax, "invalid bit layout %q", tok)
	}
	s.StartBit, s.Length = start, length
	return nil
}

func parseSignEndian(tok string, s *Signal) *Error {
	if len(tok) < 3 || tok[0] != '@' ||
		(tok[1] != '0' && tok[1] != '1') ||
		(tok[2] != '+' && tok[2] != '-') {
		return errf(0, "", "", ReasonSyntax,
			"invalid byte-order/sign token %q (want @1+, @1-, @0+ or @0-)", tok)
	}
	if tok[1] == '1' {
		s.Order = OrderIntel
	} else {
		s.Order = OrderMotorola
	}
	s.Signed = tok[2] == '-'
	return nil
}

func parseFactorOffset(tok string, s *Signal) *Error {
	in := strings.TrimPrefix(tok, "(")
	in = strings.TrimSuffix(in, ")")
	parts := strings.Split(in, ",")
	if len(parts) != 2 {
		return errf(0, "", "", ReasonSyntax,
			"gain/offset %q must be (factor,offset)", tok)
	}
	f, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	o, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil {
		return errf(0, "", "", ReasonSyntax, "invalid gain/offset %q", tok)
	}
	s.Factor, s.Offset = f, o
	return nil
}

func parseMinMax(tok string, s *Signal) *Error {
	in := strings.TrimPrefix(tok, "[")
	in = strings.TrimSuffix(in, "]")
	parts := strings.Split(in, "|")
	if len(parts) != 2 {
		return errf(0, "", "", ReasonSyntax,
			"range %q must be [min|max]", tok)
	}
	lo, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	hi, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err1 != nil || err2 != nil {
		return errf(0, "", "", ReasonSyntax, "invalid range %q", tok)
	}
	s.Min, s.Max = lo, hi
	return nil
}

// extractQuoted extracts the first "..." quoted token from the front of s and
// returns its content plus the remainder after the closing quote.
func extractQuoted(s string) (content, rest string, err error) {
	if !strings.HasPrefix(s, "\"") {
		return "", "", strconvSyntaxError("unit must be a quoted string")
	}
	end := strings.IndexByte(s[1:], '"')
	if end < 0 {
		return "", "", strconvSyntaxError("unterminated quoted string")
	}
	return s[1 : 1+end], strings.TrimSpace(s[1+end+1:]), nil
}

type syntaxErrorString string

func (e syntaxErrorString) Error() string { return string(e) }

func strconvSyntaxError(msg string) error { return syntaxErrorString(msg) }
