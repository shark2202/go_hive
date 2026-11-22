# LongMemEval S，n=60：notes 开关四组对比 — Go/Py × 有/无 notes（2026-11-22）

> 结论先行：在同一份 60 题样本（六类各 10 题，含 2 道 `_abs`）上打开 notes（上游 `HIVE_LME_NOTES=1`；
> Go 侧新增等价开关 `hive-longmemeval run --notes`）后，**两侧的 notes 都真实进了隔离 `HIVE_HOME`**
> （每题 notes 数 Go 均值 42.4 / Py 46.4；`notes_indexed` 两侧 60/60；queen 实际读笔记 Go 35 次 / Py 65 次），
> 但对总分的效应在 n=60 上**方向不一致、统计上不可区分**：
>
> | 组 | overall | vs 同侧无 notes |
> |---|---|---|
> | Go 无 notes（基线） | 52/60 = **86.7%**（95% CI 75.8–93.1） | — |
> | Go 有 notes（本次） | 54/60 = **90.0%**（95% CI 79.9–95.3） | **+2 题**（+5/−3，McNemar p=0.727） |
> | Python 无 notes（基线） | 51/60 = **85.0%**（95% CI 73.9–91.9） | — |
> | Python 有 notes（本次） | 50/60 = **83.3%**（95% CI 72.0–90.7） | **−1 题**（+4/−5，McNemar p=1.000） |
>
> 打开 notes 后 Go 比 Python 高 4 题（54 vs 50，+6/−2，McNemar p=0.289，不显著）。逐题看，两侧的翻题集中在
> preference（Go 6→10、Py 6→6）与 temporal/multi-session/knowledge-update（Go/Py 各有 2–3 题向内翻），
> 且若干翻题的 judge 文本对实质相近的答案给出相反结论（见 §2.5）——这更像模型每次运行的检索/措辞与单次
> LLM judge 噪声，而不是 notes 带来的系统性增益或损失。**n=60 不足以判定 notes 对分数的方向性影响；
> 本轮能确认的是「notes 开关在 Go 侧与上游语义等价且真实生效」这一点（§2.4）。**

- 主判沿用 `deepseek-v4-pro`（与既有 n=20 / n=60 基线同口径）；官方 `gpt-4o-2024-08-06` 本机不可得 →
  自建基线，不与发布分（88.4/82.0）可比。
- 数据：`longmemeval_s_cleaned.json`（277,383,467 B，sha256 `d6f21ea9…c3a442`）拆分出的 `s_cleaned/`（500 题）；
  样本清单 `60q/sample-n60.json`（id 列表 sha256 `90c275b3eba1cccfbdaf4f980cebe879f9e390ea4380b4378995b7e97bfa8934`）。
- 代码：worktree `/Users/mac/hive/.wt/lm-notes`（`lm-notes` 分支，基线 `151dbe66`；runner 改动提交 `5a57058f`）。
- 证据目录：`go_hive/docs/e2e-assets/longmemeval/60q-notes/`（四组 raw rows、逐题 JSON、summary、缓存清单、复算输出）。

---

## 1. 方法与开关

### 1.1 上游 notes 语义（`HIVE_LME_NOTES=1`）

`core/tests/e2e/test_longmemeval.py:66` 在每题 seed 完历史、发问之前调用
`longmemeval.build_notes(hive_home, item, ...)`（`core/tests/e2e/longmemeval.py:278`）：

1. 把该题 `haystack_dates`/`haystack_sessions` 按日期**从旧到新**排序，对每个 seed 会话跑一次生产路径的
   `reflection_agent.reflect_on_messages`（统一短反思 prompt + 反思工具集）；因为逐会话写入同一
   `$HIVE_HOME/memories`，每个会话的反思能看到更早会话留下的笔记。
2. 笔记写到 `$HIVE_HOME/memories/global` 与 `$HIVE_HOME/memories/agents/queens/<queen>`。
3. 每题按提示词版本缓存：`$HIVE_LME_DATA/notes_cache/<model>/<prompt hash>/<variant>_<qid>`；
   命中即 `shutil.copytree` 到 home 的 `memories/`（首次逐会话构建，之后免费；`HIVE_LME_NOTES_VERSION`
   可钉住一个缓存版本）。
4. `record()` 在 notes 模式下额外写 `notes`（数量）、`notes_mode`、`notes_indexed`、`notes_recalled`、
   `notes_read` 五个字段，供事后判断 notes 是否真的到达 queen。

