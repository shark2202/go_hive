# Queen / Worker / Colony 核心流程：真实模型双跑（concept-flows-real）

本页是 [`queen-worker-colony-parity-audit.html`](queen-worker-colony-parity-audit.html) 的实测补充：把 #16（模型槽位运行时消费）、#20（escalation / human-in-the-loop）、#3（worker 工具集）、#5（worker 报告）、#8（colony 目录 / tracker）、#4（resume 语义）放到**同一组真实模型场景**里，用 Python 参考实现（hive 1.0.14，`core/framework`）与 Go 移植（go_hive）双跑，对比事件、字段与落盘。

证据目录：[`docs/e2e-assets/concept-flows-real/`](../../docs/e2e-assets/concept-flows-real/)。真实 LLM 测试 opt-in：`HIVE_REAL_LLM_E2E=1`。

## 1. 方法与约束

- **模型是真实的**：两侧的 `configuration.json` 都指向本地**透传录制代理**（`go_hive/go-hive/e2e/recordproxy`），代理把请求原样转发到真实上游（`HIVE_REAL_LLM_UPSTREAM`，本轮为 OpenAI 兼容的 `deepseek-flash-1017`），只记录 request/response，不伪造响应。API key 只存在于代理进程的环境变量（`HIVE_REAL_LLM_KEY_ENV`，默认 `BROADTOKEN_API_KEY`）；被测 `hive serve` 的配置里只有一个哑元 `CF_PROXY_KEY`，请求头不落盘。
- **两侧同形**：同一个临时 `HIVE_HOME` 结构（`configuration.json` + `colonies/<id>/metadata.json` + `worker.json`），同样的 HTTP 驱动（`POST /api/sessions`、`POST /api/sessions/{id}/chat`、`GET /api/sessions/{id}`、`GET /api/sessions/{id}/snapshot`），同样的 prompt 与任务文本。
- **no fake models / no scripted gateway**：本 lane 的结论全部来自真实模型回合。真实模型的行为有随机性，所以证据同时记录 `checks`（结构断言）与 `observations`（事件词表、工具面、报告 status 等形态差异），不把模型措辞当作对齐证据。
- **证据可复跑**：Go 半场是 `go test -tags e2e ./e2e -run TestRealLLMConceptFlows`；Python 半场是 `go_hive/scripts/concept_flows_real_python.py`；对比器 `go_hive/scripts/concept_flows_real_compare.py` 输出 `comparison/diff.json` 与 `comparison/summary.md`。
- **请求日志摘要**：证据里的 `request-markers.jsonl` 是代理原始记录的摘要（保留 model / status / tool_calls / roles / `[WORKER_ESCALATION]`、`[QUEEN_REPLY]` 标记；完整 request body 在运行时临时落盘，不提交）。

## 2. 场景结果（一轮完整双跑）

