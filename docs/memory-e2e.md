# Memory 端到端双跑（Go ⇄ Python）

本文件记录 go_hive 记忆链路（Timeline / Past conversations / Recall）与 Python Hive 的**真实双跑**证据。
不是单测对表：两侧都是真实 `hive serve` 进程、临时 `HIVE_HOME`、同一套确定性本地 LLM 网关，输入、落盘与
prompt 均来自本次运行。

- 日期：2026-11-20
- Go：`.wt/memory-e2e/go_hive/go-hive`（module `github.com/aden-hive/hive-go`，分支 `memory-e2e`，基线 `2ca7328f`）
- Python：`/Users/mac/hive`（`core` @ `2ca7328f`），`uv run --offline hive serve`
- 网关：`e2e/memgw`（本 lane 新增，测试基础设施；确定性 OpenAI 兼容，支持 JSON 与 SSE 两种响应）
- 证据目录：`go_hive/docs/e2e-assets/memory-e2e/`

## 结论

| 项 | 结果 |
| --- | --- |
| Go 真实 e2e | `TestMemoryEndToEndGoRuntime` 3/3 子测试 PASS（timeline/search/backfill/past/recall、first-turn、stop flush） |
| Python 真实 e2e | `memory_e2e_python.py` PASS（12 个阶段断言全部通过） |
| prompt/schema 对比 | 13/13 PASS（timeline system prompt、单会话 prompt 块、recall 基础 system prompt、recall user 格式、manifest、item/meta schema、回填/停止/`TopK` 输出） |
| 门禁 | `make ci` 退出 0（fmt-check / vet / test / cross / parity / parity-targets / wiring_check），parity 4741/4741，refs 5044/5044 |

发现并修复 3 处实现差异（见“差异”节）；1 处注入时序差异记录、不改；其余为配置/编码类差异。

## 复现

```bash
# Go（真实二进制 + 临时 HIVE_HOME + in-process 网关）
cd .wt/memory-e2e/go_hive/go-hive
CGO_ENABLED=0 GOPROXY=off \
HIVE_MEMORY_E2E_OUT=/tmp/memory-e2e-go \
HIVE_MEMORY_E2E_HOME=/tmp/memhome \
  go test -tags e2e ./e2e -run TestMemoryEndToEndGoRuntime -v -count=1

# Python（真实 hive serve + 同一个网关实现的独立进程）
cd /Users/mac/hive
uv run --no-project python .wt/memory-e2e/go_hive/scripts/memory_e2e_python.py \
  --go-module /Users/mac/hive/.wt/memory-e2e/go_hive/go-hive \
  --out /tmp/memory-e2e-py --keep-home /tmp/hive-py-home

# prompt / schema 对比
uv run --no-project python .wt/memory-e2e/go_hive/scripts/memory_e2e_compare.py \
  --go /tmp/memory-e2e-go --py /tmp/memory-e2e-py --out /tmp/memory-e2e-compare
```

两侧 LLM 配置都指向网关：Go 用 `llm.api_base` + 本地模型名（`ollama/memory-e2e`，无需 key）；Python 用
`llm.api_base` + `api_key_env_var`（`openai/memory-e2e`）。`memory_timeline: true` 写在两侧的
`configuration.json`，不依赖环境变量。抓取 `-tags e2e` 测试不会进入 `make ci` 的常规测试集。

## 输入 fixture

- 会话 S1 的三条用户消息（相对日期 + 中文实体）：
  1. `昨天我去参加了同事的婚礼，随礼 800 元。`
  2. `我打算下个月去云南旅行，顺便拍点照片。`
  3. `我家养了一只橘猫，叫团子。`
- S4 的重复消息与召回查询：`帮我回忆一下我在婚礼上随礼的事情，还有北京的安排。`
- 召回失败探针：`FAILRECALL 今天天气怎么样？`（网关对选区请求返回 500）
- 回填 fixture：手写 `session_20250101_093000_meme2e01/events.jsonl`，两条 `client_input_received`
  （`去年我在杭州买了一套房子。` / `前年我换了一辆黑色的车。`），无 `timeline_meta.json`
- TopK fixture：8 个 `session_20250201..08_090000_topk000N`，各一条含 `帆船` 的用户消息；查询
  `帆船绕标训练记录`
