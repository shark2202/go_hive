# 06 · 记忆与 Soul

> 关键文件：
> `agents/queen/queen_memory_v2.py`、`memory_log.py`、`reflection_agent.py`、`recall_selector.py`、`timeline.py`
> `agent_loop/memory_reminders.py`、`past_conversations_reminder.py`
> `tools/src/memory_tools/`（索引、BM25、`search_messages`、`search_timeline`）
> `agents/queen/soul.py`、`soul_reflection.py`

## 1. 三个作用域 + 两种载体

### 1.1 记忆文件

```text
~/.hive/memories/
├── global/                     # 跨所有 Queen / Colony
├── colonies/<name>/            # 某个 Colony
├── agents/queens/<queen_id>/   # 某个 Queen
└── agents/<name>/              # 某个 Worker Agent
```

每个记忆是一份独立 `.md` 文件，可带 YAML frontmatter：

```yaml
---
name: user-commute
type: preference
description: How the user gets to work
author: owner | queen | reflection
learned: 2026-11-09
confirmed: 2026-11-20
---
正文...
```

硬限制：

| 限制 | 值 |
|---|---:|
| 每作用域文件数 | 最多 200 |
| 单文件 | 最多 4 KB |
| 全局记忆类别 | `profile` / `preference` / `environment` / `feedback` |

### 1.2 对话历史

所有会话的 `events.jsonl` + 落盘的 `data/` 工具结果构成“原始历史”。它不经过记忆压缩，是 `search_messages` 与自动召回的数据源。

二者关系：**记忆笔记是提炼，对话历史是证据**。Queen 被明确要求：在问用户之前，先查记忆和对话。

## 2. 反思（Reflection）：笔记怎么写

### 2.1 触发节奏

`configuration.json`：

```json
"memory_reflection": {"every_turns": 6, "long_every": 4, "model": "worker"}
```

| 键 | 默认 | 含义 |
|---|---:|---|
| `every_turns` | 6 | 每完成 6 个“回答用户的回合”反思一次 |
| `long_every` | 4 | 每 4 次反思做一次“长反思”：合并重复、整理归档、删除冗余 |
| `model` | `worker` | `worker` 用 worker_llm（未配置则用 Queen 模型）；`main` 用 Queen 自己的模型 |

另外两个触发点：

- **上下文压缩时**：压缩会丢弃细节，顺势反思一次；
- **会话结束时**：如果自上次反思后有新内容，再反思一次。

### 2.2 反思做什么

`reflection_agent.py` 给反思模型两个工具：

- `write_memory_file(scope, filename, content)`
- `delete_memory_file(scope, filename)`

短反思：读最近对话，写/改 1–2 个文件。长反思：读全部记忆文件，合并重复、移动作用域、删除过时项。

作用域由反思模型显式选择（global / colony / queen / agent），不是硬编码。

### 2.3 记忆变更日志

`memory_log.py` 为每个作用域维护 `log.jsonl`，每行记录：

- 时间、作者（`owner` / `queen` / `reflection`）、原因；
- 变更前文本、变更后文本；
- 证据（引用的对话片段）。

三个关键规则：

1. **用户写的文件归用户**：`author: owner` 的文件，反思不能覆盖或删除；它会把学到的东西写到另一个文件。
2. **年龄从“最后一次被确认”算**：`confirmed` 是最后一次有对话支持该事实的日期；整理措辞不会让旧事实显得新鲜。
3. **删除可恢复**：被删除文件留在日志里，Memory 页面可查看历史并恢复任意版本。

### 2.4 记忆如何到达 Queen

`memory_recall.mode` 有三种（`config.py:1058`）：

| 模式 | 行为 | 代价 |
|---|---|---|
| `index`（默认） | 会话开始/压缩后给全量“文件名 + 一行描述”，之后只给变化；Queen 自己决定打开哪些 | 无额外模型调用 |
| `wait` | 每条消息前由模型挑选相关笔记，最多等 `wait_seconds`（默认 10s） | 每消息一次模型调用，约 +2s |
| `background` | 同样的挑选在 Queen 回答时并行进行，常常晚一条消息才到 | 每消息一次模型调用，不阻塞 |

实现：

- `MemoryIndexReminderSource`（index 模式）：增量清单；
- `MemoryRecallReminderSource`（wait/background）：挑选内容。

v1.0.15 的基准显示 `index` 与 `wait` 在 108 题上并列 101 正确，而 `index` 不增加延迟，因此成为默认。

## 3. 会话召回（Past Conversations）

### 3.1 目标

Queen 常常“不觉得该查历史”，直接问用户或泛泛回答。自动召回在她回答前把相关历史片段放到眼前。

