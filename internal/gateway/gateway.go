// Package gateway maintains the latest vehicle state decoded from a local
// frame source: signal values, physical units, timestamps and validity. It
// supports atomic hot-reload of DBC definitions and configurable timeouts.
package gateway

import (
	"sync"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/decode"
)

// Status is the machine-readable validity of a signal.
type Status string

const (
	StatusNeverReceived Status = "never_received" // no frame since definitions became active
	StatusOK            Status = "ok"
	StatusTimeout       Status = "timeout"      // no update within Timeout
	StatusStaleBranch   Status = "stale_branch" // last value came from a branch not selected by the newest frame
)

// SignalState is an immutable snapshot of one signal.
type SignalState struct {
	MessageID  uint32
	Message    string
	Signal     string
	Raw        int64
	Physical   float64
	Unit       string
	Status     Status
	UpdatedAt  int64 // timestamp (ms) carried by the frame that last set the value
	LastSeenAt int64 // timestamp (ms) of the latest frame carrying the signal, even if the value was equal
	Branch     int   // branch at the time the value was produced, -1 otherwise
	HasBranch  bool
}

// Config holds gateway-wide options.
type Config struct {
	// DefaultTimeout is applied to every signal unless overridden per signal.
	// Zero disables timeout (a signal stays valid forever once received).
	DefaultTimeout time.Duration
	// SignalTimeout overrides the default for "MessageName.SignalName".
	SignalTimeout map[string]time.Duration
}

// Gateway is safe for concurrent use.
type Gateway struct {
	mu     sync.RWMutex
	db     *dbc.Database
	cfg    Config
	states map[string]*signalRuntime // key: "id:signal"
}

type signalRuntime struct {
	def       *dbc.Signal
	messageID uint32
	timeout   time.Duration

	raw        int64
	physical   float64
	unit       string
	updatedAt  int64
	lastSeenAt int64
	received   bool
	branch     int
	hasBranch  bool
	stale      bool // belonged to a branch that a newer frame did not select
}

// New builds a gateway from an already parsed database.
func New(db *dbc.Database, cfg Config) *Gateway {
	g := &Gateway{cfg: cfg}
	g.swapDB(db)
	return g
}

func (g *Gateway) timeoutFor(m *dbc.Message, s *dbc.Signal) time.Duration {
	if g.cfg.SignalTimeout != nil {
		if d, ok := g.cfg.SignalTimeout[m.Name+"."+s.Name]; ok {
			return d
		}
	}
	return g.cfg.DefaultTimeout
}

// swapDB builds a fresh runtime set. The caller must hold g.mu for writing.
// State for identical (message id + signal name) signals is carried over only
// when the definition is layout-compatible; otherwise the signal resets, so
// values can never be interpreted under the wrong layout.
func (g *Gateway) swapDB(db *dbc.Database) {
	next := map[string]*signalRuntime{}
	for _, m := range db.Messages {
		for _, s := range m.Signals {
			rt := &signalRuntime{
				def:       s,
				messageID: m.ID,
				timeout:   g.timeoutFor(m, s),
				branch:    -1,
			}
			if g.states != nil {
				if old := g.states[key(m.ID, s.Name)]; old != nil && layoutEqual(old.def, s) {
					cp := *old
					cp.def = s
					cp.timeout = rt.timeout
					next[key(m.ID, s.Name)] = &cp
				} else {
					next[key(m.ID, s.Name)] = rt
				}
			} else {
				next[key(m.ID, s.Name)] = rt
			}
		}
	}
	g.db = db
	g.states = next
}

func layoutEqual(a, b *dbc.Signal) bool {
	return a.StartBit == b.StartBit && a.Length == b.Length &&
		a.Order == b.Order && a.Signed == b.Signed &&
		a.Factor == b.Factor && a.Offset == b.Offset
}

func key(id uint32, signal string) string {
	return uintKey(id) + ":" + signal
}

func uintKey(id uint32) string {
	if id == 0 {
		return "0"
	}
	var buf [12]byte
	i := len(buf)
	for id > 0 {
		i--
		buf[i] = byte('0' + id%10)
		id /= 10
	}
	return string(buf[i:])
}

// Database returns the active definitions snapshot.
func (g *Gateway) Database() *dbc.Database {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.db
}

// IngestResult reports what happened to a frame. Rejected frames carry a
// decode error; existing state is untouched in that case.
type IngestResult struct {
	Accepted bool
	Reason   decode.RejectReason // empty when accepted
	Message  string
}

// ProcessFrame applies the deterministic ordering/decode/state rules:
//
//   - Frames must be accepted in non-decreasing timestamp order per message.
//     A frame whose timestamp is older than the newest frame already seen for
//     that message is rejected with reason "stale_timestamp"; it never touches
//     state. Equal timestamps are applied in call order.
//   - Malformed/unknown frames are rejected by the decoder and leave all
//     existing state untouched.
//   - For multiplexed messages, branch signals not selected by the newest
//     frame become stale_branch; receiving their branch again clears it.
func (g *Gateway) ProcessFrame(f decode.Frame) (*decode.FrameResult, *ProcessError) {
	g.mu.Lock()
	defer g.mu.Unlock()

	res, derr := decode.Decode(g.db, f)
	if derr != nil {
		fe := derr.(*decode.FrameError)
		return nil, &ProcessError{FrameError: fe}
	}

	m := g.db.Messages[f.ID]
	// Ordering rule: reject older frames for the same message.
	latest := int64(-1)
	for _, rt := range g.states {
		if rt.messageID == f.ID && rt.lastSeenAt > latest {
			latest = rt.lastSeenAt
		}
	}
	if f.Timestamp < latest {
		return nil, &ProcessError{
			Stale:     true,
			LastTime:  latest,
			FrameTime: f.Timestamp,
			MessageID: f.ID,
		}
	}

	active := map[string]bool{}
	for _, v := range res.Signals {
		active[v.Name] = true
	}
	// Mark branch signals of this message that were not present as stale.
	for _, s := range m.Signals {
		rt := g.states[key(f.ID, s.Name)]
		if rt == nil {
			continue
		}
		if s.HasBranch && !active[s.Name] {
			rt.stale = true
		}
	}
	for _, v := range res.Signals {
		rt := g.states[key(f.ID, v.Name)]
		if rt == nil {
			continue
		}
		rt.raw = v.Raw
		rt.physical = v.Physical
		rt.unit = v.Unit
		rt.updatedAt = f.Timestamp
		rt.lastSeenAt = f.Timestamp
		rt.received = true
		rt.hasBranch = res.HasBranch
		rt.branch = res.Branch
		rt.stale = false
	}
	return res, nil
}

// ProcessError distinguishes decode rejections from stale-timestamp reorders.
type ProcessError struct {
	*decode.FrameError
	Stale     bool
	LastTime  int64
	FrameTime int64
	MessageID uint32
}

func (e *ProcessError) ErrorReason() string {
	if e.Stale {
		return "stale_timestamp"
	}
	return string(e.FrameError.Reason)
}