- 记忆文件（两侧相同）：
  - 全局 `profile_user.md`（`type: profile`，应作为 pin 回填）
  - 全局 `project_atlas.md`（`type: preference`，由选区返回）
  - queen 作用域 `queen_notes.md`（`type: preference`）

网关对 timeline 请求按 prompt 里的 `Session Sx, YYYY-MM-DD (Weekday)` 生成条目：`yesterday` 解析为会话日
前一天、`end < start` 的区间、未知 kind、越界 turn、不可解析日期、未知 session 标签、空 text —— 这样
“相对日期由会话日期解析”与读取端的归一化都被真实驱动。

## 双跑结果表

| 机制 | Go | Python | 证据 |
| --- | --- | --- | --- |
| `memory_timeline=true` 产生 `timeline.jsonl` + `timeline_meta.json` | PASS（meta `version=1`、`model` 非空、`updated_at` 非空） | PASS（同） | `*-timeline-s1.jsonl`、`*-timeline-meta-s1.json` |
| 相对日期解析（yesterday→ISO） | PASS（`2026-11-19`） | PASS（`2026-11-19`） | `*-timeline-s1.jsonl` |
| `end < start` 自动交换 | PASS（`11-05..11-06`） | PASS（同） | 同上（牙医条目） |
| 未知 kind/越界 turn/坏日期/未知 session/空 text 归一化 | PASS（`reminder→event`、turn→1、日期回落会话日、丢弃 2 条） | PASS（同） | 同上 |
| aliases 截断到 4 条 | PASS | PASS | 同上 |
| 首个完成 turn 即抽取（默认 batch=3 / cooldown=300） | PASS | PASS | `*-timeline-first-turn.jsonl` |
| 停止（DELETE）flush 未抽取的消息 | PASS（真实测试；去掉修复时该子测试失败） | PASS（上游 `session_manager.stop_session` 本就 flush） | `*-timeline-s2-stop-flush.jsonl`、`go-stop-flush-before-fix.txt` |
| 启动回填（无 meta 的历史会话） | PASS（`user_turns=2`，日期按 2025-01-01 解析） | PASS（同） | `*-timeline-backfill-2025.jsonl`、`*-timeline-backfill-prompt.txt` |
| `search_timeline` 工具真调用 | PASS（tool result 含 `婚礼`、`2026-11-19`、会话 id） | PASS（同） | Go/Python S5 转录（见 compare 日志） |
| `search_messages` 找回最终 assistant 回复 | PASS（S1 最后一条回复 token 命中） | PASS（同） | 同上 |
| 中文/CJK 查询 | PASS（`婚礼`） | PASS（`婚礼`） | 同上 |
| past conversations：带日期摘要、最多 6 段 | PASS（8 个命中会话 → 恰好 6 行，格式 `- YYYY-MM-DD (Www), session_…`） | PASS（同） | `*-excerpt-topk.txt` |
| 最终 assistant 回复已索引 | PASS | PASS | `search_messages` 断言 |
| recall：只看当前 query | PASS（选区请求仅 1 条 user 消息，不含历史） | PASS（同） | `go-gateway-requests-main.jsonl` / `py-gateway-requests.jsonl`（`kind=recall`） |
| recall：profile pins 回填 | PASS（`project_atlas.md` + `profile_user.md`） | PASS（同） | `*-recall-block-1.txt` |
| recall：内容不变不重复注入 | PASS（相同消息第二次不再注入，计数 1） | PASS（缓存块不变 → 不注入，计数 1） | `go-conversation-s4.json` / `py-conversation-s4.json` |
| recall：LLM 失败不阻塞 turn | PASS（turn 正常完成；注入 pinned-only 块） | PASS（turn 正常完成；pinned 块在下一个 drain 出现） | `go-recall-block-2.txt`、`py-recall-failure-next-drain.txt` |

## Prompt / schema 对比

`memory_e2e_compare.py` 从两侧网关日志取原文对比（结果落盘 `memory-e2e-compare.md`）：