### 3.2 索引与检索

`tools/src/memory_tools/`：

```text
$HIVE_HOME/.message_index/
├── events/<scope>/<owner>/<session>/<NNNNNN>.<role>.txt
├── data/<scope>/<owner>/<session>/<spill>.txt
├── meta/<scope>/<owner>/<session>/cursor.json
└── recall/<scope>/<owner>.sqlite3      # BM25 倒排索引
```

- 索引是**按作用域/所有者**的 SQLite 倒排表；首次使用惰性构建，后台刷新；
- 查询时用 BM25（`k1=1.2, b=0.75`）；
- 最多 **12** 个查询词，返回 **6** 段；
- 用户与助手各取 **500** 字符；
- 支持中英文：CJK 双字切分；英文用词干前缀；
- 同一会话中已命中 top hit 时，会再扩展最多 2 段/会话、总 4 段；
- 明确排除当前会话；
- 检索在 `HIVE_PAST_RECALL_TIMEOUT`（默认 **3s**）内完成，超时则本轮不带召回继续。

### 3.3 注入

`PastConversationsReminderSource` 在 `SESSION_START` 与 `USER_PROMPT_SUBMIT` 触发，仅 Queen，且仅当 provider 已接线。消息短于 8 字符（“ok”“谢谢”）不召回。

格式是按日期排序的短摘录块，作为 `<system-reminder>` 放在用户消息前。

### 3.4 `search_messages`

Queen 也能主动搜索：

```python
search_messages(
  pattern="(?i)commute",
  session=None,
  role="all",              # user / assistant / tool / all
  match_source="all",      # events / data / all
  since="2026-01-01",
  until=None,
  max_matches=20,
  context="turn",          # none / narrow / turn
  turn_char_cap=4000,
  per_message_char_cap=800,
)
```

- 正则使用 Rust/RE2 子集，禁止回溯型构造；
- 搜索范围包含 **工具结果正文**（包括落盘的大结果）；
- 日期过滤在同步与命中两层都生效（v1.0.15 修复）。

## 4. 时间线（Timeline，可选）

### 4.1 解决什么问题

`search_messages` 找“说过什么”，但回答不了“今年参加了几场婚礼”“上周做了什么”。时间线把用户陈述的**事件、事实、计划**抽取成带日期的条目。

### 4.2 机制

- 位置：`<session>/timeline.jsonl` + `timeline_meta.json`（抽取游标）；
- 只读**用户消息**（约占文本 1/10）；
- 每条包含：`session`、`turn`、`kind`（event/fact/plan）、自包含的 `text`、`start`/`end`（相对日期已解析）、2–4 个别名；
- 抽取在会话进行中（每 3 条新用户消息或 300s 冷却）、会话停止时、以及历史回填时进行；
- 单次抽取最多 12,000 字符用户文本；
- 用 Queen 的模型（`memory_reflection.model` 同一选择逻辑）；
- 默认**关闭**，需 `memory_timeline=true` 或 `HIVE_MEMORY_TIMELINE=1`。

### 4.3 `search_timeline`

开启后，Queen 获得 `search_timeline` 工具；它与 `search_messages` 并列出现在 `context_awareness` 类别。

## 5. LongMemEval：记忆到底有多好

仓库自带评测记录（`docs/memory-benchmark.md`），口径值得完整引用：

| 配置 | LongMemEval-S | LongMemEval-M |
|---|---:|---:|
| **Hive V1** | **88.4%** | **82.0%** |
| V1 前的 Hive | 77.0% | 未跑 |
| 直接给整段历史 | 96.8% | 不可行（约 1.1M tokens） |
| 只给包含答案的会话 | 97.6% | 97.8% |

按题型（S / M）：

| 题型 | S | M |
|---|---:|---:|
| 用户陈述的事实 | 100.0% | 92.9% |
| 助手说过的话 | 78.6% | 76.8% |
| 用户偏好 | 63.3% | **30.0%** |
| 跨会话组合事实 | 82.0% | 78.9% |
| 随时间变化的事实 | 96.2% | 91.0% |
| 日期推理 | 94.0% | 88.0% |

关键结论（来自同一文档）：

- V1 记忆工作把 S 从 77.0% 提到 88.4%；
- 但**当整段历史能塞进上下文时，直接读历史比 Hive 的记忆高 8.4 分**；
- 最大短板是偏好（M 上仅 30%）与“助手说过的话”（约 77–79%）；
- 笔记清单（index 模式）在 108 题上从 94 提到 101，且不增加中位延迟；
- 笔记的**一行描述**承担了大部分价值：500 题里 Queen 只在 12 题真正打开了笔记。

这是 Hive 文档中少见的“诚实评测”：它明确公布了自己的上限与失败模式。