### 1.2 Go 等价开关（只动 e2e runner/探针）

`hive-longmemeval run --notes`（缺省读 `$HIVE_LME_NOTES`；语义同上游：未设或 `"0"` 为关，其余值为开）：

- **实现位置**：`go_hive/go-hive/internal/longmemeval/notes.go`（新）+
  `run.go`/`results.go`（接线与结果行）+ `cmd/hive-longmemeval/main.go`（旗标与 `notes` 子命令）。
  产品包（`internal/memory`、`internal/server`、`internal/agentloop` 等）**零改动**。
- **构建**：`BuildNotesForHome` 在每题 seed 之后、发问之前，调生产反思构建块
  `memory.ReflectionLoop` + `memory.BuildUnifiedShortReflectSystem(queen)` + `memory.ReflectionTools`，
  按日期升序逐会话写入该题 `HIVE_HOME` 的 global/queen 记忆目录。会话 transcript 的截断规则
  （最近 50 条、每条 800 rune、`[user]/[assistant]` 行、`Timestamp:` 用该会话自己的日期）对齐上游
  `_reflect_on`。
- **缓存**：与上游同布局（`<cache>/<safe(model)>/<prompt hash>/<variant>_<qid>`）；Go 的版本哈希
  `NotesVersion()` 同构（sha256(system prompt + tools JSON)[:12]）。命中缓存直接拷贝；未命中构建后
  「part 目录 → rename」原子发布。`notes` 子命令可跨题并行预建（`--concurrency`，命中即跳过）。
- **失败语义**：与上游一致——单个会话反思失败记数并继续（该会话笔记丢失），全部会话失败才报错。
- **结果行**：`notes` / `notes_mode` / `notes_indexed` / `notes_recalled` / `notes_read` 与上游同名字段；
  notes 关闭时结果行形状不变（`NotesExtra` 为空）。
- **等价性对照**（上游 → Go）：`HIVE_LME_NOTES=1` → `--notes`（或同名 env）；`HIVE_LME_NOTES_MODEL` →
  `--notes-model`（缺省同 `--model`）；`notes_cache/<model>/<hash>` → 同路径；`build_notes` 的
  「逐会话、由旧到新、缓存命中即拷贝」→ `BuildNotesForHome`；`record()` 的 notes 块 → `NotesTelemetry`。

仅有的实现差异（如实记录）：

| 项 | 上游 Python | Go |
|---|---|---|
| 反思时间戳 | `now=parse_date(session_date)`，`isoformat(timespec="minutes")` | 同一会话日期，`2006-01-02T15:04` |
| transcript 截断 | `content[:800] + "…"`（Unicode code points） | 同一规则（rune） |
| notes 缓存路径的模型段 | 模型名做 `\w.-` 替换 | `SafeModelName`（同一规则） |
| 反思失败 | `_reflection_loop` 吞掉单次失败、继续下一会话 | 同（计数后继续；全失败报错） |
| 日志 | `logger.warning` | 无 per-session 日志；结果行/`--verbose` 汇总失败的会话数 |

### 1.3 样本、模型与四组跑法

- 样本：`60q/sample-n60.json`，60 题显式 id；六类各 10 题（`single-session-user/assistant`、`multi-session`、
  `temporal-reasoning`、`knowledge-update`、`single-session-preference`），其中 `_abs` 弃权题 2 道。
  两侧传入**同一份显式 id 列表**。
- 答题模型：`deepseek-flash-1017` @ `https://token.tokenbroad.com/v1`（真实主槽，key 取自
  `~/.hive-go/env`，全程未打印）；notes 模型同答题模型；judge：`deepseek-v4-pro` @ 同端点。
- 每题仍是隔离 `HIVE_HOME` + 冻结时钟 + 官方 judge prompt（prompt 字节级一致性见 2026-11-21 证据）。
  默认 recall 模式为 `index`（两侧一致），所以 `notes_indexed` 是「笔记清单推送」，`notes_recalled`
  只在 `background` 模式才可能为真。