| 检查 | 结果 |
| --- | --- |
| timeline system prompt（含 schema 指令） | 完全一致（sha 前缀 `0f2ce524f25ca870`） |
| 同一历史会话的 timeline user prompt 块 | 逐字节一致（`go-timeline-backfill-prompt.txt` == `py-timeline-backfill-prompt.txt`） |
| recall 基础 system prompt | 一致（sha `5b6a389a3ce5b5a3`） |
| recall user 消息（`## User query` 段 + `## Available memories` 行集合） | 一致 |
| timeline item JSON 字段 | 一致：`turn,kind,text,start,end,aliases` |
| `timeline_meta.json` 字段 | 一致：`version,user_turns,model,updated_at` |
| 回填/停止写出的条目语义 | 一致（3 条，日期/类型/文本/别名相同） |
| TopK 摘要行数 | 两侧都是 6 |

已知非语义差异（不判失败）：

- timeline meta 的 `model`：Go `ollama_chat/memory-e2e` vs Python `openai/memory-e2e`（配置的模型名归一化）。
- timeline item 的 JSON 编码：Go 紧凑（`{"turn":1,…}`）vs Python 带空格（`{"turn": 1, …}`），解析后相同。
- recall 请求的 JSON 模式：Go 用 `JSONMode` 在 system prompt 末尾追加
  `\n\nPlease respond with a valid JSON object.`；Python 用 `response_format={"type":"json_object"}`，不追加。
  基础 prompt 文本一致；这是传输层约定差异，未改动。

## 差异与处置

### 已修复（本 lane，最小改动）

1. **停止路径没有 flush timeline** — `ChatRuntime.Close()` 只等待后台 writer，没有像上游
   `session_manager.stop_session` 那样抽取“上次后台运行之后到达的消息”。消息数落在 live 合并窗口内
   （<batch 且 cooldown 未到）的会话停止后永远缺 timeline。修复：`Close()` 在 turn loop 结束后用独立
   的 30s 上下文跑一次 `memory.UpdateSessionTimeline`，仍由 `timelineWG` 保证 Close 返回后不再写盘。
   反证：去掉该段后 `stop_flush` 子测试 10s 内无 `timeline.jsonl` 并失败（`go-stop-flush-before-fix.txt`）。
2. **past conversations 重复抑制是 Go 独有的行为** — 上游 `past_conversations_reminder.py` 在每个
   `USER_PROMPT_SUBMIT` 都重新渲染，不比较上次内容；Go 的 `lastPastRecall` 会吞掉相同摘要（实测 Python
   第二次相同消息注入第 2 份摘要，Go 只有 1 份）。修复：删除 Go 的 `lastPastRecall` guard。
   （scoped recall 的 `lastRecall` 去重是上游 `_last_injected_recall` 的移植，保留。）
3. **recall system prompt 少一个尾随换行** — Go 常量结尾没有 `\n`，Python 三引号常量有；逐字节对比差 1
   字节。修复：`SelectMemoriesSystemPrompt` 末尾补 `\n`。

### 记录、不改（行为差异/配置差异）

4. **recall 注入时序** — Python 是“缓存 + 后台刷新 + `wake=False` 注入”：当前 turn 先注入上一轮缓存，
   本轮选区的结果在下一个 drain 生效；选区失败时旧缓存保留，pinned 兜底块同样延迟到下一个 drain。
   Go 在 `ExecuteTurn` 前同步计算并注入，因此同一轮就能看到当前 query 的结果/失败兜底。两侧最终内容
   （选区 + pins）一致；差异只在“何时进入对话”。改成缓存架构超出本 lane 的最小修复范围。
5. **默认 queen 名** — Go 独立会话落在 `queens/default/`；Python 落在 `queens/queen_growth/`（身份/profile
   层差异，不是记忆逻辑差异；两侧脚本都按实际 queen 目录查找）。
6. **脚本网关造成的重复条目** — live timeline 测试里每个完成 turn 都可能触发一次抽取，网关对同一批消息
   返回同一组条目，因此 `timeline.jsonl` 会出现重复条目（Go 6 条、Python 9 条，取决于触发时序）。这是
   fixture 模型“不会只返回新增条目”的产物，不是读取端 bug；断言按语义集合与必含条目检查，不按行数。

## 真实模型复跑（2026-11-21）

