package gateway

import (
	"io"
	"sort"
	"sync"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// BusRegistry 管理多个相互隔离的总线网关。
type BusRegistry struct {
	mu    sync.RWMutex
	buses map[string]*Gateway
}

// NewBusRegistry 创建多路总线管理器。
func NewBusRegistry() *BusRegistry {
	return &BusRegistry{buses: map[string]*Gateway{}}
}

// AddBus 添加一条总线。重名会返回 false，已有总线不受影响。
func (r *BusRegistry) AddBus(name string, db *model.Database, cfg Config) (*Gateway, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if name == "" {
		name = "default"
	}
	if _, exists := r.buses[name]; exists {
		return r.buses[name], false
	}
	g := New(db, cfg)
	g.setName(name)
	r.buses[name] = g
	return g, true
}

// Get 返回已注册总线；未知总线返回 nil,false。
func (r *BusRegistry) Get(name string) (*Gateway, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	g, ok := r.buses[name]
	return g, ok
}

// MustGet 与 Get 相同，但未知总线返回可区分的注入错误（方便统一封装）。
func (r *BusRegistry) MustGet(name string) (*Gateway, error) {
	g, ok := r.Get(name)
	if !ok {
		return nil, &canbus.InjectError{Reason: canbus.ReasonUnknownBus, BusName: name,
			Detail: "bus name is not registered"}
	}
	return g, nil
}

// ReloadBus 只替换指定总线；解析失败或总线未知均不影响其它总线。
func (r *BusRegistry) ReloadBus(name string, reader io.Reader) (*dbc.ImportSummary, error) {
	g, ok := r.Get(name)
	if !ok {
		return nil, &canbus.InjectError{Reason: canbus.ReasonUnknownBus, BusName: name,
			Detail: "bus name is not registered"}
	}
	db, sum, err := dbc.ParseDetailed(reader)
	if err != nil {
		return sum, err
	}
	g.ReloadDB(db)
	return sum, nil
}

// BusStatus 是一条总线的汇总快照。
type BusStatus struct {
	Name    string
	Stats   Stats
	Signals []SignalEntry
}

// SnapshotAll 汇总所有总线；注册表读锁保证总线集合稳定，每路状态独立加锁。
func (r *BusRegistry) SnapshotAll(now time.Time) []BusStatus {
	r.mu.RLock()
	names := make([]string, 0, len(r.buses))
	for name := range r.buses {
		names = append(names, name)
	}
	sortNames(names)
	out := make([]BusStatus, 0, len(names))
	for _, name := range names {
		g := r.buses[name]
		out = append(out, BusStatus{Name: name, Stats: g.Stats(), Signals: g.Snapshot(now)})
	}
	r.mu.RUnlock()
	return out
}

func sortNames(names []string) { sort.Strings(names) }
