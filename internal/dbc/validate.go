package dbc

// finish performs whole-database validation after all lines are parsed.
func (p *parser) finish() error {
	// Attach VAL_ tables; dangling references are rejected, never dropped.
	for id, sigs := range p.pending {
		m, ok := p.db.Messages[id]
		if !ok {
			return errf(0, strconvID(id), "", ReasonIncomplete,
				"VAL_ references unknown message id %d", id)
		}
		for name, tab := range sigs {
			s := m.signalMap[name]
			if s == nil {
				return errf(0, m.Name, name, ReasonIncomplete,
					"VAL_ references unknown signal %q", name)
			}
			s.Enum = tab
		}
	}

	for _, m := range p.db.Messages {
		if err := validateMessage(m); err != nil {
			return err
		}
	}
	return nil
}

func strconvID(id uint32) string {
	// labels are cosmetic; keep imports small.
	const digits = "0123456789"
	if id == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for id > 0 {
		i--
		buf[i] = digits[id%10]
		id /= 10
	}
	return string(buf[i:])
}

func validateMessage(m *Message) error {
	var sw *Signal
	branches := map[int][]*Signal{}
	for _, s := range m.Signals {
		bits, err := SignalBitPositions(s)
		if err != nil {
			err.Message, err.Signal = m.Name, s.Name
			return err
		}
		for _, b := range bits {
			if b < 0 || b >= m.DLC*8 {
				return errf(0, m.Name, s.Name, ReasonBitOutOfRange,
					"bit %d is outside the %d-byte DLC of message %q",
					b, m.DLC, m.Name)
			}
		}
		if s.IsSwitch {
			if sw != nil {
				return errf(0, m.Name, s.Name, ReasonMuxConflict,
					"message already has multiplexor switch %q", sw.Name)
			}
			sw = s
		}
		if s.HasBranch {
			branches[s.Branch] = append(branches[s.Branch], s)
		}
	}

	// Branch signals require exactly one switch, and vice versa.
	if sw == nil && len(branches) > 0 {
		var name string
		for _, list := range branches {
			name = list[0].Name
			break
		}
		return errf(0, m.Name, name, ReasonMuxDangling,
			"multiplexed branch signals exist but no signal is marked M")
	}
	if sw != nil {
		// The switch itself is always present and must not claim a branch.
		if sw.HasBranch {
			return errf(0, m.Name, sw.Name, ReasonMuxConflict,
				"multiplexor switch cannot also carry an mN tag")
		}
		if len(branches) == 0 {
			return errf(0, m.Name, sw.Name, ReasonMuxConflict,
				"multiplexor switch M has no multiplexed branch signals")
		}
		if sw.Enum != nil {
			for v := range sw.Enum {
				if _, ok := branches[int(v)]; !ok {
					return errf(0, m.Name, sw.Name, ReasonMuxDangling,
						"switch enum value %d has no matching m%d signal layout", v, v)
				}
			}
			for v := range branches {
				if _, ok := sw.Enum[int64(v)]; !ok {
					return errf(0, m.Name, sw.Name, ReasonMuxDangling,
						"branch m%d has no corresponding switch enum value", v)
				}
			}
		}
	}

	// Overlap checks:
	//  - unmultiplexed signals live in every frame
	//  - the switch lives in every frame
	//  - branch signals only coexist with other signals of the same branch
	always := []*Signal{}
	for _, s := range m.Signals {
		if !s.HasBranch {
			always = append(always, s)
		}
	}
	if err := checkOverlap(m, always, "always-present signals"); err != nil {
		return err
	}
	for _, list := range branches {
		if err := checkOverlap(m, append(append([]*Signal{}, always...), list...),
			"branch layout"); err != nil {
			return err
		}
	}
	return nil
}

func checkOverlap(m *Message, sigs []*Signal, ctx string) error {
	owner := map[int]string{}
	for _, s := range sigs {
		bits, _ := SignalBitPositions(s)
		for _, b := range bits {
			if prev, clash := owner[b]; clash && prev != s.Name {
				return errf(0, m.Name, s.Name, ReasonBitOverlap,
					"bit %d overlaps signal %q (%s)", b, prev, ctx)
			}
			owner[b] = s.Name
		}
	}
	return nil
}
