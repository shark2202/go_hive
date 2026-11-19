# go-hive 性能基线

本文件记录 `go_hive/go-hive` 的热路径基线，供后续 lane 回归对比。数字是开发机
（非 CI 专用机）上的中位数，绝对值受磁盘与后台负载影响，比较时请使用同一台机器
的同一组测试。

## 环境

| 项 | 值 |
|---|---|
| 日期 | 2026-11-19 |
| 主机 | macOS 26.7.1 / Apple M5 / arm64 |
| Go | go1.26.5 darwin/arm64 |
| 构建 | `CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod` |
| 测试二进制 | 默认 `-benchtime=1s`，每个 benchmark 运行 3 次取中位数 |
| 基准 commit | `06d5c454b336865488f4332d22b9cd74a59fd356`（`w22/sec-perf`） |

## 命令

```bash
cd go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod \
  go test -bench . -benchmem -run '^$' -count=3 ./internal/server ./internal/tasks
```

## 结果（中位数）

| Benchmark | 规模 | ns/op | 时间 | B/op | allocs/op |
|---|---:|---:|---:|---:|---:|
| `BenchmarkColdSessionList100` | 100 个冷会话 | 5,155,562 | 5.16 ms | 848,386 | 6,635 |
| `BenchmarkColdSessionList1000` | 1000 个冷会话 | 55,527,002 | 55.5 ms | 8,433,963 | 66,041 |
| `BenchmarkEventBusPublish` | 1 个订阅者 / 事件 | 1,285 | 1.29 µs | 983 | 12 |
| `BenchmarkChatRuntimeTurn` | 假模型单轮 | 49,322,980 | 49.3 ms | 59,887 | 748 |
| `BenchmarkTaskListSnapshot100` | 100 个任务 | 237,371 | 237 µs | 163,776 | 2,242 |
| `BenchmarkTaskSnapshotRender100` | 100 个任务 | 7,823 | 7.82 µs | 14,387 | 153 |

原始三次运行（`-count=3`）：

```text
BenchmarkColdSessionList100-10     171      7253405 ns/op    848386 B/op    6635 allocs/op
BenchmarkColdSessionList100-10     222      5155562 ns/op    848385 B/op    6635 allocs/op
BenchmarkColdSessionList100-10     266      4884206 ns/op    846785 B/op    6635 allocs/op
BenchmarkColdSessionList1000-10     22     55527002 ns/op   8433976 B/op   66041 allocs/op
BenchmarkColdSessionList1000-10     21     54491825 ns/op   8433963 B/op   66041 allocs/op
BenchmarkColdSessionList1000-10     20     57310635 ns/op   8434232 B/op   66041 allocs/op
BenchmarkEventBusPublish-10     955670         1216 ns/op        983 B/op      12 allocs/op
BenchmarkEventBusPublish-10     991714         1285 ns/op        982 B/op      12 allocs/op
BenchmarkEventBusPublish-10     895291         2657 ns/op        982 B/op      12 allocs/op
BenchmarkChatRuntimeTurn-10         22     47269871 ns/op      58747 B/op     739 allocs/op
BenchmarkChatRuntimeTurn-10         25     49997442 ns/op      59887 B/op     748 allocs/op
BenchmarkChatRuntimeTurn-10         27     49322980 ns/op      60851 B/op     755 allocs/op
BenchmarkTaskListSnapshot100-10    4815       228578 ns/op     163776 B/op    2242 allocs/op
BenchmarkTaskListSnapshot100-10    5299       408069 ns/op     163778 B/op    2242 allocs/op
BenchmarkTaskListSnapshot100-10    5028       237371 ns/op     163776 B/op    2242 allocs/op
BenchmarkTaskSnapshotRender100-10 170722         7039 ns/op      14387 B/op     153 allocs/op
BenchmarkTaskSnapshotRender100-10 162427         7927 ns/op      14387 B/op     153 allocs/op
BenchmarkTaskSnapshotRender100-10 173206         7823 ns/op      14387 B/op     153 allocs/op
```

## 读数说明

- **冷会话列表**：线性扩展（100 → 1000 会话约 10.8 倍）。每个会话读取
  `meta.json` 与 `summary.json`，B/op 与 allocs 随会话数线性增长。列表路由
  `/api/sessions/history` 直接调用该函数。
- **事件总线发布**：`Publish` 对每个事件做历史裁剪、订阅匹配并同步等待
  订阅者，约 1.3 µs、12 次分配；历史保留 1000 条时仍是常量开销。
- **chat 单轮**：假模型 + 空工具，单轮主要成本是 `execution_*` 事件写
  `events.jsonl` 与会话 part 的原子落盘（`fsync`）。本机磁盘抖动较大
  （47–50 ms），跨机器对比时请先跑一次 warm-up。
- **任务快照**：
  - `BenchmarkTaskListSnapshot100` 走 `GET /api/sessions/{id}/tasks` 的
    读盘 + `SerializeRecord` 路径（约 237 µs，其中一次 408 µs 为冷缓存）。
  - `BenchmarkTaskSnapshotRender100` 只做 `FingerprintTasks` + `SnapshotLines`
    + `RenderBlock`（约 7.8 µs），不含读盘。

## Fuzz 冒烟（同一 commit）

两个 fuzz target 各跑 10s，均无 panic、无 crasher 写入仓库：

| Target | 包 | 10s 执行数 |
|---|---|---:|
| `FuzzLoadAgentSpec` | `internal/agents` | ~19,900 |
| `FuzzSSEFrameAndPayload` | `internal/server` | ~57,600 |

复现命令：

```bash
go test ./internal/agents -run '^$' -fuzz FuzzLoadAgentSpec -fuzztime 10s
go test ./internal/server -run '^$' -fuzz FuzzSSEFrameAndPayload -fuzztime 10s
```
