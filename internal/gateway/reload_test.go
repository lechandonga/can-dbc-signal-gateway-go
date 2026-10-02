package gateway

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/decode"
)

func TestReloadAtomicityAndFailure(t *testing.T) {
	g := newTestGateway(t, dbcV1)
	if _, pe := g.ProcessFrame(decode.Frame{ID: 256, Data: []byte{5, 0, 0, 0, 0, 0, 0, 0}, Timestamp: 0}); pe != nil {
		t.Fatal(pe)
	}

	// Illegal reload: rejected wholesale, old definitions/value survive.
	err := g.Reload(strings.NewReader(dbcV2Bad), "bad-v2")
	if err == nil {
		t.Fatalf("GW-REL-01 expected reload failure")
	}
	t.Logf("business=hot-reload case=GW-REL-01 result=rejected error=%v basis='parse outside lock, no swap'", err)
	if g.Database().Source != "test-v1" {
		t.Fatalf("GW-REL-01 source changed to %q on failed reload", g.Database().Source)
	}
	if s := g.Signal(256, "EngineSpeed", 1); s.Raw != 5 {
		t.Fatalf("GW-REL-01 state lost on failed reload: %+v", s)
	}
	if _, pe := g.ProcessFrame(decode.Frame{ID: 257, Data: make([]byte, 8), Timestamp: 1}); pe == nil ||
		pe.ErrorReason() != "unknown_message" {
		t.Fatalf("GW-REL-01 new message must not be partially active: %v", pe)
	}

	// Legal reload: activates atomically, later frames use new definitions.
	if err := g.Reload(strings.NewReader(dbcV2), "test-v2"); err != nil {
		t.Fatalf("GW-REL-02 legal reload: %v", err)
	}
	if g.Database().Source != "test-v2" {
		t.Fatalf("GW-REL-02 source=%q", g.Database().Source)
	}
	// Motorola VehicleSpeed start 7, 16-bit, raw 771 = 0x0303.
	if _, pe := g.ProcessFrame(decode.Frame{ID: 257, Data: []byte{0x03, 0x03, 0, 0, 0, 0, 0, 0}, Timestamp: 10}); pe != nil {
		t.Fatalf("GW-REL-02 post-reload decode: %v", pe)
	}
	if s := g.Signal(257, "VehicleSpeed", 11); s.Status != StatusOK || s.Raw != 771 {
		t.Fatalf("GW-REL-02 new signal %+v", s)
	}
	if s := g.Signal(256, "EngineSpeed", 11); s.Raw != 5 {
		t.Fatalf("GW-REL-02 compatible state should be retained: %+v", s)
	}
	t.Logf("business=hot-reload case=GW-REL-02 result=activated source=test-v2 basis='single critical section swap'")

	// Repeated failed reloads after a successful one also leave state intact.
	if err := g.Reload(strings.NewReader(dbcV2Bad), "bad-v3"); err == nil {
		t.Fatalf("GW-REL-03 expected reload failure")
	}
	if g.Database().Source != "test-v2" {
		t.Fatalf("GW-REL-03 rollback failed: source=%q", g.Database().Source)
	}
	if s := g.Signal(257, "VehicleSpeed", 20); s.Raw != 771 {
		t.Fatalf("GW-REL-03 value lost: %+v", s)
	}
}

// TestConcurrentDecodeQueryReload hammers decoders, readers and reloaders in
// parallel. It must pass deterministically under -race: every operation sees a
// complete definitions snapshot, never a half-updated mixture.
func TestConcurrentDecodeQueryReload(t *testing.T) {
	g := newTestGateway(t, dbcV1)
	const iterations = 200
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var readerWg sync.WaitGroup

	validStatus := func(st Status) bool {
		switch st {
		case StatusOK, StatusTimeout, StatusNeverReceived, StatusStaleBranch:
			return true
		}
		return false
	}

	for r := 0; r < 4; r++ {
		readerWg.Add(1)
		go func() {
			defer readerWg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					now := time.Now().UnixMilli()
					for _, st := range g.Snapshot(now) {
						if !validStatus(st.Status) {
							t.Errorf("GW-RACE reader saw invalid status %q", st.Status)
							return
						}
					}
				}
			}
		}()
	}

	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				ts := int64(seed*1_000_000 + i) // per-goroutine monotonic, interleaved ids
				f := decode.Frame{
					ID:        256,
					Data:      []byte{byte(i), byte(i % 256), 0, 0, 0, 0, 0, 0},
					Timestamp: ts,
				}
				// Rejections (e.g. stale order across goroutines) are expected;
				// the contract is that the call is safe and state stays consistent.
				_, _ = g.ProcessFrame(f)
			}
		}(w + 1)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			var src *strings.Reader
			label := "test-v1"
			if i%2 == 1 {
				src = strings.NewReader(dbcV2)
				label = "test-v2"
			} else {
				src = strings.NewReader(dbcV1)
			}
			if err := g.Reload(src, label); err != nil {
				t.Errorf("GW-RACE legal reload failed: %v", err)
				return
			}
			// Bad reload must never take effect.
			if err := g.Reload(strings.NewReader(dbcV2Bad), "bad"); err == nil {
				t.Errorf("GW-RACE bad reload unexpectedly succeeded")
				return
			}
		}
	}()

	wg.Wait() // writers + reloader
	close(stop)
	readerWg.Wait()
	t.Logf("business=concurrency case=GW-RACE writers=4 readers=4 reloads=20 basis='-race clean, snapshot consistency'")
}