工具：`go_hive/scripts/memory_soul_real_e2e.py`（Go `hive serve` ⇄ Python `hive serve`，同一批 seeded 聊天、
真实 `deepseek-flash-1017`；本地透传录制代理不记录 Authorization）。证据：`go_hive/docs/e2e-assets/memory-soul-real/**`。

- **逐字节相同**：timeline system prompt、recall system/user（同 queen scope）、memory-reflection system prompt、
  soul system+user（两个 queen）。
- **schema / 落盘相同**：timeline item 与 meta；soul proposal 的空列表（`applied`/`probes` 为 `[]` 而非 `null`）；
  state 的 `cursors/last_run/last_result/attempts`。
- **作用域**：两侧 selector manifest 只含 global + 本 queen 文件；colony/worker 文件不出现在 manifest 或注入块
  （queen DM 的召回不读 colony，属上游语义）。
- **修复的差异**：past-conversation 注入改用 upstream `wrap_reminder` 包装（原 Go 自定义 header）；reflection
  prompt 缺一空行；minimal queen profile 的 `<core_identity>` 渲染 `None`；soul proposal 写 `null` 列表。
- **NOTE（非代码差异）**：Python 的 alpha recall 块在部分运行里未被 drain 进 transcript（上游缓存注入时序），
  该轮以 NOTE 记录；selector manifest 与 beta 块证明作用域一致。真实慢模型下 turn 边界仍会变化。
- **仍开放**：Go `ReflectionScheduler.OnCompaction` 是死代码——`CONTEXT_COMPACTED` 从未发布，压缩触发的
  长记忆反思未接线。

## 未验证项

- **真实 LLM 的抽取质量**：2026-11-21 已用真实模型跑通链路与 schema，但抽取准确率仍不评分。
- **UI 路径**：未通过 dashboard 触发聊天/记忆；只走 HTTP API。
- **cooldown 到期路径**：live 抽取的“未满 batch 等 cooldown”分支（等 300s 后由下一次 turn 触发）未在 e2e 中真实等待。
- **`SubscribeTimelineTriggers`（memory 包内的旧入口）**：生产路径已由 chat runtime 自己的订阅替代；该函数
  仍保留 `lastRun` 零值语义，未单独验证。
- **LongMemEval**：见 `docs/longmemeval.md`（已有真实数据 20 题双跑，不在本文件范围）。

## 证据文件（`go_hive/docs/e2e-assets/memory-e2e/`）

| 文件 | 内容 |
| --- | --- |
| `timeline-system.txt` | 两侧共用的 timeline system prompt |
| `go-timeline-backfill-prompt.txt` / `py-timeline-backfill-prompt.txt` | 同一历史会话的 user prompt（逐字节相同） |
| `go-timeline-s1.jsonl` / `py-timeline-s1.jsonl` | live 抽取输出（相对日期、区间交换、归一化、别名截断） |
| `go-timeline-first-turn.jsonl` / `py-timeline-first-turn.jsonl` | 默认 batch/cooldown 下首轮抽取 |
| `go-timeline-backfill-2025.jsonl` / `py-timeline-backfill-2025.jsonl` | 历史会话回填 |
| `go-timeline-s2-stop-flush.jsonl` / `py-timeline-s2-stop-flush.jsonl`（及 meta） | 停止 flush |
| `go-excerpt-topk.txt` / `py-excerpt-topk.txt` | 8 个命中会话 → 6 段带日期摘要 |
| `go-recall-block-1.txt` / `py-recall-block-1.txt` | 选区 + pins 回填的注入块 |
| `go-recall-block-2.txt` / `py-recall-failure-next-drain.txt` | selector 失败时的 pinned 兜底（Go 当轮 / Python 下一 drain） |
| `go-past-recall-twice.txt` / `py-past-recall-twice.txt` | 相同消息第二次仍注入摘要（上游行为；Go 修复后一致） |
| `go-stop-flush-before-fix.txt` | 去掉停止 flush 修复后的失败证据 |
| `memory-e2e-compare.md` | 本次 prompt/schema 对比报告 |

原始网关请求日志（数 MB，含完整 system prompt）不随仓库提交，可用上面的复现命令重新生成到
`/tmp/memory-e2e-{go,py}/`。
