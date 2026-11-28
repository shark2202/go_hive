# Worker meta.json / batch 坐标一致性报告（Python Hive ↔ go_hive）

- 日期：2026-11-25
- lane：`w179-worker-meta`
- 审计范围：colony worker 的 spawn 元数据（`<colony>/workers/<id>/meta.json`）与并行派发的 batch 坐标（`batch_id` / `batch_index` / `batch_size` / `worker_seq`），以及 server worker 读面（`GET /api/sessions/{id}/workers`、`GET /api/sessions/{id}/workers/{worker_id}`）
- 对照实现：`core/framework/host/colony_runtime.py`（`spawn` / `spawn_batch` / `resume_worker`）、`core/framework/tools/queen_lifecycle_tools.py`（`run_worker`）、`core/framework/tools/playbook_tools.py`（`_dispatch_seq`）、`core/framework/server/routes_colony_workers.py` ↔ `go_hive/go-hive/internal/colony`、`internal/server`、`internal/host`、`internal/cli`
- 断言基准：`core/tests/test_run_worker_tool.py::test_subagent_report_carries_batch_metadata`、`::test_run_worker_resume_mode_calls_resume_worker`、`core/framework/server/tests/test_colony_worker_routes.py`

---

## 0. 结论

**worker spawn 元数据与 batch 坐标已对齐上游；四字段缺口与 0-based 序数偏差均已修复。**

| 面 | 判定 | 说明 |
|---|---|---|
| `meta.json` 谱系字段 | ✅ 已修 | Go 现在写 `queen_session_id` / `queen_name` / `spawned_at` / `worker_seq`（此前完全缺失） |
| `meta.json` 其余字段形状 | ✅ 已修 | `goal` / `batch_id` 空值按上游 `or None` 写 null，不再写空串 |
| `batch_id` 生成 | ✅ 已修 | `spawn_batch` 每次调用都铸 `rpw_<UTC 紧凑时间戳>_<8 hex>`；resume 用 `rsw_…`；solo `spawn()` 保持无 batch |
| `batch_index` 基数 | ✅ 已修 | 1-based（`enumerate(tasks, start=1)`）；`0` 仅表示「不属于任何 batch」 |
| `batch_size` | ✅ 已修 | 有 batch 时为批大小；无 batch 时 0 |
| `worker_seq` | ✅ 已修 | playbook 派发按 run 作用域单调计数（原子），并由 `BatchTask` 抬到 worker / meta |
| resume batch 坐标 | ✅ 已修 | `batch_index = idx + 1`（按请求 id 位置）、`batch_size = len(ids)` |
| server 读面 | ✅ 已修 | 历史 worker 走盘时按 `queen_session_id` 归属，`started_at` 取 `spawned_at`；详情/列表新增 `queen_session_id` / `queen_name` |
| `task` 截断 | ⚠ 有意保留 | 上游写 `task[:500]`；Go 写全长（resume 依赖 meta 的 task 回填），读面对齐 `or _extract_historical_task` |
| `spawned_at` 来源 | ✅ | epoch 秒浮点，与上游 `time.time()` 同口径 |

---

## 1. 差异与修复

### 1.1 四个缺失的谱系字段

上游在 `spawn()` 里写一次、永不更新（`framework/host/colony_runtime.py:1774-1791`）：

```python
_meta = {
    "worker_id": worker_id,
    "queen_session_id": self._stream_id,
    "queen_name": self._queen_id,
    "colony_id": self._binding.name if self._binding else None,
    "spawned_at": time.time(),
    ...
}
```

Go 侧 `writeWorkerMeta` 只写了 `worker_id` / `colony_id` / `task` / `profile_name` / `goal` / batch 三元组。
后果不是显示瑕疵：`server` 的 `<colony>/workers` 盘面扫描按 `meta["queen_session_id"] == session.id` 归属
（`framework/server/routes_colony_workers.py:510`），缺少该字段的 worker 一律被跳过 —— Go 运行时重启/退出后，
它派发过的 worker 会从 dashboard 上整体消失；`started_at` 也会退化成目录 mtime。

修复：

- `internal/colony/runtime_options.go` 新增 `RuntimeOptions.QueenName`；`internal/host/host.go` 透传 `Config.QueenName`；
  `internal/cli/loader_colony_host.go` 用既有的 `colonyQueenName(home, colonyID)`（读 colony `metadata.json` 的 `queen_name`）填充。
- `internal/colony/worker_transcript.go::writeWorkerMeta` 写入四字段；`queen_name` / `goal` / `batch_id` 空值按上游 `or None` 写 null。
- `queen_session_id` 取 `r.streamID`，与上游 `self._stream_id`（session 事件作用域）同义。

### 1.2 batch 坐标

| 项 | 上游 | 修复前 Go | 修复后 Go |
|---|---|---|---|
| `spawn_batch` 的 `batch_id` | `rpw_%Y%m%dT%H%M%SZ_<uuid8>`，每次调用都铸 | 空或 `batch_<n>`（仅多任务时） | `rpw_…`，每次 `spawn_batch` 都铸 |
| resume 的 `batch_id` | `rsw_%Y%m%dT%H%M%SZ_<uuid8>` | 复用 `rpw_` 前缀 | `rsw_…` |
| `batch_index` | 1-based | 0-based | 1-based |
| `batch_size` | 批大小 | 总是 `len(tasks)`（solo 也写） | 有 batch 时 `len(tasks)`，solo 为 0 |
| `worker_seq` | playbook run 内 `itertools.count(1)` | 字段存在但从不赋值 | `workerDispatchExecutor` 原子计数 → `BatchTask.WorkerSeq` → worker / meta |

