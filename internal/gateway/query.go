package gateway

import "sort"

// Signal returns the current state of one signal, or nil if it does not exist
// in the active definitions. now is the evaluation timestamp in ms.
func (g *Gateway) Signal(messageID uint32, name string, now int64) *SignalState {
	g.mu.RLock()
	defer g.mu.RUnlock()
	rt := g.states[key(messageID, name)]
	if rt == nil {
		return nil
	}
	m := g.db.Messages[messageID]
	s := g.snapshot(rt, now)
	s.Message = m.Name
	return s
}

// All returns snapshots of every signal in the active definitions, ordered by
// message id then signal name for stable logs.
func (g *Gateway) All(now int64) []SignalState {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.allLocked(now)
}

// Snapshot returns all signals plus a per-message latest-timestamp map in one
// critical section, so concurrent reloads cannot make two read calls disagree
// about which definition generation is active.
func (g *Gateway) Snapshot(now int64) []SignalState {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.allLocked(now)
}

func (g *Gateway) allLocked(now int64) []SignalState {
	ids := make([]uint32, 0, len(g.db.Messages))
	for id := range g.db.Messages {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var out []SignalState
	for _, id := range ids {
		m := g.db.Messages[id]
		for _, s := range m.Signals {
			snap := g.snapshot(g.states[key(id, s.Name)], now)
			snap.Message = m.Name
			out = append(out, *snap)
		}
	}
	return out
}

func (g *Gateway) snapshot(rt *signalRuntime, now int64) *SignalState {
	st := StatusNeverReceived
	if rt.received {
		switch {
		case rt.stale:
			st = StatusStaleBranch
		case rt.timeout > 0 && now-rt.lastSeenAt > int64(rt.timeout/timeMillisecond):
			st = StatusTimeout
		default:
			st = StatusOK
		}
	}
	return &SignalState{
		MessageID:  rt.messageID,
		Signal:     rt.def.Name,
		Raw:        rt.raw,
		Physical:   rt.physical,
		Unit:       rt.unit,
		Status:     st,
		UpdatedAt:  rt.updatedAt,
		LastSeenAt: rt.lastSeenAt,
		Branch:     rt.branch,
		HasBranch:  rt.hasBranch,
	}
}

const timeMillisecond = 1_000_000
