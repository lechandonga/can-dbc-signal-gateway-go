package dbc

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
)

// ParseFile parses a DBC definition file. Parsing is all-or-nothing: on the
// first structural problem no partial database is returned.
func ParseFile(path string) (*Database, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f, path)
}

// Parse parses DBC content from r. source labels the database in errors/logs.
func Parse(r io.Reader, source string) (*Database, error) {
	p := &parser{
		db:      &Database{Messages: map[uint32]*Message{}, Source: source},
		enumTab: map[string]map[int64]string{},
		pending: map[uint32]map[string]map[int64]string{},
		valType: map[string]int{},
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		p.lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		if p.skipNS {
			// Body ends at ';' or at the next top-level keyword (some DBC
			// files omit the semicolon). Either way this line is body.
			if strings.HasSuffix(line, ";") {
				p.skipNS = false
				continue
			}
			if allowedSections[firstToken(line)] && firstToken(line) != "NS_" {
				p.skipNS = false
			} else if kw := strings.TrimSuffix(firstToken(line), ":"); allowedSections[kw] && kw != "NS_" {
				p.skipNS = false
			} else {
				continue
			}
		}
		if err := p.line(line); err != nil {
			return nil, err
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if err := p.finish(); err != nil {
		return nil, err
	}
	return p.db, nil
}

type parser struct {
	db     *Database
	lineNo int
	cur    *Message // message currently receiving SG_ lines
	skipNS bool     // inside the NS_ free-form list until ';'

	enumTab map[string]map[int64]string            // VAL_TABLE_ tables
	pending map[uint32]map[string]map[int64]string // id -> signal -> enum (VAL_)
	valType map[string]int                         // "id Name" -> SIG_VALTYPE_
}

// allowedSections lists DBC keywords recognised by this parser. Unknown
// keywords are rejected rather than silently skipped.
var allowedSections = map[string]bool{
	"VERSION": true, "NS_": true, "BS_": true, "BU_": true,
	"BO_": true, "SG_": true, "CM_": true, "BA_DEF_": true,
	"BA_DEF_REL_": true, "BA_": true, "BA_REL_": true,
	"VAL_TABLE_": true, "VAL_": true, "SIG_VALTYPE_": true,
	"BO_TX_BU_": true, "EV_": true, "ENVVAR_DATA_": true,
	"SGTYPE_": true, "SGTYPE_VAL_": true, "SIG_GROUP_": true,
	"CAT_DEF_": true, "CAT_": true, "FILTER": true,
	"BA_DEF_SGTYPE_": true, "BA_SGTYPE_": true, "SIG_TYPE_REF_": true,
	"VALTYPE_": true, "SIGTYPE_VALTYPE_": true,
	"BO_BO_REL_": true, "BU_SG_REL_": true, "BU_EV_REL_": true,
	"BU_BO_REL_": true, "SG_MUL_VAL_": true,
}

// ignoredSections are recognized DBC statements that carry metadata this
// subset does not model; accepting them is well-defined behavior.
var ignoredSections = map[string]bool{
	"VERSION": true, "NS_": true, "BS_": true, "BU_": true,
	"CM_": true, "BA_DEF_": true, "BA_DEF_REL_": true, "BA_": true,
	"BA_REL_": true, "BO_TX_BU_": true, "EV_": true, "ENVVAR_DATA_": true,
	"SGTYPE_": true, "SGTYPE_VAL_": true, "SIG_GROUP_": true,
	"CAT_DEF_": true, "CAT_": true, "FILTER": true,
	"BA_DEF_SGTYPE_": true, "BA_SGTYPE_": true, "SIG_TYPE_REF_": true,
	"VALTYPE_": true, "SIGTYPE_VALTYPE_": true,
	"BO_BO_REL_": true, "BU_SG_REL_": true, "BU_EV_REL_": true,
	"BU_BO_REL_": true,
}

func (p *parser) line(line string) error {
	keyword := firstToken(line)
	if !allowedSections[keyword] {
		return errf(p.lineNo, "", "", ReasonSyntax,
			"unrecognized DBC statement %q", keyword)
	}
	switch keyword {
	case "NS_":
		// Header like "NS_ :" or "NS_ : NS_DESC_ ... ;". Skip following
		// lines only when the body does not terminate on the header line.
		if !strings.HasSuffix(strings.TrimSpace(line), ";") {
			p.skipNS = true
		}
		return nil
	case "SG_MUL_VAL_":
		return errf(p.lineNo, "", "", ReasonUnsupported,
			"extended multiplexing (SG_MUL_VAL_) is outside the supported subset")
	case "BO_":
		return p.parseBO(line)
	case "SG_":
		return p.parseSG(line)
	case "VAL_TABLE_":
		return p.parseValTable(line)
	case "VAL_":
		return p.parseVal(line)
	case "SIG_VALTYPE_":
		return p.parseSigValType(line)
	default:
		if ignoredSections[keyword] {
			return nil
		}
		return errf(p.lineNo, "", "", ReasonSyntax,
			"unrecognized DBC statement %q", keyword)
	}
}

func firstToken(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] == ' ' || line[i] == '\t' {
			return strings.TrimRight(line[:i], ":")
		}
	}
	return strings.TrimRight(line, ":")
}

// BO_ 123 EngineData: 8 Engine
func (p *parser) parseBO(line string) error {
	body := strings.TrimSpace(strings.TrimPrefix(line, "BO_"))
	colon := strings.IndexByte(body, ':')
	if colon < 0 {
		return errf(p.lineNo, "", "", ReasonSyntax, "BO_ line missing ':'")
	}
	left := strings.Fields(body[:colon])
	right := strings.Fields(body[colon+1:])
	if len(left) != 2 || len(right) != 2 {
		return errf(p.lineNo, "", "", ReasonSyntax,
			"BO_ expects \"ID Name: DLC Sender\", got %q", line)
	}
	id, err := strconv.ParseUint(left[0], 0, 32)
	if err != nil {
		return errf(p.lineNo, "", "", ReasonSyntax, "invalid message id %q", left[0])
	}
	if id > 0x1FFFFFFF {
		return errf(p.lineNo, left[1], "", ReasonInvalidSignal,
			"message id %d exceeds the 29-bit CAN ID space", id)
	}
	name := strings.TrimSuffix(left[1], ":")
	dlc, err := strconv.Atoi(right[0])
	if err != nil || dlc < 0 || dlc > 8 {
		return errf(p.lineNo, name, "", ReasonInvalidSignal,
			"invalid DLC %q (0..8 expected)", right[0])
	}
	if _, exists := p.db.Messages[uint32(id)]; exists {
		return errf(p.lineNo, name, "", ReasonDuplicate,
			"duplicate message id %d", id)
	}
	for _, m := range p.db.Messages {
		if m.Name == name {
			return errf(p.lineNo, name, "", ReasonDuplicate,
				"duplicate message name %q", name)
		}
	}
	m := &Message{
		ID:        uint32(id),
		Name:      name,
		DLC:       dlc,
		Sender:    right[1],
		signalMap: map[string]*Signal{},
	}
	p.db.Messages[m.ID] = m
	p.cur = m
	return nil
}
