// Package fleet 在单路 gateway.Gateway 之上提供多路总线并存：
// 每路拥有独立的定义、运行状态、超时配置与统计，按总线名分发。
//
// 隔离语义：某一路定义加载失败或持续收到坏帧，只计入该路并返回该路结论，
// 不影响其它路；逐路换定义要么整份成功、要么原路维持。单路用法（gateway 包）
// 不经过本包，行为保持不变。
package fleet

import (
	"io"
	"sort"
	"sync"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/gateway"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// Manager 管理多路具名总线。自身并发安全；各路 Gateway 亦各自并发安全。
type Manager struct {
	mu    sync.RWMutex
	buses map[string]*gateway.Gateway
	cfgs  map[string]gateway.Config
}

// NewManager 创建空的多路管理器。
func NewManager() *Manager {
	return &Manager{
		buses: map[string]*gateway.Gateway{},
		cfgs:  map[string]gateway.Config{},
	}
}

// BusError 表示总线名不存在的可区分结论。
type BusError struct {
	Bus    string
	Reason string // UNKNOWN_BUS
}

func (e *BusError) Error() string { return "bus " + e.Bus + ": UNKNOWN_BUS" }

// Add 用已解析成功的定义加入一路总线。重名返回 false（不覆盖既有路）。
func (m *Manager) Add(name string, db *model.Database, cfg gateway.Config) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.buses[name]; exists {
		return false
	}
	m.cfgs[name] = cfg
	m.buses[name] = gateway.New(db, cfg)
	return true
}

// Load 解析 DBC 并原子地加入一路总线；解析失败时该路不存在（或若为已存在路，
// 语义与 Reload 相同：失败维持原路）。
func (m *Manager) Load(name string, r io.Reader, cfg gateway.Config) (*dbc.Summary, error) {
	res, err := dbc.ParseDetailed(r)
	if err != nil {
		return res.Summary, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.buses[name]; ok {
		existing.ReloadDB(res.Database)
		return res.Summary, nil
	}
	m.cfgs[name] = cfg
	m.buses[name] = gateway.New(res.Database, cfg)
	return res.Summary, nil
}

// Remove 移除一路总线（不存在返回 false）。
func (m *Manager) Remove(name string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.buses[name]; !ok {
		return false
	}
	delete(m.buses, name)
	delete(m.cfgs, name)
	return true
}

// Names 返回全部总线名（排序，保证汇总稳定）。
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.buses))
	for n := range m.buses {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Has 判断总线是否存在。
func (m *Manager) Has(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.buses[name]
	return ok
}

// Bus 返回某路网关（供高级用法直接调用）；不存在返回 nil。
func (m *Manager) Bus(name string) *gateway.Gateway {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.buses[name]
}

// Reload 逐路原子换定义：失败时该路维持原样并返回错误，其它路不受影响。
func (m *Manager) Reload(name string, r io.Reader) (*dbc.Summary, error) {
	gw := m.Bus(name)
	if gw == nil {
		return nil, &BusError{Bus: name, Reason: "UNKNOWN_BUS"}
	}
	res, err := dbc.ParseDetailed(r)
	if err != nil {
		return res.Summary, err
	}
	gw.ReloadDB(res.Database)
	return res.Summary, nil
}

// HandleFrame 按总线名分发一帧；未知总线返回 *BusError（与帧级拒绝可区分）。
func (m *Manager) HandleFrame(bus string, f canbus.Frame) (*gateway.Result, error) {
	gw := m.Bus(bus)
	if gw == nil {
		return nil, &BusError{Bus: bus, Reason: "UNKNOWN_BUS"}
	}
	return gw.HandleFrame(f)
}

// Inject 按总线名分发一次注入；未知总线返回 *BusError。
func (m *Manager) Inject(bus string, req gateway.InjectRequest) (*gateway.Result, error) {
	gw := m.Bus(bus)
	if gw == nil {
		return nil, &BusError{Bus: bus, Reason: "UNKNOWN_BUS"}
	}
	return gw.Inject(req)
}

// GetSignal 按总线名分发查询。
// 返回 ok=false 有两种来源：总线不存在（err 为 *BusError）或信号/报文不存在。
func (m *Manager) GetSignal(bus string, msgID uint32, sigName string, now time.Time) (gateway.SignalView, bool, error) {
	gw := m.Bus(bus)
	if gw == nil {
		return gateway.SignalView{}, false, &BusError{Bus: bus, Reason: "UNKNOWN_BUS"}
	}
	v, ok := gw.GetSignal(msgID, sigName, now)
	return v, ok, nil
}

// Snapshot 返回某路全量信号快照。
func (m *Manager) Snapshot(bus string, now time.Time) ([]gateway.SignalEntry, error) {
	gw := m.Bus(bus)
	if gw == nil {
		return nil, &BusError{Bus: bus, Reason: "UNKNOWN_BUS"}
	}
	return gw.Snapshot(now), nil
}

// Stats 返回某路计数。
func (m *Manager) Stats(bus string) (gateway.Stats, error) {
	gw := m.Bus(bus)
	if gw == nil {
		return gateway.Stats{}, &BusError{Bus: bus, Reason: "UNKNOWN_BUS"}
	}
	return gw.Stats(), nil
}

// BusSummary 为单路状态汇总（与逐路查询结果一致）。
type BusSummary struct {
	Name     string
	Stats    gateway.Stats
	Messages int
	Signals  int
}

// Summary 一次取回所有路的状态汇总；逐路独立加锁，内容与单独 Stats/定义一致。
func (m *Manager) Summary() []BusSummary {
	names := m.Names()
	out := make([]BusSummary, 0, len(names))
	for _, name := range names {
		gw := m.Bus(name)
		st := gw.Stats()
		db := gw.DB()
		sigs := 0
		for _, msg := range db.Messages {
			sigs += len(msg.Signals)
		}
		out = append(out, BusSummary{
			Name: name, Stats: st, Messages: len(db.Messages), Signals: sigs,
		})
	}
	return out
}
