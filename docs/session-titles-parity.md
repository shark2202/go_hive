# 会话标题的模型槽位（上游 `_title_llm`）

> 上游参考：`core/framework/server/queen_orchestrator.py:1954`（标题特性自 `ede4174e` 起；
> 非 1.0.15 delta——v1.0.15 release notes 未提 title）。
> Go 侧：`internal/server/session_titles.go` + 两处 `SubscribeTitleTrigger` 接线。
> lane `title-llm`，2026-11-22，基线 main `6375dd0d`（rebase 到 `b6e73623`）。
> 工具面（`contracts/tool-definitions.json`）sha 与本 lane 基线一致
> （`b896eda3…`）；`refactor_freeze.sh compare wave15-base` 通过（差异仅既有
> allowlist 项，非本 lane 引入）。

## 0. 结论

上游在 `create_queen` 装配 queen loop 时只解析一次标题模型，然后把它交给
`subscribe_title_trigger`：

```python
# queen_orchestrator.py:1951-1955
# Session title, written by the worker model after the first turn.
from framework.server.session_titles import subscribe_title_trigger

_title_llm = (session_manager.build_worker_llm() if session_manager is not None else None) or session.llm
_reflection_subs += subscribe_title_trigger(session.event_bus, queen_dir, _title_llm)
```

规则（本次用上游测试 + 探针实跑确认）：

| # | 规则 | 上游事实 |
|---|---|---|
| T1 | `worker_llm` 未配置时回退会话模型 | `SessionManager.build_worker_llm()` 在 `get_preferred_worker_model()` 为 None 时返回 `None`（docstring 明说 caller 应回退 `session.llm`）；`or session.llm` 即回退 |
| T2 | 读取时机 = 订阅时一次，不是每次命名重读 | `_title_llm` 是 `create_queen` 里的局部值，闭包捕获后传给 trigger；运行中改 `worker_llm` 不会移动已启动会话的标题模型，只影响之后新建的会话 |
| T3 | 与 memory 槽不同 | `_memory_llm = session_manager.memory_llm(session)` 是每次调用重解析（`_MemoryLLM.current`）；标题槽是订阅时定值 |

Go 差异（本 lane 修复前）：两处 `SubscribeTitleTrigger(...)` 都直接传会话 provider
（`queenAwareChatProvider` 的结果），配置了 `worker_llm` 也不会用它命名会话。

## 1. 上游证据（实跑）

上游没有专门测 `_title_llm` 选槽的测试：`core/tests` 里只有
`test_session_titles.py`（功能提交 `ede4174e` 随附），且它直接以显式 `llm` 调
`subscribe_title_trigger(bus, d, llm)`；全仓 `grep _title_llm` 只有
`queen_orchestrator.py` 两处与 backlog 登记。因此：

1. **1:1 调用链测试**（对齐 g1-resid 的跑法）：

   ```bash
   cd /Users/mac/hive/core
   uv run --offline pytest tests/test_session_titles.py -q
   # 8 passed
   ```

   覆盖：完成一轮后命名并广播 `session_title_updated`、只调工具的回合不命名、
   用户标题不被覆盖、clean_title/路由。Go 侧同 5 条已 1:1 移植
   （`internal/server/session_titles_test.go`、`routes_session_history_test.go`）。

2. **选槽规则探针**（scratch，不入库；`HIVE_HOME` 指向临时目录）：

   | 断言 | 结果 |
   |---|---|
   | 空配置下真实 `SessionManager().build_worker_llm()` 返回 `None` | PASS |
   | 编排表达式 `(build_worker_llm() or session.llm)` 在空槽时 `is session.llm`；配置 worker 时取 worker | PASS |
   | 用表达式结果订阅 trigger，随后把 `build_worker_llm` 改成另一个模型；完成一轮后标题由**订阅时**模型写出（新模型 0 次调用） | PASS |

   即 T1（回退）与 T2（读取时机）均由真实上游代码路径（`session_manager` +
   `session_titles.subscribe_title_trigger`）验证，而不是只读源码。

## 2. Go 实现

- `internal/server/session_titles.go` — `TitleTriggerLLM(home, session)`：
  `worker_llm` 段存在（`WorkerLLMSection` 非 nil，等价 `build_worker_llm` 返回
  provider）→ 新建该槽的 provider（`SectionProvider`，与 colony worker 同一构造，
  每次调用新建、不共享可热换的 `ProviderSlot`）；否则返回会话自己的 provider
  （等价 `or session.llm`）。只在订阅时调用一次，语义即 T2。
- `internal/cli/loader.go:627`（创建/恢复会话）与
  `internal/cli/loader_preloaded_session.go:275`（`serve --colony` 预载会话）：
  `SubscribeTitleTrigger(..., hiveserver.TitleTriggerLLM(home, provider))`。

## 3. Go 测试

`internal/server/session_titles_test.go::TestTitleTriggerLLMTakesTheWorkerSlotWhenConfigured`：

- 未配置 `worker_llm`：`TitleTriggerLLM` 原样返回会话 completer；并订阅 trigger
  实跑一轮，标题由该 completer 写出（复用上游 `test_a_finished_turn_titles_the_session_and_announces_it`
  的完成轮路径）。
- 配置 `worker_llm`：得到 worker 槽 provider（model `groq/llama-worker`、段内
  `api_base`），不是会话 provider。
- 读取时机：改写 `worker_llm` 后再解析得到新模型（`groq/llama-worker-next`），
  而已构建的 provider 仍是 `groq/llama-worker`（运行中会话不漂移）。

## 4. 复现

```bash
cd go_hive/go-hive
export CGO_ENABLED=0 GOPROXY=off GOSUMDB=off GOFLAGS=-mod=mod
go build ./...
go test -count=1 -run 'TestTitleTriggerLLM' ./internal/server -v
go test -count=1 ./internal/server ./internal/cli ./internal/quality

cd ../..
bash go_hive/scripts/merge_postcheck.sh
bash go_hive/scripts/check_parity_targets.sh go_hive/go-hive
bash go_hive/scripts/refactor_freeze.sh compare wave15-base
```

## 5. 口径 / 遗留

- 上游 `build_worker_llm()` 的 `use_antigravity_subscription` 分支返回
  `AntigravityProvider`；Go 的 worker 槽构造（`WorkerLLMSection` + `SectionProvider`）
  没有该 transport（主槽以 `ErrAntigravityUnsupported` 显式拒绝）。这是既有
  worker 槽差异（colony worker 同路径），非本 lane 引入；本 lane 的标题槽复用
  同一构造，因此与该差异同源。重看条件：移植 Antigravity provider 时一并。
- `worker_llm` 写入只对之后新建的会话生效（T2）；主 `llm` 槽的热换仍会通过会话
  provider 对象影响回退路径下的标题模型，与上游 `session.llm` 被就地 reconfigure
  的行为一致。