| 场景 | Go | Python 1.0.14 | 结论 |
|---|---|---|---|
| worker 派发（queen→`run_worker`→worker→`report_to_parent`→汇总） | worker 落 `colonies/cf_worker/workers/worker_1`，`terminal_exec date`→`report_to_parent(success)`，lead 汇总；`subagent_report` 事件 | worker 落 `workers/session_…`，`get_current_time`（+ tracker）→`report_to_parent(success)`，queen 汇总；`subagent_report` 事件 | **闭环一致**；worker 工具面不同（见 §3） |
| `worker_llm` 探针（不存在 model id） | worker 请求带 `cf-probe-worker-model`→503；worker `state.status=failed`；主槽 queen 请求仍 `deepseek-flash-1017` | worker 请求带 `cf-probe-worker-model`→503；主槽正常 | **两侧都消费**（#16） |
| escalation 链（doom-loop→收件箱→回复→worker 续跑） | `[External event] [WORKER_ESCALATION]`→`list_worker_questions`→`reply_to_worker`→worker 消费 `[QUEEN_REPLY]`→`report_to_parent(success)`；`node_tool_doom_loop` 事件 | **无该链**：worker 3–6 次 `task_list` 后自行 `report_to_parent`（success），queen 未收到 handoff，工具面也没有 `list_worker_questions`/`reply_to_worker` | **参考差**：Go 链是 port 扩展（见 §4） |
| ask_user / park | `client_input_requested`（`park_reason=ask_user`，含 `questions`）→snapshot `activity=awaiting_user`→用户答复→恢复，回复含答案 | 同左（`park_reason`/`questions`/`activity` 字段一致；user part 带 `is_client_input=true`） | **字段/状态一致** |
| `queen_llm` 探针（新会话） | 新会话首个请求带 `cf-probe-queen-model`→503；`queen_id=queen_growth` | 同左 | **两侧都消费**（#16） |
| serve 重启后 resume（fork 出来的 colony） | fork 写 `metadata.queen_session_id`；`serve --colony` 重启后同一 session id 仍 live、可继续 chat；worker 目录 / tracker 完好 | 同左（fork 本来就写 `queen_session_id`；重启 resume 同一 id） | **一致**；本轮修复了 Go fork 不记 id（§5） |

对比器输出：`comparison/summary.md` 中 5 个场景 `aligned`，`escalation_chain` 为 `differences`（即 §4 的参考差，属于**已知且有意保留**的 Go 扩展，不是跑挂）。

## 3. #16 模型槽位消费：修前 / 修后

审计页在基线 `dab983d4` 的结论是「worker_llm / vision_fallback 零消费、queen_llm 创建期不生效」。本轮按 `c4eb266d` 复核：

| 槽位 | 旧状态（dab983d4） | 当前状态（c4eb266d） | 本轮真实模型证据 |
|---|---|---|---|
| `worker_llm` | `newColonyHost` 不传 `WorkerLLM`；worker 恒用主模型 | `loader_model_slots.go::workerChatProvider` 建独立 provider；`loader_colony_host.go:78` 传入；`colony/runtime.go::workerCompleter` 仅 worker/judge 使用 | 配置 `worker_llm.model=cf-probe-worker-model` → worker 请求的 `model` 字段即该 id，上游 503；worker `state.status=failed`；主槽 queen 调用不受影响。两侧一致。 |
| `queen_llm` | `resolveChatProvider` 只读 `llm`；新建会话用主模型 | `queenAwareChatProvider` 在会话创建期读 `queen_llm.<queen>` 并建 provider（chat / colony host / preloaded session 三入口） | 新建 `queen_name=queen_growth` 会话 + `queen_llm.queen_growth.model=cf-probe-queen-model` → 首个（session-start）请求带探针 id，503；两侧一致。 |
| `vision_fallback` | 仅角色名常量，无 caption 链 | `visionCaptionerFor` 把 `vision_fallback` 解析成 `visionfallback.Captioner`，经 `ChatRuntimeDeps.VisionCaptioner` 注入；`internal/visionfallback` 是 Python `caption_tool_image` 的移植 | **已 live**（本 lane 补）：真实文本主模型 `deepseek-v4-pro` + `attach_file` 真实截图 → 槽位被真实调用（caption 含 viewport 分数坐标，`Submit (0.76, 0.54)` vs 真值 `0.7565, 0.5386`）→ caption 注入后续主模型请求、图片块丢弃；两侧同断言全通过，另有 10/10 固定向量字节相同。见 `docs/vision-caption-parity.md` 与 `docs/e2e-assets/vision-caption/`。 |

结论：#16 从「未对齐」改为「已对齐」：三槽消费均已在生产路径，`worker_llm`/`queen_llm` 有真实模型探针，`vision_fallback` 的 caption 链有真实图片 E2E 与逐字节对照。

## 4. #20 escalation：Go 链是「扩展」，不是 Python 逐字对齐

