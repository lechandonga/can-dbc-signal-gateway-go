// Package gateway 提供帧解码、信号状态维护、超时失效判定与定义热加载。
//
// 并发语义：所有解码与查询在 RWMutex 保护下进行；Reload 在写锁内一次性
// 原子替换整份定义与状态表，并发调用永远不会看到新旧混用的半更新视图。
package gateway

import (
	"io"
	"sync"
	"time"

	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/canbus"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/dbc"
	"github.com/lechandonga/can-dbc-signal-gateway-go/internal/model"
)

// Config 为网关配置。零值 DefaultTimeout 表示默认 1s。
type Config struct {
	// DefaultTimeout 为信号默认超时时长；<=0 时使用 1s。
	DefaultTimeout time.Duration
	// SignalTimeouts 按 "报文名.信号名" 或 "信号名" 单独配置超时；
	// 更具体的键优先。负值表示该信号永不超时。
	SignalTimeouts map[string]time.Duration
}

// SignalView 为查询返回的信号视图：状态快照 + 判定结论。
type SignalView struct {
	State  model.State
	Status model.Status
	Reason string // StatusTimeout 时为固定说明，其余为 ""
}

// Result 为一次成功解码的结果。
type Result struct {
	Frame    canbus.Frame
	MsgID    uint32
	MsgName  string
	MuxValue *int // 非复用报文为 nil
	Updated  []model.State
}

// Stats 为运行计数（仅增，热加载不清零）。
type Stats struct {
	TotalFrames   int64
	DecodedOK     int64
	Rejected      int64
	RejectReasons map[canbus.DecodeReason]int64
}

// Gateway 为组件入口。
type Gateway struct {
	cfg Config

	mu        sync.RWMutex
	db        *model.Database
	states    map[uint32]map[string]*model.State
	lastTS    map[uint32]time.Time
	lastData  map[uint32][]byte // 每报文最后一次被接受帧的原始字节（注入基帧）
	total     int64
	ok        int64
	rejected  int64
	reasonCnt map[canbus.DecodeReason]int64
}

// New 基于已解析定义创建网关。
func New(db *model.Database, cfg Config) *Gateway {
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = time.Second
	}
	if cfg.SignalTimeouts == nil {
		cfg.SignalTimeouts = map[string]time.Duration{}
	}
	return &Gateway{
		cfg:       cfg,
		db:        db,
		states:    map[uint32]map[string]*model.State{},
		lastTS:    map[uint32]time.Time{},
		lastData:  map[uint32][]byte{},
		reasonCnt: map[canbus.DecodeReason]int64{},
	}
}

// Reload 从 reader 解析新定义：解析/校验失败时整体拒绝，旧定义与状态继续可用；
// 成功时原子替换定义并清空运行时状态（旧定义下的历史值不属于新定义，避免新旧混用）。
func (g *Gateway) Reload(r io.Reader) error {
	newDB, err := dbc.Parse(r)
	if err != nil {
		return err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.db = newDB
	g.states = map[uint32]map[string]*model.State{}
	g.lastTS = map[uint32]time.Time{}
	g.lastData = map[uint32][]byte{}
	return nil
}

// ReloadDB 原子替换为外部已解析成功的定义（语义与 Reload 相同）。
func (g *Gateway) ReloadDB(newDB *model.Database) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.db = newDB
	g.states = map[uint32]map[string]*model.State{}
	g.lastTS = map[uint32]time.Time{}
	g.lastData = map[uint32][]byte{}
}

// DB 返回当前定义快照（不可变，可安全读取）。
func (g *Gateway) DB() *model.Database {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.db
}