| 组 | 命令 | 运行目录 | 墙钟 |
|---|---|---|---|
| Go 无 notes（基线，引用） | `hive-longmemeval run --data <s_cleaned> --questions <60 ids> --run-dir …/runs/go-60`（单进程顺序） | `runs/go-60` | 15m29s |
| Py 无 notes（基线，引用） | `pytest tests/e2e/test_longmemeval.py -m live -n 4`（`60q/run-py-60.sh`） | `runs/py-60` | 4m24s |
| Go 有 notes | 先 `hive-longmemeval notes … --concurrency 24` 预建缓存（60/60，0 失败），再 `run --notes`（`60q/run-go-60-notes.sh`） | `runs/go-60-notes` | 预建 ~57m + 发问 3 批共 ~17m（与预建重叠） |
| Py 有 notes | `HIVE_LME_NOTES=1 pytest … -n 14`（`60q/run-py-60-notes.sh`） | `runs/py-60-notes` | 1:23:48（pytest 报告，含逐题 notes 构建） |

说明（如实）：

- 基线两组来自 2026-11-22 的 n=60 既有运行（worktree `7a14adf0`，见 `2026-11-22-60q-real-run.md`）；
  本轮四组中的有 notes 两组跑在 `151dbe66` + runner-only 改动上（`7a14adf0..151dbe66` 之间的
  `internal` 改动见 §3 不可比项）。
- Go 的有 notes 发问分 3 批（24/23/13 题）执行，追加到同一个 run dir，以便和仍在进行的 notes 预建重叠；
  每题的流程（新 home → 拷贝该题 notes → 发问 → 判分）与单批完全一致，最终 60 行全量汇总。
- 两侧 notes 均由各自实现生成（Go 用 `memory.ReflectionLoop` 生产构建块，Python 用
  `reflection_agent.reflect_on_messages`），缓存哈希目录不同（Go `feb123fc447c` / Py `c77ef7180f69`），
  没有共享笔记内容。

### 1.4 复算

```bash
# 四组总体/分题型/配对/差异题 + 逐题 JSON（读 60q-notes/ 下的四份 results.jsonl 副本）
uv run --offline python go_hive/docs/e2e-assets/longmemeval/60q/summarize_lme_notes_60q.py \
  --go-notes go_hive/docs/e2e-assets/longmemeval/60q-notes/go-60-notes-results.jsonl \
  --py-notes go_hive/docs/e2e-assets/longmemeval/60q-notes/py-60-notes-results.jsonl \
  --go-base  go_hive/docs/e2e-assets/longmemeval/60q/go-60-results.jsonl \
  --py-base  go_hive/docs/e2e-assets/longmemeval/60q/py-60-results.jsonl \
  --json go_hive/docs/e2e-assets/longmemeval/60q-notes/summary-60q-notes.json \
  --per-question-dir go_hive/docs/e2e-assets/longmemeval/60q-notes

# 原始重跑（需真实 key；脚本不打印 key）
bash go_hive/docs/e2e-assets/longmemeval/60q/run-go-60-notes.sh   # 预建缓存 + 60 题 run --notes
bash go_hive/docs/e2e-assets/longmemeval/60q/run-py-60-notes.sh   # HIVE_LME_NOTES=1 的 60 题
```

## 2. 结果

### 2.1 总体（四组）

| | 无 notes | 有 notes | 配对翻题（有 vs 无） | McNemar |
|---|---|---|---|---|
| **Go** | 52/60 = **86.7%**（75.8–93.1） | 54/60 = **90.0%**（79.9–95.3） | +5 / −3 | p = 0.727 |
| **Python** | 51/60 = **85.0%**（73.9–91.9） | 50/60 = **83.3%**（72.0–90.7） | +4 / −5 | p = 1.000 |

| 指标 | Go 无 | Go 有 | Py 无 | Py 有 |
|---|---:|---:|---:|---:|
| task-averaged（六类均值） | 86.7% | 90.0% | 85.0% | 83.3% |
| abstention（2 题） | 1/2 | 1/2 | 1/2 | 1/2 |
| searched | 61.7%（37/60） | 60.0%（36/60） | 68.3%（41/60） | 63.3%（38/60） |
| evidence retrieved（58 非弃权题） | 58.6%（34/58） | 58.6%（34/58） | 67.2%（39/58） | 58.6%（34/58） |
| 每题中位耗时 | 7.2s | 9.9s | 7.3s | 10.9s |
| error / judge_error | 0 / 0 | 0 / 0 | 0 / 0 | 0 / 0 |

有 notes 两组均 **60/60 完成、无 error、无 ungraded**；耗时变长与「每题先准备/拷贝 notes、prompt 多出
笔记清单」一致，稳在 +3s 量级。

### 2.2 分题型