**Go 实测（真实模型，1 次成功链）**：`worker_1` 连续 3 次 `task_list {}` 触发 doom-loop → worker session bus 发 `ESCALATION_REQUESTED` → 会话桥把 `[External event] [WORKER_ESCALATION]`（含 `request_id`）投到 lead transcript → queen/overseer 调 `list_worker_questions` → `reply_to_worker(request_id, guidance)` → worker 消费 `[QUEEN_REPLY]` → `report_to_parent(status=success, summary="recovered after escalation")`。事件侧有 `node_tool_doom_loop`、`subagent_report`。证据：`go/escalation_chain-main/evidence.json`（`full_chain_observed=true`）。

**同一刺激的 Python 1.0.14 实测**：worker 3–6 次 `task_list`（其中相同调用会被 replay breaker 提示/拦截）后**自行** `report_to_parent`；没有 `[WORKER_ESCALATION]` handoff，queen 的工具面也没有 `list_worker_questions` / `reply_to_worker`。代码里这是刻意的：

> `framework/agents/queen/nodes/__init__.py`（colony 阶段工具面注释）：*“Workers have NO escalation channel back to the queen — list_worker_questions / reply_to_worker were removed deliberately. Workers either succeed … or fail-fast …; the queen re-dispatches as needed.”*

因此审计页 GAP-02 原来「escalation 按上游语义收口」的表述不准确：Go 的 doom-loop→收件箱→回复链是 **Go 侧扩展**；Python 参考在 colony 阶段没有这条通道（parallel worker fail-fast / replay breaker / 自报）。这条差异已回填到审计页 #3/#20/GAP-02/GAP-13。

该链对真实模型时序敏感：本 lane 的一次中间运行里，worker 在 queen 指导到达前因 text-only stall 自动失败（`auto_fail_reason=stall_text_only`，grace 2；该次运行未作为最终证据归档），说明这条升级链不是确定性送达。最终归档的 Go 运行 1/1 全链，`evidence.json` 的 `attempts` 数组保留每次尝试的 checks。

## 5. #4 resume：fork 殖民地记录 `queen_session_id`（本轮修复）

`serve --colony` 的重启 resume 依赖 colony `metadata.json` 的 `queen_session_id`。Python 的 `fork_session_into_colony` 本来就写这个字段；Go 的 `writeColonyFromSession` 原先只写 `name/created_at/source_session_id/queen_name`，导致 fork 出来的殖民地在重启时**新铸 session**、旧 transcript 变成孤儿（Python 会沿用记录 id）。

修复：`internal/cli/loader.go::CreateColonyFromSource` 在 session 创建成功后调用 `recordColonyQueenSessionID`（`internal/cli/colony_worker_definition.go`），把 `queen_session_id`（及缺失的 `queen_name`）写回 metadata；已有非空值不覆盖。单测 `TestCreateColonyFromSourceMarksSourceAndStartsOverseer` 增加断言。真实模型双跑验证：fork → 重启 `serve --colony` → 同一 session id live、chat 继续、worker 目录与 tracker 完好；两侧 `metadata_records_session / session_live_after_restart / resumed_reply` 全 true。

## 6. 其余实测差异（观察项）

