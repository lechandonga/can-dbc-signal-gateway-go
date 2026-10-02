package dbc

// Linear frame bit index used throughout the codebase:
//   linear = byte*8 + bit, bit 0 = LSB of the byte.
// This coincides with the DBC start-bit numbering for Intel signals.
// Bit extraction from a byte uses mask 1<<(linear%8).

// SignalBitPositions returns the linear frame bits occupied by a signal,
// ordered from the most significant raw-integer bit to the least significant.
//
// Intel (@1,+little-endian): start bit is the raw LSB; raw bit k occupies
// linear index start+k.
//
// Motorola (@0, big-endian): start bit is the raw MSB, given in DBC
// sawtooth numbering (inside a byte bits count 7..0). Raw bits descend in
// sawtooth order; crossing sawtooth offset 0 wraps to offset 7 of the next
// higher byte (the "+15 sawtooth" step). The final LSB may land in the byte
// below the start byte.
func SignalBitPositions(s *Signal) ([]int, *Error) {
	if s.Length <= 0 || s.StartBit < 0 || s.StartBit > 63 {
		return nil, errf(0, "", s.Name, ReasonInvalidSignal,
			"invalid start bit %d or length %d", s.StartBit, s.Length)
	}
	out := make([]int, s.Length)
	switch s.Order {
	case OrderIntel:
		// out is MSB-first; raw LSB is the start bit.
		for k := 0; k < s.Length; k++ {
			lin := s.StartBit + k
			if lin > 63 {
				return nil, errf(0, "", s.Name, ReasonBitOutOfRange,
					"intel signal extends past bit 63")
			}
			out[s.Length-1-k] = lin
		}
	case OrderMotorola:
		saw := s.StartBit // sawtooth index of the raw MSB
		for k := 0; k < s.Length; k++ {
			if k > 0 {
				if saw%8 == 0 {
					saw += 15 // offset 0 of byte N -> offset 7 of byte N+1
				} else {
					saw--
				}
			}
			if saw > 63 {
				return nil, errf(0, "", s.Name, ReasonBitOutOfRange,
					"motorola signal extends past bit 63")
			}
			out[k] = saw
		}
	default:
		return nil, errf(0, "", s.Name, ReasonInvalidSignal,
			"unknown byte order")
	}
	return out, nil
}