新增 `internal/colony/batch_ids.go` 统一铸 id（`rpw_` / `rsw_`，8 hex 后缀，熵读失败回落到纳秒时钟）；
`newRunWorkerBatchID()` 保留为 `newWorkerBatchID()` 的薄封装，`run_worker` 与 resume 各取所需。
solo `spawn(count=n)` 通过 `SpawnBatchOptions.solo` 显式不铸 batch —— 上游只有 `spawn_batch` 铸 id，
`spawn` 留空，这一区别被写进了测试（见 §3 负向回归）。

### 1.3 server 读面

- `ReadWorkerFromDisk` 增补 `queen_session_id` / `queen_name`（缺失即 null）。
- `walkColonyWorkers` / `historicalColonyWorkerDetail` 把两字段带进列表与详情 payload。
- 归属过滤、`started_at` 回退链（`spawned_at` → mtime）保持上游语义不变。

---

## 2. 兼容性

- 旧 meta（无四字段）：读面按 `.get()` 语义返回 null / mtime，行为与修复前一致；这类目录仍不被归属（上游同）。
- 旧 batch id（`batch_<n>`）：仅作为字符串透传，前端按 `batch_id` 是否存在分组，不解析前缀。
- run-state（`runs/<id>.json`、`workers/<id>/state.json`）的 `batch_index` 随之为 1-based；重启恢复原样回填，不重新编号。
- `session_bridge` / `runtime_publish` 上报 payload 的 `batch_index` 随之为 1-based，与 `[WORKER_REPORT]` 的 `<task_index>` 一致。

---

## 3. 证据

新增/更新测试（全部带 parity 注解或豁免理由）：

| 测试 | 断言 |
|---|---|
| `internal/colony/worker_meta_test.go::TestWorkerMetaRecordsSpawnLineage` | spawn 后 meta 的 `queen_session_id` / `queen_name` / `spawned_at`（区间内浮点）/ `worker_seq` / `colony_id` |
| `...::TestSpawnBatchCoordinatesMatchUpstream` | 同一 `rpw_` id、`batch_index` = 1..N、`batch_size` = N、`worker_seq` 抬升、`Info()` 一致 |
| `...::TestSoloSpawnWorkerHasNoBatchCoordinates` | **负向回归**：solo `spawn` 不得携带 batch（`batch_id` null、`batch_index`/`batch_size` 0） |
| `...::TestResumeWorkersNumberBatchFromOne` | resume 按位置 1-based、`batch_size` = 请求数、`rsw_` 前缀 |
| `internal/colony/playbook_worker_seq_test.go::TestPlaybookDispatchStampsRunScopedWorkerSeq` | 同一 run 三次派发得到 1/2/3，共享 run batch id |
| `...::TestPlaybookDispatchWorkerSeqUniqueUnderConcurrency` | 并发派发下序号唯一（原子计数） |
| `internal/server/routes_colony_workers_test.go::TestWorkerSurfaceReadsColonyMetaLineage` | 列表/详情读到谱系与 batch 坐标，`started_at` 取 `spawned_at` |
| `...::TestWorkerSurfaceSkipsUnattributedLegacyMeta` | **负向回归**：无 `queen_session_id` 的 meta 不被归属；详情谱系为 null |
| `...::TestWorkerSurfaceRoundTripsSpawnedWorkerMeta` | 端到端：真实 colony runtime spawn → 运行时销毁 → 仅靠盘面在 `/workers` 列出，坐标 1..N 不重复 |

已运行（本 lane，模块根 `go_hive/go-hive`）：

- `CGO_ENABLED=0 go test ./internal/colony ./internal/server ./internal/cli -count=1` → ok / ok / ok
- `gofmt -l .` → 空；`CGO_ENABLED=0 go vet ./internal/colony ./internal/server ./internal/host ./internal/cli` → 干净
- `make parity` → `check_parity: OK — 6780 个测试全部带有 parity 注解或豁免说明`
- 规模门禁：`go test ./internal/quality -run 'TestFileSizeBudget|TestFuncSizeBudget|TestLanePrefixBudget|TestPackageDocBaseline'` → ok
  （为守住 1500 行/文件与 200 行/函数：`spawnBatchOn` + `startWorker` 迁入 `runtime_worker_lifecycle.go`，
  `run_worker` 的 tasks 归一化抽为 `runWorkerBatchTasks`）

未运行（不在本 lane 范围）：全量 `make ci`、真实 serve 进程重启后的盘面复验、前端 dashboard 手工走查。

---

## 4. 残余

- `task` 未按上游截断到 500 字符（有意：resume 以 meta 的 task 回填缺失 transcript 场景）。
- `spawned_at` 为 Go `time.Now()` 的 epoch 秒，与上游同为墙钟秒；跨进程时钟漂移的排序影响与上游同。
- 历史 `batch_<n>` id 的旧目录不会被重写（meta 只写一次），属预期。
