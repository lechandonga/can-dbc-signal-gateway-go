package gateway

import (
	"io"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
)

// Reload parses new definitions and atomically activates them.
//
// Semantics:
//   - The new content is fully parsed and validated outside the state lock.
//   - Activation is a single critical section: concurrent ProcessFrame and
//     query calls see either the complete old set or the complete new set,
//     never a mixture.
//   - If parsing fails the call returns the parse error and the gateway keeps
//     serving the previous definitions; no partial activation is visible.
//   - After success, all later frames are decoded with the new definitions.
//     Signals whose layout is unchanged keep their state; signals that are
//     new, removed-then-readded or layout-changed start as never_received.
func (g *Gateway) Reload(r io.Reader, source string) error {
	db, err := dbc.Parse(r, source)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.swapDB(db)
	return nil
}

// ReloadFile reloads from a DBC file path.
func (g *Gateway) ReloadFile(path string) error {
	db, err := dbc.ParseFile(path)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.swapDB(db)
	return nil
}
