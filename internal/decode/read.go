package decode

import (
	"fmt"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
)

// readSignal extracts the signed raw integer of one signal from frame data.
// Bits are read LSB-first per the signal layout; the caller guarantees DLC.
func readSignal(s *dbc.Signal, data []byte) (int64, error) {
	bits, err := dbc.SignalBitPositions(s)
	if err != nil {
		return 0, fmt.Errorf("%s: %s", s.Name, err.Detail)
	}
	var u uint64
	// bits is MSB-first: walk it like a binary number.
	for j, lin := range bits {
		if lin < 0 || lin/8 >= len(data) {
			return 0, fmt.Errorf("%s: bit %d outside %d-byte frame", s.Name, lin, len(data))
		}
		if data[lin/8]&(1<<uint(lin%8)) != 0 {
			u |= 1 << uint(len(bits)-1-j) // keep canonical integer bit weight
		}
	}
	if s.Signed && s.Length < 64 && u&(1<<uint(s.Length-1)) != 0 {
		u |= ^uint64(0) << uint(s.Length) // sign extend to 64 bits
	}
	return int64(u), nil
}