| question_type | Go 无 | Go 有 | Py 无 | Py 有 |
|---|:--:|:--:|:--:|:--:|
| single-session-user | 10/10 | 10/10 | 10/10 | 10/10 |
| single-session-assistant | 10/10 | 10/10 | 10/10 | 10/10 |
| multi-session | 7/10 | 6/10 | 7/10 | 7/10 |
| temporal-reasoning | 10/10 | 10/10 | 10/10 | **8/10** |
| knowledge-update | 9/10 | 8/10 | 8/10 | 9/10 |
| single-session-preference | 6/10 | **10/10** | 6/10 | 6/10 |
| ALL | 52/60 | 54/60 | 51/60 | 50/60 |

- Go 的 +2 题净变化由 **preference +4** 与 **multi-session −1 / knowledge-update −1** 构成；
- Python 的 −1 题由 **temporal −2 / knowledge-update +1** 构成，preference 仍是 6/10；
- 两个 single-session 类在四组里都是 20/20 满分，notes 的影响集中在需要跨会话聚合或按偏好作答的题型。

### 2.3 notes 确实生效（telemetry）

| | Go 有 notes | Py 有 notes |
|---|---:|---:|
| 每题 notes 数（均值 / min–max） | 42.4 / 22–62 | 46.4 / 25–64 |
| `notes_mode` | `index`（60/60） | `index`（60/60） |
| `notes_indexed`（清单进 prompt） | **60/60** | **60/60** |
| `notes_recalled`（background 注入） | 0/60（非 background 模式，预期） | 0/60（同） |
| queen 实际读过笔记的行数 / 次数 | 20/60 / 35 次 | 41/60 / 65 次 |
| 缓存（每题目录 / 全量大小） | 60 题，`feb123fc447c` | 60 题，`c77ef7180f69` |
| 反思失败 | 0（预建日志逐题 `failed reflections: 0`） | 无记录（上游吞掉单会话失败，未见报错） |

逐题清单：`60q-notes/notes-cache-manifest.txt`（每题 notes 数 / 字节数 / 最后写入时刻）、
`summary-60q-notes.json` 的 `groups.*.notes_telemetry.rows`（`notes`/`notes_mode`/`notes_indexed`/`notes_recalled`/`notes_read`）。
`notes_indexed=60/60` + 非零 `notes_read` 是「开关不是装饰」的直接证据：两侧每一题的 queen prompt 都拿到了
笔记清单，且相当比例的题真的打开了笔记文件。

### 2.4 配对对比与差异题

| 对比 | 双对 | 双错 | 仅前者对 | 仅后者对 | McNemar p |
|---|---:|---:|---:|---:|---:|
| Go 有 vs Py 有（notes 处理下） | 48 | 4 | 6 | 2 | 0.289 |
| Go 有 vs Go 无 | 49 | 3 | 5 | 3 | 0.727 |
| Py 有 vs Py 无 | 46 | 5 | 4 | 5 | 1.000 |
| （参考）Go 无 vs Py 无 | 48 | 5 | 4 | 3 | 1.000 |

**Go：notes 帮助的题（5）**：`07b6f563`、`32260d93`、`afdc33df`、`caf03d32`（均 preference）、`edced276`（multi-session）。
**Go：notes 伤害的题（3）**：`3a704032`、`46a3abf7`（multi-session）、`e61a7584`（knowledge-update）。

**Python：notes 帮助的题（4）**：`505af2f5`、`afdc33df`、`b0479f84`（preference）、`e61a7584`（knowledge-update）。
**Python：notes 伤害的题（5）**：`07b6f563`、`a89d7624`、`caf03d32`（preference）、`6e984301`、`c8090214`（temporal-reasoning）。

**notes 处理下 Go 独对（6）**：`07b6f563`、`32260d93`、`a89d7624`、`caf03d32`（preference）、`6e984301`、`c8090214`（temporal）。
**notes 处理下 Py 独对（2）**：`46a3abf7`（multi-session）、`e61a7584`（knowledge-update）。

### 2.5 差异题性质（抽查 judge 文本，避免把噪声当信号）

- `07b6f563` / `caf03d32` / `e61a7584` 等题：两侧答案的实质内容高度相近（同一偏好清单 / 同一
  「九个月」结论），judge 却给出 `Yes` vs `no` 的相反单次判定 —— 典型单次 LLM judge 的阈值噪声；
  这类翻题在无 notes 基线里也出现过（如 `3a704032` 当时被记为判分噪声）。
- `edced276`（multi-session，旅行天数）与 `afdc33df`（preference）：基线两侧都错，有 notes 后两侧都对
  —— 更像 notes 让检索/聚合路径真的变了，而不是判分。