1. **worker 工具面（#3，降为「部分」）**：Python worker 继承 queen spawn 工具（22 个，含 `get_current_time` / `web_scrape` / `browser_setup` / `update_worker_profile`），Go worker 是 builtin 基座 + `worker.json` 声明（空 `tools` 时 24 个，无 `get_current_time`）。同一任务文本下 Python worker 直接调 `get_current_time`，Go worker 走 `search_tools`→`terminal_exec date` 后成功 report。fork 的 `worker.json` 用 queen 类别标签展开可补回一部分（`context_awareness` 等），但 scaffold colony 仍不同。
2. **run_worker 的 tracker 预检**：Python 在 `run_worker` 前要求「至少一张可写表已注册」（否则返回 “No tables registered for worker writes …”），Go 不做这个预检，worker 可直接启动；本轮 evidence 里 Python 的 `_tracker_registry` 有 1 行、Go 为 0。
3. **worker id 形态**：Python `workers/session_YYYYMMDD_…/`，Go `workers/worker_N/`；两侧 `meta.json` 都记录 lineage（Python 的 `queen_session_id` 在 spawn 时写；Go 在 `state.json/meta.json`）。
4. **事件词表**：Python 发布 `client_input_committed` / `client_reasoning` / `judge_verdict` / `session_title_updated` 等；Go 的事件集更窄（`node_loop_completed` 等也不同）。两侧共有的核心事件（`client_input_received` / `node_loop_iteration` / `llm_turn_complete` / `tool_call_*` / `subagent_report`）均出现；对比器把词表差异列为 observation。
5. **默认 queen 选择**：无 `queen_name` 的 DM 会话，Python 默认 `queen_growth`；Go 的 session scope 默认 `default`（`queen_llm` 以 queen 名为键时需要注意；显式 `queen_name` 时两侧一致）。
6. **会话落盘布局（#2）**：Go 的 colony 会话把客户端消息交给 overseer（session transcript 里是 `[overseer]` 记录），Python 的 colony 会话是单一 queen transcript；两侧的 worker 报告都进入 lead 的 transcript（`[WORKER_REPORT]`）。

## 7. 剩余项（不在本 lane 能力内）

- Python replay breaker 的 Go 移植（相同调用第 3 次被拦截并 park；Go 目前是执行后由 doom-loop 升级）。
- `run_worker` 的「可写 tracker 表」预检（如需与 Python 行为一致）。
- #13 Sentinel 真实投递、#17 破坏性操作、#24 LongMemEval：与本 lane 无关，仍按审计页的未验证处理。

（原先列在此处的 `vision_fallback` 真实图片 caption 已由 vision-caption-real 车道补齐：
见 `docs/vision-caption-parity.md` 与 `docs/e2e-assets/vision-caption/`。）

## 8. 复现

```bash
cd go_hive/go-hive
export BROADTOKEN_API_KEY=...          # 或 HIVE_REAL_LLM_KEY_ENV 指定的变量
export HIVE_REAL_LLM_UPSTREAM=https://token.tokenbroad.com/v1
export HIVE_REAL_LLM_E2E=1
export HIVE_CONCEPT_OUT=<repo>/docs/e2e-assets/concept-flows-real

go test -tags e2e ./e2e -run TestRealLLMConceptFlows -v -count=1 -timeout 45m

uv run --no-project python ../scripts/concept_flows_real_python.py \
  --go-module . --repo-root ../../core \
  --out <repo>/docs/e2e-assets/concept-flows-real \
  --upstream "$HIVE_REAL_LLM_UPSTREAM"

python ../scripts/concept_flows_real_compare.py --out <repo>/docs/e2e-assets/concept-flows-real
```

证据索引：

| 路径 | 内容 |
|---|---|
| `go/<scenario>-<variant>/evidence.json` | Go 半场的 checks / session / events / workers / tracker / model_requests |
| `go/<scenario>-<variant>/request-markers.jsonl` | 代理记录摘要（model、status、tool_calls、`[WORKER_ESCALATION]`/`[QUEEN_REPLY]` 标记） |
| `python/<scenario>-<variant>/evidence.json` | Python 半场同形证据 |
| `python/<scenario>-<variant>/*-serve.log` | Python 侧 serve 日志 |
| `comparison/diff.json` | 逐场景、逐字段的 gate differences / observations |
| `comparison/summary.md` | 可读的对比摘要 |

> 真实模型批次不可逐字复现：`evidence.json` 的 `generated_at`、session id、模型措辞每次都不同；gate 字段（checks）与结构字段是可重复的，模型内容仅作观察。