## 6. Soul：Queen 的自我叙事

### 6.1 它是什么

- Profile 是“她应该是谁”；
- Memory 是“她知道什么”；
- **Soul 是“她变成了谁”**：一段第一人称、短小、常驻系统提示的自我记录。

默认结构（`config.py:1242`）：

| key | 标题 | 字符上限 | 其他规则 |
|---|---|---:|---|
| `now` | Now: what's on my mind | 300 | 14 天后过期 |
| `people` | Me and them | 800 | 每条需要 2 个不同来源的引文 |
| `threads` | Open threads and promises | 600 | — |
| `views` | What I think, and why | 600 | 2 个来源 |
| `changes` | How I've changed | 400 | 2 个来源 |

### 6.2 每日反思

`configuration.json`：

```json
"soul": {
  "queens": {
    "queen_archivist": {
      "enabled": true,
      "mode": "review",
      "hour": 4,
      "max_edits": 5,
      "sections": [...]
    }
  }
}
```

| 键 | 默认 | 含义 |
|---|---|---|
| `mode` | `review` | 变更等用户批准；`auto` 立即应用 |
| `hour` | 4 | 本地时间几点后运行 |
| `model` | `queen` | `queen` 用她的模型；`worker` 用 worker 模型 |
| `max_edits` | 5 | 每日反思最多改几条 |
| `bootstrap_edits` | 30 | 首次草稿最多改几条 |
| `sources` | 无 | 渠道会话映射 + key 过滤（只读某些群聊） |
| `include_titles` / `exclude_titles` | 无 | 按会话标题筛选 |
| `bootstrap_memories` | 无 | 首稿可读的记忆文件 |
| `session_chars` / `message_chars` | 12000 / 400 | 单会话与单条消息读取上限 |

### 6.3 证据约束（代码强制）

`soul_reflection.py` 不是“让模型自由写”，而是：

- 每个新增/改写必须给**逐字引文**；
- 引文必须真的出现在它读过的对话里（`_check_evidence`）；
- `evidence: 2` 的 section 需要来自**不同人/不同会话**的两条引文；
- 每次反思最多 `max_edits` 条；
- 任一 section 超过字符上限则拒绝保存；
- 超过 `max_age_days` 的条目在反思时自动淘汰。

### 6.4 审阅、回滚与探针

- 存储：`queens/<id>/soul/`
  - `soul.json` 当前版本；
  - `history/` 每个版本；
  - `proposals/` 待审提案；
  - `log.jsonl` 变更日志；
  - `state.json` 每个会话读到哪。
- `review` 模式下，提案在 Queen 的 Profile → Soul 页面逐条批准/编辑/回滚。
- `probes.json` 可以放固定问题与“她的语气锚点”；“Try her with these changes” 会用当前 Soul 与提案后 Soul 各回答一次，并排展示。
- 有提案未决时，下一次反思会等待，避免堆积。

### 6.5 它在提示中的位置

Soul 紧跟在身份/人格之后，进入系统提示。因此它影响**每一个会话**，而不是某个会话的临时上下文。

## 7. 记忆与 Soul 的分工

| | Memory | Soul |
|---|---|---|
| 回答 | 我知道什么 | 我成了谁 |
| 形态 | 多份文件、可检索 | 一份短文、常驻提示 |
| 写入 | 反思工具、用户编辑 | 每日反思提案 |
| 粒度 | 事实/偏好/环境/反馈 | 心境、关系、承诺、观点、变化 |
| 审阅 | 可查看历史、恢复 | review/auto，可逐条批准 |
| 风险 | 过时、重复、描述误导 | 自我叙事漂移、过度拟合少量对话 |

## 8. 评价

**做得好的地方**

- 记忆是**文件**，可读、可 diff、可回滚，不引入向量库运维。
- 召回是 BM25 + 结构化索引，延迟可控（约 30ms 读索引；召回 3s 超时），不依赖 embedding 服务。
- 对“记忆到底有没有用”做了端到端评测，并公开了失败题型。
- 用户写的记忆有明确所有权，反思不能覆盖——这是很少见但正确的设计。
- Soul 的证据逐字校验，把“人格演化”从玄学拉回到可审计。

**局限**

- 偏好类记忆在长历史（M）上只有 30%，说明“一次性提到的细节”仍是硬伤。
- 自动召回偏向用户原话，助手此前的回答被截短，导致“助手说过什么”题型偏弱。
- 记忆是 markdown + LLM 维护，规模上来后去重/一致性靠长反思，仍有漂移风险。
- 时间线默认关闭，因为成本/收益不显著（37 个剩余错题上 +7/−2，统计不显著）。