- `46a3abf7`（multi-session，鱼缸数量）：Go 基线答对、「Two tanks」，有 notes 后仍答「Two tanks」被判错；
  Py 有 notes 答「Three, counting all of them」被判对。答案本身随运行变化，属于模型每次检索/措辞路径不同。
- 因此：**不把任何单题翻题归因为 notes 的系统性作用**；总体结论按 §2.1 的配对统计表述。

## 3. 结论、不可比项与边界

1. **开关落地结论（可确证）**：Go `--notes` 与上游 `HIVE_LME_NOTES=1` 语义等价（逐会话、由旧到新、
   写入 home 的 global/queen 记忆目录、按提示词版本缓存、命中即拷贝、notes telemetry 同名字段），
   且本轮 60 题全部真实生效（`notes_indexed` 60/60，非零 `notes_read`，缓存 60/60，0 反思失败）。
   实现只在 e2e runner/CLI/docs/scripts，产品 Go 包零改动。
2. **分数结论（n=60 不足以定性）**：Go 有 notes 90.0% vs 无 notes 86.7%（+2 题，p=0.727）；
   Python 有 notes 83.3% vs 无 notes 85.0%（−1 题，p=1.000）；notes 处理下 Go 比 Py 高 4 题
   （p=0.289）。方向不一致、均不显著；翻题里还混有判分噪声。**本轮不主张 notes 涨分或掉分**，
   只提供四组可复算证据。
3. **不可比项**：
   - 基线两组跑在 `7a14adf0`；本轮有 notes 两组跑在 `151dbe66` + runner-only 改动。
     `7a14adf0..151dbe66` 之间 `internal` 有 14 个提交（memory reflection 的 worker 槽位、recall 索引化、
     notes 推送/`memory_recall` 配置、chat-app gate 等）。无 notes 基线与有 notes 组的差异因此混入了
     这些产品改动；有 notes 组内的 Go vs Py 对照不受此影响（同 commit、同一 runner）。
   - 两侧 notes 由各自实现生成（模型同一，提示词各自 1:1 移植），notes 数量/内容有差异
     （Go 均值 42.4 vs Py 46.4），所以 Go vs Py 的 notes 差异 = 实现差异 + 笔记内容差异，未做
     「同一份 notes 两侧共用」的隔离实验（若需要，可把一侧缓存按另一侧哈希布局复制后复跑）。
   - judge 是自建基线（`deepseek-v4-pro`，单次判定），不是官方 `gpt-4o-2024-08-06`；绝对分与发布分不可比。
     单次 judge 对实质相近答案的阈值噪声在 preference/temporal 上尤其明显（§2.5）。
   - Go 的发问分 3 批、与 notes 预建重叠，Python 是单次 xdist 运行；两者每题流程一致，但并发/时序不同。
   - `notes_recalled` 全为 0 是默认 `index` recall 模式的预期行为，不代表 notes 未生效；生效证据看
     `notes_indexed` / `notes_read`。
4. **复现口径**：`60q/run-go-60-notes.sh` / `run-py-60-notes.sh` 为本次两侧命令；四组汇总用
   `60q/summarize_lme_notes_60q.py`（§1.4）；所有数字来自 `60q-notes/` 下的四份 `results.jsonl`
   副本与 `summary-60q-notes.json`，可离线重算。

## 4. 产物清单（`60q-notes/`）

| 文件 | 内容 |
|---|---|
| `go-60-notes-results.jsonl` / `py-60-notes-results.jsonl` | 两侧有 notes 的 60 行原始结果（含 notes telemetry） |
| `go-60-notes-summary.txt` / `py-60-notes-summary.txt` | 两侧运行自带的文字汇总 |
| `go-60-notes-summary.json` | Go 运行的机器汇总 |
| `go-notes-per-question.json` / `py-notes-per-question.json` | 逐题 JSON（id/type/expect/pred/hit/seconds/judge/notes*） |
| `summary-60q-notes.json` / `summary-60q-notes.txt` | 四组 + 配对 + 差异题的机器/文字汇总 |
| `notes-cache-manifest.txt` | 两侧 notes 缓存逐题清单（notes 数 / 字节数 / 写入时刻） |

逐题的完整会话原文（`transcripts/<qid>.json`）留在运行目录
`~/.hive-go/longmemeval-data/runs/{go,py}-60-notes/transcripts/`（未入仓，避免大文件）。
