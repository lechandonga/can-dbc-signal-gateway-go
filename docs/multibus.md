# 多路总线并存（internal/fleet）

`fleet.Manager` 在单路 `gateway.Gateway` 之上提供一个进程内的多路总线服务。
每路拥有**独立的定义、运行状态、超时配置与统计**；单路用法（直接使用 `gateway` 包）
完全保留，不经过 Manager。

## 生命周期与分发

- `NewManager()`；`Load(name, reader, cfg)` 解析 DBC 并加入一路（返回导入摘要）；
  `Add(name, db, cfg)` 用已解析定义加入；`Remove(name)` 移除。
- 重名 `Add` 返回 false 不覆盖；对已存在路执行 `Load`/`Reload` 即原子换定义。
- 分发 API 均以总线名为首参：`HandleFrame(bus, f)`、`Inject(bus, req)`、
  `GetSignal(bus, id, sig, now)`、`Snapshot(bus, now)`、`Stats(bus)`。
- 总线名不认识：返回可区分的 `*fleet.BusError`（`UNKNOWN_BUS`），
  与帧级 `*canbus.DecodeError`、信号不存在（`ok=false`）三层结论互不混淆。

## 隔离语义

- 某一路定义未加载成功（`Load`/`Reload` 返回错误）时，该路保持原状，其它路无任何变化。
- 某一路持续收到坏帧，拒绝只计入该路统计；其它路的计数与查询结论不受影响。
- 逐路换定义要么整份成功、要么该路维持原样；不存在跨路串扰。
- `Summary()` 一次返回所有路的 `BusSummary`（报文数、信号数、统计），
  结果与逐路调用 `Stats()` / 查定义完全一致；路名排序保证输出稳定。

## 并发与规模

- 多路可同时解码、注入、查询、换定义；竞态检测下计数必须精确
  （`go test -race ./...`，用例 `TestMultiBusConcurrent`）。
- 规模用例 `TestScaleHundredsMessages`：400 报文 / 3200 信号定义，
  加载、逐路解码、按信号查询与全路汇总均为线性量级，秒级完成。

## 最小用法

```go
mgr := fleet.NewManager()
summary, err := mgr.Load("PT", dbcFileReader, gateway.Config{DefaultTimeout: time.Second})
// err != nil -> *dbc.ErrorList（该路未建立/未变更）；summary 含采用/忽略/拒绝明细
res, err := mgr.HandleFrame("PT", canbus.Frame{ID: 100, Data: data, Timestamp: t0})
res, err = mgr.Inject("PT", gateway.InjectRequest{MsgID: 100,
    Values: map[string]float64{"ASpeed": 800}, Timestamp: t1})
view, ok, err := mgr.GetSignal("PT", 100, "ASpeed", now) // err=*fleet.BusError 表示未知总线
for _, s := range mgr.Summary() { fmt.Println(s.Name, s.Stats) }
```
